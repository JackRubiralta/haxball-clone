package control

import (
	"phootball/internal/geom"
	"phootball/internal/sim"
)

// On-ball play. When the AI has the ball it scores the available actions -- shoot, pass,
// dribble, clear, shield -- and executes the best one. Scores share a common scale and
// get a little deterministic noise, so similar situations are resolved differently by
// different players (controlled variety) without ever being dumb.

type onBallKind int

const (
	actDribble onBallKind = iota
	actShoot
	actPass
	actClear
	actShield
)

// LastAction reports the carrier's most recent on-ball decision, for diagnostics/tests.
func (a *AI) LastAction() string {
	switch a.lastOnBall {
	case actShoot:
		return "shoot"
	case actPass:
		return "pass"
	case actClear:
		return "clear"
	case actShield:
		return "shield"
	default:
		return "dribble"
	}
}

// onBall decides and executes the carrier's action for this tick.
func (a *AI) onBall(p perception, plan teamPlan) sim.Intent {
	in := sim.Intent{}

	// If a charged shot/pass is mid-flight, see it through (commitment) instead of re-deciding
	// every tick, as long as the ball is still ours -- but keep the target LIVE: track a moving
	// receiver so a pass leads the runner (not a stale spot), and re-hurry a shot if a defender
	// has since closed the window.
	if a.charging && p.iControl {
		if a.passReceiver != nil {
			// Keep the committed pass aimed at a STABLE point so the lineup can settle and fire
			// (re-leading every tick chased the runner and the aim never converged -> timed out).
			// Only nudge it if the receiver has drifted far from the committed lead.
			live := a.leadPoint(p, a.passReceiver)
			if geom.Dist(live, a.shotTarget) > a.tune.passReceiverSpace {
				a.shotTarget = live
				a.shotDesired = a.passChargeFor(p, a.shotTarget)
			}
		} else if w := a.openDuration(p, p.me.Position()); w < a.tune.shootHurryWindow {
			dist := geom.Dist(p.me.Position(), p.enemyGoal)
			if hur := a.desiredCharge(dist) * clampFloat(w/a.tune.shootHurryWindow, 0.15, 1); hur < a.shotDesired {
				a.shotDesired = hur
			}
		}
		return a.shootAt(p, in, a.shotTarget, a.shotDesired, a.shotAlignRad)
	}
	a.charging = false
	a.passReceiver = nil

	// Just kicked: take a real controlling touch before kicking again, so the AI dribbles
	// between passes/shots instead of frantically poking the ball every few ticks.
	if p.view.Tick() < a.kickCooldown {
		a.lastOnBall = actDribble
		target, _ := a.bestDribble(p)
		return a.dribble(p, in, target)
	}

	// Dribble is the always-available default (score 1.0): with equal top speeds a carrier
	// can keep the ball by driving into space, so we only KICK it away for a genuinely
	// better option -- an open shot, a safe progressive pass, or a clearance from deep under
	// pressure. This keeps possession instead of hoofing every loose touch.
	shootTarget, shootScore := a.bestShot(p)
	passTarget, passReceiver, passScore := a.bestPass(p)
	dribbleTarget, dribbleScore := a.bestDribble(p)
	clearScore := a.clearScore(p)
	shieldScore := a.shieldScore(p, passScore)

	// Decision noise scaled by skill: lower tiers vary (and err) more.
	jitter := a.params.scoreNoise
	shootScore += noise(a.ID, p.view.Tick(), 1^p.seed) * jitter
	passScore += noise(a.ID, p.view.Tick(), 2^p.seed) * jitter
	dribbleScore += noise(a.ID, p.view.Tick(), 3^p.seed) * jitter

	// Hysteresis: a small bonus to repeating last tick's action stops flip-flopping.
	const stick = 0.15
	best, bestScore := actDribble, dribbleScore
	consider := func(k onBallKind, s float64) {
		if k == a.lastOnBall {
			s += stick
		}
		if s > bestScore {
			best, bestScore = k, s
		}
	}
	consider(actShoot, shootScore)
	consider(actPass, passScore)
	consider(actClear, clearScore)
	consider(actShield, shieldScore)
	a.lastOnBall = best

	switch best {
	case actShoot:
		dist := geom.Dist(p.me.Position(), p.enemyGoal)
		desired := a.desiredCharge(dist)
		// Urgency vs power: if a defender is closing the window, hurry the shot (less charge, get
		// it away before it's blocked); but if there's time and space, HIT IT HARD -- a full
		// charge is much harder for the keeper to stop.
		if w := a.openDuration(p, p.me.Position()); w < a.tune.shootHurryWindow {
			desired *= clampFloat(w/a.tune.shootHurryWindow, 0.15, 1)
		} else {
			desired = 1 // plenty of time: shoot as hard as we can
		}
		return a.shootAt(p, in, a.applyAim(p, shootTarget), desired, a.tune.shootAlignRad)
	case actPass:
		// Calibrated power: only as hard as needed to reach the receiver in control (soft for
		// short passes, firmer for long), not a blast.
		dc := a.passChargeFor(p, passTarget)
		// Give-and-go: after laying a forward pass off, break into space for a return.
		if p.goalwardness(p.ball, passTarget) > a.tune.passMinAdvance {
			a.runUntil = p.view.Tick() + a.tune.oneTwoTicks
		}
		a.passReceiver = passReceiver // keep the pass tracking the (moving) receiver
		return a.shootAt(p, in, a.applyAim(p, passTarget), dc, a.tune.shootAlignRad)
	case actClear:
		// Quick clear: low charge + wide tolerance so it boots the ball away fast (loose aim).
		return a.shootAt(p, in, a.applyAim(p, a.clearTarget(p)), a.tune.clearCharge, a.tune.clearAlignRad)
	case actShield:
		return a.shield(p, in)
	default:
		return a.dribble(p, in, dribbleTarget)
	}
}

