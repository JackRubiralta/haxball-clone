package control

import (
	"phootball/internal/geom"
	"phootball/internal/sim"
)

// Off-ball play. Exactly one outfielder (the elected presser) goes for the ball; everyone
// else holds the dynamic formation shape, makes a supporting run when we attack, or marks
// space when we defend. This is what keeps players spread out and kills the kickoff swarm.

// press drives the elected presser to win the ball: it runs to the predicted intercept
// point (leading the ball, not chasing its current spot) and uses trap to take a clean
// touch or to steal a fresh enemy touch.
func (a *AI) press(p perception, plan teamPlan) sim.Intent {
	in := a.abortCharge(p, sim.Intent{})
	reach := p.me.Radius() + p.ballRadius
	ip := interceptPoint(p.me.Position(), p.me.Tuning().MaxSpeed, p.me.Tuning().TurnRate, p.me.Heading(), reach, p.ball, p.ballVel, p.friction, p.dt, a.tune)

	// Receiving an incoming pass (a loose, moving ball our side is collecting uncontested):
	// don't charge the fast ball head-on at the first point we can touch it -- run ONTO its
	// trajectory and meet it a little deeper, where it has slowed to a controllable pace, for a
	// clean first touch. A genuine 50/50 still goes for the earliest intercept (win the race).
	receiving := a.receivingPass(p)
	if receiving {
		ip = a.receivePoint(p)
	}

	// At a kickoff the defending side must not barge the spot before the ball is in play;
	// hold just outside the centre circle until it moves.
	if kickoffActive(p) && p.view.KickoffSide() != p.me.Side() {
		ip = a.kickoffStandoff(p)
		receiving = false // hold the standoff at full readiness, don't pace-match a non-pass
	}

	// Pace the reception to arrive WITH the ball (no overshoot) instead of sprinting through the
	// meeting point; a genuine 50/50 (receiving false) keeps the flat-out steer.
	mv, th := a.steer(p, ip, false)
	if receiving && a.tune.receiveMatch {
		mv, th = a.steerReceive(p, ip)
	}
	in.Move, in.Throttle = mv, th
	// The presser faces the ball: in the directional move model its run is toward the ball anyway
	// (≈ forward, so no speed penalty), and facing it is what lets it trap/settle a loose ball
	// cleanly the moment it arrives.
	in.Aim = a.aimToward(p, p.ball)

	// Deep in our own third under pressure: DRIVE straight in to win the ball, skipping the trap
	// below -- the trap centre-pull would hold the ball at a standoff just outside touch range, so
	// the presser could never gain control to clear it. Hold shoot as we drive in: it builds NO
	// charge off the ball (the m.touching gate in Match.applyIntent), but the instant we touch, the
	// charge winds up AND the shoot-contact keeps the ball glued to the front; onBall's clear path
	// then lines it up and fires the charged boot. (There is no off-ball pre-charge -- holding shoot
	// here never fires until onBall releases it, so it can't go off as a zero-charge tap.)
	if a.shouldDriveInClear(p) {
		in.ShootHeld = true
		return in
	}

	// Poke-tackle: when pressing an opponent who has the ball at very close range, nick it off them
	// with a quick middle-click jab (sends it upfield off the opponent) instead of dwelling to set
	// up a trap-steal. A visible, tactical use of the push ability.
	if a.shouldPokeSteal(p) {
		a.lastOnBall = actDribble // a defensive poke is not a deliberate pass/shot/clear -- don't let it be mis-counted as one
		return a.pushIntent(p)
	}

	// Trap to take a clean touch or steal -- but NOT in a 50/50 race: trap halves our speed,
	// so if an opponent can reach the ball about as soon as we can, contest it at full pace
	// instead of slowing down and losing it.
	if (a.wantTrapReceive(p) || a.wantTrapSteal(p)) && !a.contested(p) {
		in.Trap = true
	}
	return in
}

