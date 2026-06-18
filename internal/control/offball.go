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
	if a.receivingPass(p) {
		ip = a.receivePoint(p)
	}

	// At a kickoff the defending side must not barge the spot before the ball is in play;
	// hold just outside the centre circle until it moves.
	if kickoffActive(p) && p.view.KickoffSide() != p.me.Side() {
		ip = a.kickoffStandoff(p)
	}

	mv, th := a.steer(p, ip, false)
	in.Move, in.Throttle = mv, th
	in.Aim = a.aimToward(p, p.ball) // face the ball (projected far so the facing is cache-stable)

	// Pre-charge a clearance: when about to reach a loose ball deep in our own third, start
	// holding shoot WHILE closing in (charging needs no ball) so the clearance leaves with
	// power the instant we touch it -- then release on contact. Approaching from our own
	// side, the radial kick naturally goes upfield.
	if a.shouldPrechargeClear(p) {
		if p.iControl {
			in.ShootHeld = false // contact: release the charged clear
			a.kickCooldown = p.view.Tick() + a.tune.kickCooldownTicks
			a.lastOnBall = actClear
		} else {
			in.ShootHeld = true // keep charging as we close in
		}
		return in
	}

	// Trap to take a clean touch or steal -- but NOT in a 50/50 race: trap halves our speed,
	// so if an opponent can reach the ball about as soon as we can, contest it at full pace
	// instead of slowing down and losing it.
	if (a.wantTrapReceive(p) || a.wantTrapSteal(p)) && !a.contested(p) {
		in.Trap = true
	}
	return in
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

	var earliest geom.Vec
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
		if ballSpeedAt(p.ballVel, t, p.friction, p.dt) <= controlSpeed {
			return target // soonest reachable point where the ball is controllable
		}
	}
	if haveEarliest {
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

// shouldPrechargeClear reports whether the presser is about to reach a loose ball deep in
// its own third (a dangerous situation) and should pre-charge a clearance. It is gated on a
// short ETA so the player doesn't wander pre-charged at half speed.
func (a *AI) shouldPrechargeClear(p perception) bool {
	if p.carrierEnemy {
		return false // an opponent controls it: steal, don't swing at it
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
	return eta <= a.tune.prechargeETA
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

	target := idealPosition(p, a.tune)
	supporting := false
	switch {
	case p.teamControls && p.view.Tick() < a.runUntil:
		target = a.receiveSpot(p, a.tune.runForwardBias) // give-and-go run into space
		supporting = true
	case p.teamControls:
		target = a.receiveSpot(p, a.tune.supportForwardBias) // offer a passing option in range
		supporting = true
	case p.carrierEnemy && plan.support == p.me.ID():
		target = a.markSpot(p)
	}

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
	in.Aim = a.aimToward(p, p.ball)
	if a.wantTrapReceive(p) {
		in.Trap = true
	}
	return in
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
		if o.Role() == sim.RoleGoalkeeper {
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