// bestShot picks the best goal corner to aim at and scores shooting from here.
func (a *AI) bestShot(p perception) (geom.Vec, float64) {
	dist := geom.Dist(p.me.Position(), p.enemyGoal)
	if dist > a.tune.shootRange {
		return p.enemyGoal, -1
	}
	// The shot is radial (the ball leaves along ball-minus-player). If the ball is on the far
	// side from goal (behind us), shooting would mean spinning it all the way round first --
	// not worth committing to; dribble to reposition it goal-side instead.
	if geom.Dot(geom.Unit(p.ball.Sub(p.me.Position())), geom.Unit(p.enemyGoal.Sub(p.me.Position()))) < a.tune.shootBallSide {
		return p.enemyGoal, -1
	}
	corner, clearance := a.bestCorner(p)
	prox := clampFloat(1-dist/a.tune.shootRange, 0, 1)
	// A partly-blocked lane still rates: shots from range force saves and rebounds. Only a
	// fully smothered lane (negative clearance) is discarded.
	open := smoothstep(-p.me.Radius(), 2*p.me.Radius(), clearance)
	// Angle quality: a shot from in front of goal beats one from a tight wing angle.
	square := clampFloat(1-2*absFloat(p.me.Position().Y-p.enemyGoal.Y)/p.view.Field().Height(), 0, 1)
	// Close-range urge: from shooting distance just HIT it -- in a crowded box you score off
	// rebounds, deflections and keeper spills, not by waiting for a pristine lane.
	closeBonus := smoothstep(a.tune.shootRange*0.7, a.tune.tapRange, dist) * 1.5
	// Base below the dribble baseline so a SMOTHERED look (open~0) is not taken, but a clear
	// look at goal gets a flat openness bonus so it decisively out-rates a lateral pass -- the
	// AI takes the good shot instead of declining it, without blasting hopeless ones.
	return corner, 0.9 + 1.8*prox*open + 0.6*square*open + a.tune.shootOpenBonus*open + closeBonus
}