// shouldDriveInClear reports whether the presser is about to win a loose ball deep in its own
// third under pressure (a dangerous situation) and should drive straight in to control it for a
// charged clearance, rather than trapping (which would hold the ball at a standoff out of touch
// range). Gated on a short ETA so the player doesn't abandon a clean reception far from the ball.
func (a *AI) shouldDriveInClear(p perception) bool {
	if p.carrierEnemy {
		return false // an opponent controls it: steal, don't barge in
	}
	frac := (p.ball.X - p.ownGoal.X) * p.attackX / p.view.Field().Width() // 0 own goal..1 enemy goal
	if frac > a.tune.clearThird {
		return false
	}
	if p.pressureOnCarry < a.tune.actPressure {
		return false // uncontested in our third: control it and play out, don't hoof it away
	}
	reach := p.me.Radius() + p.ballRadius
	eta := interceptTime(p.me.Position(), p.me.Tuning().MaxSpeed, p.me.Tuning().TurnRate, p.me.Heading(), reach, p.ball, p.ballVel, p.friction, p.dt, a.tune)
	return eta <= a.tune.driveClearETA
}

// receivingPass reports that the ball is an in-flight pass this player should glide onto and
// receive rather than a 50/50 to win at the earliest point: it is loose (no firm carrier),
// genuinely moving (a played ball, not a near-stopped one), our side is the one collecting it
// (teamControls), and no opponent can contest the intercept. Under those conditions meeting it
// a touch deeper on its path -- where it has slowed -- gives a clean reception with no risk.
func (a *AI) receivingPass(p perception) bool {
	if !p.ballLoose {
		return false // someone has firm possession -> not a ball in flight
	}
	if geom.Norm(p.ballVel) < a.tune.receiveMinSpeed {
		return false // basically stopped -> a loose ball to win, not a pass to receive
	}
	if !p.teamControls {
		return false // the other side is better placed -> not ours to receive
	}
	return !a.contested(p) // a real 50/50 -> take the earliest intercept, don't dawdle
}

// receivePoint returns where on the ball's predicted path the receiver should meet an incoming
// pass: the SOONEST point it can reach where the ball has also slowed to a controllable speed,
// so it runs onto the trajectory and takes a clean touch instead of charging the fast ball.
// Falls back to the earliest reachable point if the ball never slows enough within the horizon.
// receiveControlSpeed is the ball speed at/under which the receiver can take a clean first touch:
// a fraction (receiveControlFrac) of its own CaptureSpeed -- the impact speed below which the ball
// sticks instead of bouncing. Deriving it from the LIVE capture means it tracks any tuning of the
// capture physics automatically, so the receiver always meets a pass where the ball will actually
// stick rather than at a stale hard-coded constant.
func (a *AI) receiveControlSpeed(p perception) float64 {
	return p.me.Tuning().CaptureSpeed.Front * a.tune.receiveControlFrac
}

func (a *AI) receivePoint(p perception) geom.Vec {
	controlSpeed := a.receiveControlSpeed(p)
	reach := p.me.Radius() + p.ballRadius
	from := p.me.Position()
	maxSpeed := p.me.Tuning().MaxSpeed
	turnRate := p.me.Tuning().TurnRate
	heading := p.me.Heading()
	penalize := a.tune.turnPenaltyGain > 0 && turnRate > 0 && geom.Norm(heading) > 1e-9

	var earliest, deepest geom.Vec
	haveEarliest := false
	for t := 0.0; t <= a.tune.interceptHorizon; t += a.tune.interceptStep {
		target := predictBall(p.ball, p.ballVel, t, p.friction, p.dt)
		usable := t
		if penalize {
			usable = t - a.tune.turnPenaltyGain*geom.AngleBetween(heading, target.Sub(from))/turnRate
			if usable < 0 {
				usable = 0
			}
		}
		if geom.Dist(from, target)-reach > maxSpeed*usable {
			continue // can't reach this point of the path in time
		}
		if !haveEarliest {
			earliest, haveEarliest = target, true
		}
		deepest = target // furthest-along reachable point so far (ball slowest, met moving WITH it)
		if ballSpeedAt(p.ballVel, t, p.friction, p.dt) <= controlSpeed {
			return target // soonest reachable point where the ball is controllable
		}
	}
	if haveEarliest {
		// The ball never slows below the clean-capture speed within reach (a hot short pass that
		// can't decelerate enough over the lane). Meeting it at the EARLIEST point takes it nearly
		// head-on -- a high RELATIVE impact speed (approachSpeed = playerVel-ballVel along the
		// contact normal), so it bounces off. Meeting it at the DEEPEST reachable point instead has
		// the receiver running in the ball's own travel direction, so the ball catches up from
		// behind: the relative impact speed is far lower and the touch sticks even though the ball's
		// absolute speed is still high. (Interception risk barely rises -- intercepts are a tiny
		// share of failures -- and a deeper meeting point is also a slower ball.)
		if a.tune.receiveDeepenHot {
			return deepest
		}
		return earliest
	}
	return predictBall(p.ball, p.ballVel, a.tune.interceptHorizon, p.friction, p.dt)
}

// contested reports whether an opponent can reach the ball about as quickly as this player,
// so slowing to trap would lose the race.
func (a *AI) contested(p perception) bool {
	reach := p.me.Radius() + p.ballRadius
	mine := interceptTime(p.me.Position(), p.me.Tuning().MaxSpeed, p.me.Tuning().TurnRate, p.me.Heading(), reach, p.ball, p.ballVel, p.friction, p.dt, a.tune)
	for _, o := range p.opponents {
		// An opponent's speed/turn-rate are hidden (not rendered), so assume the nominal
		// values; its committed steering heading is hidden too, so use its VISIBLE facing.
		ot := interceptTime(o.Position(), a.tune.assumedOppSpeed, a.tune.assumedOppTurn, o.Facing(), o.Radius()+p.ballRadius, p.ball, p.ballVel, p.friction, p.dt, a.tune)
		if ot <= mine+a.tune.contestMargin {
			return true
		}
	}
	return false
}

// kickoffStandoff returns a holding point just outside the centre circle on the defending
// presser's side, so it is ready to pounce the instant the ball is played.
func (a *AI) kickoffStandoff(p perception) geom.Vec {
	c := p.view.Field().CenterSpot()
	dir := geom.NewVec(-p.attackX, 0) // back toward our own goal, away from the spot
	return c.Add(dir.Scale(p.view.Field().CenterCircleRadius() + p.me.Radius() + 6))
}

// offBall positions a non-presser: when we have the ball it moves to a spot where it is a
// real passing option (an open lane from the carrier that avoids opponents, with space and
// advancement), breaking into space for a give-and-go just after it has passed; when the
// opponent has the ball the cover marks the danger; otherwise it holds formation shape. It
// keeps facing the ball so an arriving pass is received cleanly (with trap if it is coming).
func (a *AI) offBall(p perception, plan teamPlan) sim.Intent {
	in := a.abortCharge(p, sim.Intent{})

	// COLLECT an incoming pass: if a ball our side is uncontested-collecting is in flight and we are
	// the nearest of our side to it, GO GET IT (run onto its line and receive with trap) instead of
	// holding a formation/support spot. The elected presser already does this in press(), but the
	// intercept-TIME election sometimes picks a different (worse-placed) player, leaving the actual
	// nearest man -- usually the intended receiver -- to keep repositioning and DRIFT off the spot the
	// pass was aimed at. That is the measured "ball arrives a player-length from the open man" miss
	// (about half of failed passes had the receiver moving AWAY from the ball at closest approach).
	// Gated to the single nearest man, so it does not create a second chaser (no swarm).
	if a.receivingPass(p) && a.nearestToBallOnMyside(p) {
		ip := a.receivePoint(p)
		mv, th := a.steerReceive(p, ip)
		in.Move, in.Throttle = mv, th
		in.Aim = a.aimToward(p, p.ball)
		if a.wantTrapReceive(p) {
			in.Trap = true
		}
		return in
	}

	target := idealPosition(p, a.tune)
	supporting := false
	switch {
	case p.teamControls && p.view.Tick() < a.runUntil:
		target = a.receiveSpot(p, a.tune.runForwardBias) // give-and-go run into space (deliberately moving)
		supporting = true
		a.holdSpotOK = false // a runner is not a stationary target; don't hold a spot during the run
	case p.teamControls:
		target = a.supportHoldSpot(p) // offer a STATIONARY passing option (held spot) so passes don't over-hit
		supporting = true
	case p.carrierEnemy && plan.support == p.me.ID():
		target = a.markSpot(p)
		a.holdSpotOK = false
	default:
		a.holdSpotOK = false
	}
	target = a.avoidKeeperOnlyBox(p, target) // don't path into a keeper-only goal area we'd be evicted from

	mv, th := a.steer(p, target, true)
	// Spread off the ball: repel the MOVEMENT away from teammates that are too close (a boids
	// separation from this player's OWN position, not its target -- nudging the target instead
	// just makes runs cross). If an idle player is in a near-collision, a small throttle floor
	// steps it apart. Applied in the holding / defending / marking phases -- NOT to the elected
	// presser (free to chase the ball) and NOT to an attacking support run, whose precise
	// receiving line must be preserved for clean passing.
	if a.tune.separationGain > 0 && !supporting && !a.wantTrapReceive(p) {
		if push := a.teammatePush(p); push != (geom.Vec{}) {
			mv = geom.Unit(mv.Add(push.Scale(a.tune.separationGain)))
			if th < a.tune.separationMinThrottle {
				th = a.tune.separationMinThrottle
			}
		}
	}
	in.Move, in.Throttle = mv, th
	// Facing: the player CLOSEST of our side to a loose ball is the backup behind the presser and
	// must face the ball to settle a rebound/loose ball cleanly. Everyone else uses faceAim, which
	// under the directional move model faces their TRAVEL direction while transiting (so they RUN at
	// forward speed instead of the side/back crawl) and turns to face the ball just before receiving;
	// under the standard model faceAim always faces the ball (speed-neutral, unchanged behaviour).
	if p.ballLoose && a.nearestToBallOnMyside(p) {
		in.Aim = a.aimToward(p, p.ball)
	} else {
		in.Aim = a.faceAim(p, in, p.ball)
	}
	if a.wantTrapReceive(p) {
		in.Trap = true
	}
	return in
}