func absFloat(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

// bestCorner returns the goal corner with the clearest lane from the ball, and that
// lane's clearance (distance to the nearest blocker). Aiming for the more open corner
// naturally targets the side away from the keeper.
func (a *AI) bestCorner(p perception) (geom.Vec, float64) {
	// Aim a safe fraction of the way to the open post (not at the post itself) so that even
	// with a small release-angle error the shot still hits the target -- on-target shots
	// that force saves and rebounds, rather than ambitious corners that fly wide to nothing.
	half := p.view.Field().GoalHeight() / 2 * a.tune.shootAimFrac
	top := geom.NewVec(p.enemyGoal.X, p.enemyGoal.Y-half)
	bot := geom.NewVec(p.enemyGoal.X, p.enemyGoal.Y+half)
	ct := laneClearance(p.ball, top, p.opponents, p.ballRadius)
	cb := laneClearance(p.ball, bot, p.opponents, p.ballRadius)
	if ct >= cb {
		return top, ct
	}
	return bot, cb
}

// laneClearance returns the smallest distance from any player to the segment from->to,
// minus that player's radius -- how much room the ball has to travel the lane untouched.
func laneClearance(from, to geom.Vec, players []sim.PlayerView, ballRadius float64) float64 {
	worst := 1e9
	for _, q := range players {
		d := segPointDist(q.Position(), from, to) - q.Radius() - ballRadius
		if d < worst {
			worst = d
		}
	}
	return worst
}

// bestPass evaluates passing to each teammate, leading runners into space. It is selective
// on purpose: the receiver must be open and the lane safe with margin, and a pass should
// progress play (forward) unless we are under pressure and need an outlet. Returns the best
// target, its receiver, and a score on the same scale as dribble (1.0), or a negative score
// if no worthwhile pass exists -- so the carrier keeps the ball rather than spraying it.
func (a *AI) bestPass(p perception) (geom.Vec, sim.PlayerView, float64) {
	pressured := p.pressureOnMe > a.tune.actPressure
	trapped := a.trapped(p)
	best := -1.0
	var bestTarget geom.Vec
	var bestRecv sim.PlayerView

	// consider scores one candidate target on the dribble (1.0) scale. A pass must be safe
	// (lane clear with margin) and to an open man; progressive balls out-rate keeping it,
	// while a square/back recycle only beats losing the ball (used when trapped).
	consider := func(mate sim.PlayerView, target geom.Vec, kindBonus float64) {
		dist := geom.Dist(p.ball, target)
		// Judge interception against the ACTUAL pace this pass would be played at (which is
		// itself paced up to beat the lane), so the safety check and the delivery agree.
		safety := laneSafe(p.ball, target, a.passSpeedFor(p, target), p.ballRadius, p.friction, p.opponents, a.tune)
		if safety < a.tune.passSafetyMin {
			return
		}
		// The receiver must actually be able to COLLECT it and WIN it: it should reach the target
		// around when the ball does, and clearly before any opponent. laneSafe guards the flight
		// path, but over a long pass opponents converge on the DESTINATION during the flight, so
		// we race them to the target too -- this is what stops passes being cut out on a big map.
		ballT := a.passFlightTime(p, target)
		recvT := timeToPoint(mate, target, p.ballRadius)
		if recvT > ballT+a.tune.passReachMargin {
			return // ball would arrive well before the receiver -> it runs to no one
		}
		controlT := ballT // when our man can control it (ball must have arrived, and the mate)
		if recvT > controlT {
			controlT = recvT
		}
		for _, o := range p.opponents {
			if timeToPoint(o, target, p.ballRadius) < controlT+a.tune.passContestMargin {
				return // an opponent reaches the target first/together -> contested, don't gift it
			}
		}
		space := p.space(target)
		if space < a.tune.passReceiverSpace {
			return
		}
		advance := p.goalwardness(p.ball, target)
		forward := advance >= a.tune.passMinAdvance
		if !forward && !(pressured || trapped) {
			return // recycle/back passes are an outlet only when we can't go forward or dribble
		}
		// Prefer an open receiver who will STAY open, with a safe lane; penalise distance a
		// little so a long, riskier ball doesn't out-rate a simple safe one. A forward pass
		// that has cleared every safety gate gets a standing preference over dribbling -- moving
		// the ball by pass is faster and safer than carrying it, so the team actually plays
		// football instead of one player dribbling forever.
		openDur := clampFloat(a.openDuration(p, target), 0, 1.5)
		score := 1.0 + kindBonus + advance*0.009 + space*0.006 + safety*0.5 + openDur*0.4 - dist*a.tune.passDistPenalty
		if forward {
			score += a.tune.passForwardBonus
		} else {
			score = clampFloat(score, 0, 1.12) // a recycle never out-rates real progress
		}
		if score > best {
			best, bestTarget, bestRecv = score, target, mate
		}
	}

	for _, mate := range p.teammates {
		if mate.Role() == sim.RoleGoalkeeper && !trapped {
			continue // only recycle to the keeper when genuinely hemmed in
		}
		// To feet / lead the runner by the ball's real flight time.
		consider(mate, a.leadPoint(p, mate), 0.15)
		// Through ball into the space ahead of the mate toward goal -- only when it actually
		// progresses the ball past where the mate stands now (otherwise it's not a through ball).
		ahead := mate.Position().Add(geom.NewVec(p.attackX*a.tune.throughDist, 0))
		if p.goalwardness(p.ball, ahead) > p.goalwardness(p.ball, mate.Position()) {
			consider(mate, ahead, 0.3)
		}
	}
	if bestRecv == nil {
		return geom.Vec{}, nil, -1
	}
	return bestTarget, bestRecv, best
}

// passSpeedFor returns the launch speed to play a pass at target so the ball ARRIVES at a
// controllable pace rather than being blasted: vArrive + drag*dist (the ball has linear
// drag, so to slow from v0 to vArrive over d, v0 = vArrive + drag*d). A contested lane is
// handled by REJECTING the pass (laneSafe judges this same speed), not by zipping a rocket
// the receiver can't control. Clamped to a sane band.
func (a *AI) passSpeedFor(p perception, target geom.Vec) float64 {
	v0 := a.tune.passArriveSpeed + (-p.friction)*geom.Dist(p.ball, target)
	return clampFloat(v0, a.tune.passSpeedMin, a.tune.passSpeedMax)
}

// passFlightTime estimates how long (seconds) the ball takes to travel from where it is now
// to target at the calibrated pass pace. The ball decelerates with drag from the launch
// speed toward the arrive speed, so the average pace is roughly their mean -- accurate
// enough to LEAD a moving receiver to where it will actually be when the ball gets there.
func (a *AI) passFlightTime(p perception, target geom.Vec) float64 {
	dist := geom.Dist(p.ball, target)
	avg := (a.passSpeedFor(p, target) + a.tune.passArriveSpeed) / 2
	if avg < 1 {
		return 0
	}
	return dist / avg
}

// leadPoint returns where to aim a pass so it meets a moving mate: its current position
// plus its velocity over the ball's FLIGHT TIME (not a fixed gain), so a long pass to a
// runner is led the right amount instead of arriving behind them.
func (a *AI) leadPoint(p perception, mate sim.PlayerView) geom.Vec {
	t := a.passFlightTime(p, mate.Position()) // estimate flight from the mate's spot now
	return mate.Position().Add(mate.Velocity().Scale(t))
}

// timeToPoint estimates how long a player takes to reach a point at top speed (its body
// reaching within a ball's touch of it). Used to check a pass is actually collectable.
func timeToPoint(q sim.PlayerView, point geom.Vec, ballRadius float64) float64 {
	d := geom.Dist(q.Position(), point) - q.Radius() - ballRadius
	if d < 0 {
		return 0
	}
	return d / q.Stats().MaxSpeed
}

// passChargeFor maps the pass launch speed to a shoot charge via the shoot curve, so the
// pass is played with exactly enough power -- no more.
func (a *AI) passChargeFor(p perception, target geom.Vec) float64 {
	front := p.me.Stats().Shoot.Eval(0)
	if front <= 0 {
		return 0.3
	}
	factor := a.passSpeedFor(p, target) / front
	charge := (factor - p.me.Stats().MinShootFactor) / (1 - p.me.Stats().MinShootFactor)
	return clampFloat(charge, 0, 1)
}

// bestDribble chooses a heading that carries the ball goalward into the most open space
// (and away from the immediate presser), and returns the constant baseline score 1.0 --
// the bar every kick action must beat to be worth giving the ball up.
func (a *AI) bestDribble(p perception) (geom.Vec, float64) {
	toGoal := geom.Unit(p.enemyGoal.Sub(p.me.Position()))
	if toGoal == (geom.Vec{}) {
		toGoal = geom.NewVec(p.attackX, 0)
	}
	// Preferred direction: drive straight at goal when free (exploit any head start), peel
	// away from the nearest defender when pressured (you can't beat a marker at equal
	// speed, so make space and retain). w blends the two by how much pressure we are under.
	// In the final third we stop peeling and commit toward goal, so attacks come inside for
	// a real shot instead of dying in the corner.
	w := p.pressureOnMe
	if geom.Dist(p.me.Position(), p.enemyGoal) < a.tune.shootRange*1.5 {
		w *= 0.25
	}
	away := geom.Unit(p.me.Position().Sub(p.nearestOppToMe.Position()))
	prefer := geom.Unit(toGoal.Scale(1 - w).Add(away.Scale(w)))
	if prefer == (geom.Vec{}) {
		prefer = toGoal
	}

	probe := 90.0
	bestDir, bestVal := prefer, -1e9
	for _, ang := range []float64{-1.2217304763960306, -0.7853981633974483, -0.3490658503988659, 0, 0.3490658503988659, 0.7853981633974483, 1.2217304763960306} { // radians (~70,45,20deg fan)
		dir := rotate(prefer, ang)
		pt := p.me.Position().Add(dir.Scale(probe))
		// Stay close to the preferred heading, nudged toward open space.
		val := geom.Dot(dir, prefer)*100 + clampFloat(p.space(pt), 0, 120)*0.5
		// Don't carry the ball into a wall: penalise headings whose probe runs out of the play
		// area (the ball is confined, so driving at a wall just grinds it). The overshoot is how
		// far past the bounds the probe reaches, so a heading along/away from the wall wins.
		val -= geom.Dist(pt, confineSlot(p, pt)) * a.tune.dribbleWallAvoid
		if val > bestVal {
			bestVal, bestDir = val, dir
		}
	}
	return p.me.Position().Add(bestDir.Scale(probe)), 1.0
}

// clearScore rises when the ball is deep in our own third under pressure -- the moment to
// boot it clear rather than risk losing it in front of goal.
func (a *AI) clearScore(p perception) float64 {
	frac := (p.ball.X - p.ownGoal.X) * p.attackX / p.view.Field().Width() // 0 own goal..1 enemy goal
	if frac > a.tune.clearThird {
		return -1
	}
	if p.pressureOnMe < a.tune.actPressure {
		return -1 // calm in our own third: play it out rather than hoof it
	}
	return 0.8 + p.pressureOnMe*1.2
}

// clearTarget returns a safe upfield/wide target for a clearance: toward the far flank,
// never square across our own goal.
func (a *AI) clearTarget(p perception) geom.Vec {
	f := p.view.Field()
	// Aim up the pitch and toward whichever touchline the ball is nearer (push it wide).
	y := f.Min().Y + f.Height()*0.2
	if p.ball.Y > f.CenterSpot().Y {
		y = f.Min().Y + f.Height()*0.8
	}
	x := f.CenterSpot().X + p.attackX*f.Width()*0.2
	return geom.NewVec(x, y)
}

// shieldScore rises under heavy pressure when there is no safe pass and we are not deep
// enough to simply clear: keep the ball by shielding it. It must out-rate the dribble
// baseline (1.0) to ever be chosen, so it is scored on the same 1.0+ scale.
func (a *AI) shieldScore(p perception, passScore float64) float64 {
	if p.pressureOnMe < a.tune.shieldPressure {
		return -1
	}
	if passScore > 1.0 {
		return -1 // a real (progressive) pass is better than shielding
	}
	return 1.0 + p.pressureOnMe // 1.5+ under heavy pressure: a genuine hemmed-in fallback
}

// shield turns the player so its body is between the ball and the nearest opponent, holds
// trap to firm up control, and edges away from the pressure.
func (a *AI) shield(p perception, in sim.Intent) sim.Intent {
	in = a.abortCharge(p, in)
	away := geom.Unit(p.me.Position().Sub(p.nearestOppToMe.Position()))
	if away == (geom.Vec{}) {
		away = geom.NewVec(p.attackX, 0)
	}
	target := p.me.Position().Add(away.Scale(60))
	mv, th := a.steer(p, confineSlot(p, target), false)
	in.Move, in.Throttle = mv, th
	in.Aim = a.aimToward(p, p.me.Position().Add(away)) // face away so the ball stays on the far side
	in.Trap = true
	return in
}

// dribble carries the ball toward target while obeying the ball-control physics. The ball
// is rolled toward the player's FACING (front pull is strongest) and cannot follow a facing
// that snaps around -- it lags more the farther/looser it sits -- so the key lever is how
// fast the FACING turns, not the body. Three rules:
//
//	(1) Smooth, settledness-scaled facing: rotate the aim toward the heading at a rate that
//	    is gentle for a loose/fresh ball and quicker once the ball is tight and possessed.
//	(2) Recover a side/back ball by facing it: if the ball has drifted off the front arc,
//	    aim AT the ball first to scoop it back to the front (then resume facing travel) --
//	    faster than waiting for the weak back-roll to bring it around.
//	(3) Trap through turns / while the ball is loose so the stronger roll-to-front keeps it
//	    glued; only sprint (full pace, no trap) once settled and running into clear space.
func (a *AI) dribble(p perception, in sim.Intent, target geom.Vec) sim.Intent {
	in = a.abortCharge(p, in)

	// Smoothed movement heading toward the target (the body turns gradually too).
	move := geom.Unit(confineSlot(p, target).Sub(p.me.Position()))
	if move == (geom.Vec{}) {
		move = geom.Unit(p.enemyGoal.Sub(p.me.Position()))
	}
	if a.lastDribbleDir != (geom.Vec{}) {
		move = rotateToward(a.lastDribbleDir, move, a.tune.maxTurnRad)
	}
	a.lastDribbleDir = move

	// Face the travel direction, but keep the ball on the front -- recovering a side/back ball
	// by facing it first -- via the shared, rate-limited rule (no facing snap = no fling/jitter).
	in.Aim = a.aimKeepingBall(p, p.me.Position().Add(move))
	recovering := a.recovering

	// Trap is expensive (it halves speed), so use it sparingly: to scoop the ball back when
	// recovering, to keep it glued through a genuinely sharp turn, or to damp a fast ball we
	// are receiving (a clean first touch). A merely "unsettled" ball does NOT trigger trap --
	// that over-used the right click and slowed everything down.
	bigTurn := geom.AngleBetween(p.me.Facing(), move) > a.tune.turnTrapRad
	trap := recovering || bigTurn || a.wantTrapReceive(p)

	throttle := 1.0
	if p.me.Possession() < a.tune.settlePossession {
		throttle = a.tune.settleThrottle // nurse a fresh touch into control
	}
	if recovering {
		throttle *= 0.6 // slow down so the ball can catch up to the front
	}

	in.Move, in.Throttle, in.Trap = move, throttle, trap
	return in
}

// ballSettled rates how firmly the player has the ball (0 loose .. 1 glued) from its
// possession build-up and how flush the ball sits to the surface. A settled ball can be
// turned more sharply without flinging it; a loose one must be coaxed gently.
func (a *AI) ballSettled(p perception) float64 {
	gapFactor := 1 - clampFloat(p.gapToBall/p.me.Stats().PullRange, 0, 1)
	return clampFloat(0.5*p.me.Possession()+0.5*gapFactor, 0, 1)
}

// applyAim adds skill-scaled aim error to a target point, so lower tiers (and a touch of
// every tier) miss the perfect spot -- the controlled-chaos element on shots and passes.
func (a *AI) applyAim(p perception, target geom.Vec) geom.Vec {
	if a.params.aimNoiseRad <= 0 {
		return target
	}
	rel := target.Sub(p.ball)
	ang := gaussian(a.ID, p.view.Tick(), 5^p.seed) * a.params.aimNoiseRad
	return p.ball.Add(rotate(rel, ang))
}

// rotate turns v by angle radians about the origin.
func rotate(v geom.Vec, angle float64) geom.Vec {
	return v.Rotate(angle, geom.Vec{})
}