// avoidKeeperOnlyBox keeps an OUTFIELD player's movement target out of a goal area that only a
// keeper may enter (the "only keeper in box" rule, config.Ruleset.GoalAreaKeeperOnly). Without this
// the AI would path into a box it is immediately walled out of by the sim, leaving its man/marking
// spot unmanned at the edge; clamping the target to the box FRONT face (never toward the goal line)
// keeps the player stable just outside, goal-side of whatever it was covering. A no-op when the rule
// is off or for the keeper, so default play is unchanged. The rule is a global, on-screen match
// setting, so reading it stays within the AI<=human boundary. (Numeric box caps are left to the sim's
// gentle eviction; only the explicit keeper-only rule is anticipated here.)
func (a *AI) avoidKeeperOnlyBox(p perception, target geom.Vec) geom.Vec {
	if !p.rules.GoalAreaKeeperOnly || p.me.Role() == sim.RoleKeeper {
		return target
	}
	f := p.view.Field()
	margin := p.me.Radius() + 4
	for _, side := range []sim.Side{sim.SideLeft, sim.SideRight} {
		box := f.GoalArea(side)
		if !box.Contains(target) {
			continue
		}
		if side == sim.SideLeft {
			target.X = box.Max.X + margin // push out the front (midfield-facing) face, never toward the goal line
		} else {
			target.X = box.Min.X - margin
		}
	}
	return target
}

// nearestToBallOnMyside reports whether this player is the closest of its own side to the ball.
// Such a player is the backup behind the elected presser and must stay turned toward the ball to
// settle a loose ball or rebound, so it faces the ball rather than its travel direction (faceAim).
func (a *AI) nearestToBallOnMyside(p perception) bool {
	myDist := geom.Dist(p.me.Position(), p.ball)
	for _, q := range p.teammates {
		if geom.Dist(q.Position(), p.ball) < myDist {
			return false
		}
	}
	return true
}

// teammatePush returns a steering vector that repels this player away from teammates within
// separationRadius, each contribution a unit direction scaled by how deep inside the radius the
// teammate sits (0 at the edge, 1 on top). Summing over close teammates spreads bunched players
// apart. Read from current positions in the shared view, so it is deterministic and symmetric.
func (a *AI) teammatePush(p perception) geom.Vec {
	push := geom.Vec{}
	for _, q := range p.teammates {
		rel := p.me.Position().Sub(q.Position())
		d := geom.Norm(rel)
		if d > 1e-6 && d < a.tune.separationRadius {
			push = push.Add(rel.Scale((a.tune.separationRadius - d) / a.tune.separationRadius / d))
		}
	}
	return push
}

// supportHoldSpot returns a STABLE receiving spot for a standing supporter: it re-picks a fresh
// receiveSpot only when the held one has gone stale (the ball has moved far from where it was picked)
// or is no longer a good option (its lane from the ball is no longer safe, or it is no longer open) --
// otherwise it HOLDS the spot. A stationary target is what stops passes over-hitting a receiver that
// has drifted off the ball's line during the flight (the dominant over-hit failure). The spot itself
// still comes from receiveSpot's sector search, so players stay fanned out.
func (a *AI) supportHoldSpot(p perception) geom.Vec {
	repick := !a.holdSpotOK ||
		geom.Dist(p.ball, a.holdSpotBall) > a.tune.supportHoldBallMove ||
		laneSafe(p.ball, a.holdSpot, a.passSpeedFor(p, a.holdSpot), p.ballRadius, p.friction, p.opponents, a.tune) < a.tune.passSafetyMin ||
		p.space(a.holdSpot) < a.tune.passReceiverSpace
	if repick {
		a.holdSpot = a.receiveSpot(p, a.tune.supportForwardBias)
		a.holdSpotBall = p.ball
		a.holdSpotOK = true
	}
	return a.holdSpot
}

// receiveSpot moves this player to where it is a real, REACHABLE passing option. Each
// teammate works its own SECTOR -- the direction from the ball to its formation slot -- and
// comes within passing range along that sector, so teammates fan out into distinct lanes
// (no clustering) yet are all close enough for a safe, controllable pass rather than a long
// hoof. Within its sector it samples for the spot with the safest, most open, most goalward
// lane from the ball. Stays onside and out of the keeper's box.
func (a *AI) receiveSpot(p perception, fwdBias float64) geom.Vec {
	slot := idealPosition(p, a.tune).Add(geom.NewVec(p.attackX*fwdBias, 0))
	dir := geom.Unit(slot.Sub(p.ball)) // this player's distinct sector from the ball
	if dir == (geom.Vec{}) {
		dir = geom.NewVec(p.attackX, 0)
	}
	rng := p.view.Field().Width() * a.tune.supportRangeFrac

	best := confineSlot(p, p.ball.Add(dir.Scale(rng)))
	bestVal := -1e18
	for _, frac := range []float64{0.55, 0.8, 1.05} { // how far along the sector (passing range)
		for _, ang := range []float64{-0.3839724354387525, 0, 0.3839724354387525} { // radians (~22deg) spread within the sector
			d := rotate(dir, ang)
			spot := confineSlot(p, p.ball.Add(d.Scale(rng*frac)))
			if kickoffActive(p) {
				spot = clampOwnHalf(p, spot)
			}
			spot = clampZoneRules(p, spot)
			lane := laneSafe(p.ball, spot, a.passSpeedFor(p, spot), p.ballRadius, p.friction, p.opponents, a.tune)
			val := clampFloat(lane, -0.5, 1)*a.tune.recvLaneWeight +
				p.space(spot)*a.tune.recvSpaceWeight +
				p.goalwardness(p.ownGoal, spot)*a.tune.recvAdvanceWeight
			if val > bestVal {
				bestVal, best = val, spot
			}
		}
	}
	return best
}

// markSpot stands goal-side of the most dangerous unattended attacker (the one nearest our
// goal), cutting the line between that attacker and our goal.
func (a *AI) markSpot(p perception) geom.Vec {
	var mark sim.ObservedView
	bestThreat := -1e9
	for _, o := range p.opponents {
		if o.Role() == sim.RoleKeeper {
			continue
		}
		threat := -geom.Dist(o.Position(), p.ownGoal) // nearer our goal = more dangerous
		if threat > bestThreat {
			bestThreat, mark = threat, o
		}
	}
	if mark == nil {
		return idealPosition(p, a.tune)
	}
	toGoal := geom.Unit(p.ownGoal.Sub(mark.Position()))
	return confineSlot(p, mark.Position().Add(toGoal.Scale(p.me.Radius()*3)))
}
