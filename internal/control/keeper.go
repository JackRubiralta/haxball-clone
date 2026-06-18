package control

import (
	"phootball/internal/geom"
	"phootball/internal/sim"
)

// Goalkeeper. The keeper sits on the line between the ball and its own goal centre,
// coming out to cut the angle as the ball nears; it sweeps loose balls it is favourite to
// reach, sets to trap-save fast shots, and clears with a charged kick once it has the ball.
// In a team with no outfielders (e.g. 1-a-side) it falls back to playing like a field
// player so the side is not purely passive.

func (a *AI) keeper(p perception, plan teamPlan) sim.Intent {
	if len(outfielders(p.view.Squad(p.me))) == 0 {
		return a.outfieldFallback(p, plan)
	}
	if p.iControl {
		return a.keeperDistribute(p)
	}

	// Save: the ball is driven hard toward our goal -- set to trap and meet it on the line.
	toGoal := geom.Unit(p.ownGoal.Sub(p.ball))
	closing := geom.Dot(p.ballVel, toGoal)
	if closing > a.tune.keeperSaveSpeed {
		return a.keeperSave(p)
	}

	// Sweep: a loose ball near our goal we are clear favourite to reach.
	if a.keeperShouldSweep(p, plan) {
		in := sim.Intent{}
		reach := p.me.Radius() + p.ballRadius
		ip := interceptPoint(p.me.Position(), p.me.Stats().MaxSpeed, p.me.Stats().TurnRate, p.me.Heading(), reach, p.ball, p.ballVel, p.friction, p.dt, a.tune)
		mv, th := a.steer(p, ip, false)
		in.Move, in.Throttle, in.Aim = mv, th, a.aimToward(p, p.ball)
		if a.wantTrapReceive(p) {
			in.Trap = true
		}
		return in
	}

	// Otherwise hold the angle on the ball-goal line.
	guard := a.keeperGuardSpot(p)
	in := sim.Intent{}
	mv, th := a.steer(p, guard, true)
	in.Move, in.Throttle, in.Aim = mv, th, a.aimToward(p, p.ball)
	return in
}

// keeperGuardSpot is a point on the ball-to-own-goal line, advanced off the line to cut
// the shooting angle as the ball gets closer, and held inside the goal area.
func (a *AI) keeperGuardSpot(p perception) geom.Vec {
	f := p.view.Field()
	dGoal := geom.Dist(p.ball, p.ownGoal)
	near := 1 - clampFloat(dGoal/(f.Width()*0.5), 0, 1) // 1 when ball is close
	standoff := lerp(a.tune.keeperDepthMin, a.tune.keeperDepthMax, near)
	spot := p.ownGoal.Add(geom.Unit(p.ball.Sub(p.ownGoal)).Scale(standoff))

	// NOTE: no positioning mis-read here. While merely HOLDING the angle (no shot driven at us)
	// the keeper should sit cleanly on the ball-to-goal line -- adding a skill-scaled lateral
	// drift here made the keeper slide around its line seemingly at random as the ball moved.
	// The beatable mis-read belongs on an actual save (keeperSave), where a well-placed shot
	// should be able to find the corner; a resting keeper just holds the true angle.

	// Keep within the goal area depth and a touch inside each post, and -- crucially -- never
	// let the keeper hug its goal line / back into the net: hold it at least keeperDepthMin off
	// the line, out to the box front.
	mouthHalf := f.GoalHeight()/2 - p.me.Radius()
	spot.Y = clampFloat(spot.Y, f.CenterSpot().Y-mouthHalf, f.CenterSpot().Y+mouthHalf)
	spot.X = a.clampKeeperDepth(p, spot.X)
	return spot
}

// keeperSave positions the keeper at the ball's predicted goal-line crossing and sets trap
// so the incoming shot is absorbed rather than parried back into play.
func (a *AI) keeperSave(p perception) sim.Intent {
	f := p.view.Field()
	crossY := p.ball.Y
	if dx := p.ownGoal.X - p.ball.X; (dx > 0) == (p.ballVel.X > 0) && p.ballVel.X != 0 {
		t := dx / p.ballVel.X
		crossY = p.ball.Y + p.ballVel.Y*t
	}
	// A keeper reads a central shot well but mis-judges the corners: add a skill-scaled
	// mis-read that grows with ball speed AND with how far the shot crosses from goal centre.
	// So a shot straight at the keeper is saved, while a firm, well-placed corner can beat it
	// (it isn't a wall, but it isn't a sieve up the middle either).
	mouthHalf := f.GoalHeight()/2 - p.me.Radius()
	// Mis-read grows toward the corners (central shots are read best) but keeps a floor, so
	// even a central shot is saved most -- not all -- of the time.
	edgeFrac := clampFloat(absFloat(crossY-f.CenterSpot().Y)/mouthHalf, 0.35, 1)
	crossY += a.keeperMisread(p) * a.params.keeperError * clampFloat(geom.Norm(p.ballVel)/300, 0.6, 1.6) * edgeFrac
	crossY = clampFloat(crossY, f.CenterSpot().Y-mouthHalf, f.CenterSpot().Y+mouthHalf)
	guardX := a.clampKeeperDepth(p, p.ownGoal.X+p.attackX*a.tune.keeperDepthMin)

	in := sim.Intent{Trap: true, Aim: a.aimToward(p, p.ball)}
	mv, th := a.steer(p, geom.NewVec(guardX, crossY), false)
	in.Move, in.Throttle = mv, th
	return in
}

// clampKeeperDepth keeps an X coordinate between keeperDepthMin off the keeper's own goal
// line and the goal-area front, on whichever side the team defends -- so the keeper never
// hugs the line or backs into the net, but also never leaves its area.
func (a *AI) clampKeeperDepth(p perception, x float64) float64 {
	box := p.view.Field().GoalArea(p.me.Side())
	depth := box.Max.X - box.Min.X
	lo := p.ownGoal.X + p.attackX*a.tune.keeperDepthMin
	hi := p.ownGoal.X + p.attackX*depth
	if lo > hi {
		lo, hi = hi, lo
	}
	return clampFloat(x, lo, hi)
}

// keeperShouldSweep reports whether the ball is loose near our goal and the keeper can
// reach it before any opponent -- the moment to rush off the line.
func (a *AI) keeperShouldSweep(p perception, plan teamPlan) bool {
	if p.carrierEnemy {
		return false // don't dive at a controlled enemy; hold the angle
	}
	box := p.view.Field().GoalArea(p.me.Side())
	depth := box.Max.X - box.Min.X
	// Only sweep loose balls fairly close to goal -- never charge far upfield and leave the
	// net empty (which it can't recover from with a turn rate).
	if geom.Dist(p.ball, p.ownGoal) > depth*a.tune.keeperSweepBox+p.view.Field().GoalHeight() {
		return false
	}
	reach := p.me.Radius() + p.ballRadius
	mine := interceptTime(p.me.Position(), p.me.Stats().MaxSpeed, p.me.Stats().TurnRate, p.me.Heading(), reach, p.ball, p.ballVel, p.friction, p.dt, a.tune)
	for _, o := range p.opponents {
		ot := interceptTime(o.Position(), o.Stats().MaxSpeed, o.Stats().TurnRate, o.Heading(), o.Radius()+p.ballRadius, p.ball, p.ballVel, p.friction, p.dt, a.tune)
		// Require the keeper to be a CLEAR favourite (beat opponents by a margin) before
		// committing -- a marginal sweep that loses the race is a gift goal.
		if ot <= mine+a.tune.keeperSweepMargin {
			return false
		}
	}
	return true
}

// keeperDistribute clears or passes once the keeper has the ball: a safe pass if one
// exists, otherwise a strong charged clearance up a flank.
func (a *AI) keeperDistribute(p perception) sim.Intent {
	in := sim.Intent{}
	// Prefer a real, progressive pass.
	if target, receiver, score := a.bestPass(p); receiver != nil && score > 0.6 {
		dc := a.passChargeFor(p, target) // calibrated to reach the receiver in control
		a.passReceiver = receiver
		a.lastOnBall = actPass
		return a.shootAt(p, in, a.applyAim(p, target), dc, a.tune.shootAlignRad)
	}
	// No progressive pass found, but a keeper should PLAY THE BALL OUT, not hoof it: look for any
	// safe, uncontested outlet to an open teammate (forwardness not required -- the keeper is the
	// deepest player). Only clear when there is genuinely no safe option.
	if target, receiver := a.keeperOutlet(p); receiver != nil {
		dc := a.passChargeFor(p, target)
		a.passReceiver = receiver
		a.lastOnBall = actPass
		return a.shootAt(p, in, a.applyAim(p, target), dc, a.tune.shootAlignRad)
	}
	a.lastOnBall = actClear
	// No safe pass and a closing attacker bearing down: BOOT it clear instantly with a push (no
	// time to charge), as long as the radial sends it upfield/wide -- the keeper getting rid of it.
	if p.pressureOnMe > a.tune.pushPressure && a.pushClears(p) {
		return a.pushIntent(p)
	}
	// Otherwise clear it quickly with a low-charge, loose-aim kick rather than dwelling on the ball.
	return a.shootAt(p, in, a.applyAim(p, a.clearTarget(p)), a.tune.clearCharge, a.tune.clearAlignRad)
}

// keeperOutlet finds the safest open outlet pass for the keeper: an open teammate (not the
// keeper) with a clear, uncontested lane the receiver wins ahead of any opponent. Forwardness
// is NOT required -- a square ball to an open full-back to start play beats a blind clearance.
// Returns the target and receiver, or (zero, nil) if no safe outlet exists.
func (a *AI) keeperOutlet(p perception) (geom.Vec, sim.PlayerView) {
	var bestRecv sim.PlayerView
	var bestTarget geom.Vec
	best := -1.0
	for _, mate := range p.teammates {
		if mate.Role() == sim.RoleGoalkeeper {
			continue
		}
		target := a.leadPoint(p, mate)
		if laneSafe(p.ball, target, a.passSpeedFor(p, target), p.ballRadius, p.friction, p.opponents, a.tune) < a.tune.passSafetyMin {
			continue // lane could be cut out
		}
		space := p.space(target)
		if space < a.tune.passReceiverSpace {
			continue // receiver is marked
		}
		// The receiver must win the ball at the target ahead of every opponent.
		recvT := timeToPoint(mate, target, p.ballRadius)
		contested := false
		for _, o := range p.opponents {
			if timeToPoint(o, target, p.ballRadius) < recvT+a.tune.passContestMargin {
				contested = true
				break
			}
		}
		if contested {
			continue
		}
		// Favour the most open, nearest safe outlet.
		if score := space - geom.Dist(p.ball, target)*0.1; score > best {
			best, bestTarget, bestRecv = score, target, mate
		}
	}
	return bestTarget, bestRecv
}

// keeperMisread returns a deterministic, roughly-normal positioning error in [-~1,1] keyed
// to the keeper and the ball's coarse position and heading. Keying on the situation (rather
// than the tick) means each distinct attack draws its own error -- so no recurring shot is
// always saved or always conceded -- while staying steady through a single shot's flight so
// the keeper doesn't jitter.
func (a *AI) keeperMisread(p perception) float64 {
	qx := int64(p.ball.X / 30)
	qy := int64(p.ball.Y / 30)
	vx := int64(p.ballVel.X / 50)
	vy := int64(p.ballVel.Y / 50)
	h := hash64(uint64(qx)*0x9e3779b1 ^ uint64(qy)*0x85ebca77 ^ uint64(vx)*0xc2b2ae3d ^ uint64(vy)*0x27d4eb2f ^ uint64(int64(a.ID))*0x165667b1 ^ p.seed)
	situational := float64(h>>11)/float64(uint64(1)<<53)*2 - 1
	// Blend in a slow time drift so two near-identical attacks at different moments don't get
	// the exact same (always-saved / always-conceded) error -- breaks deterministic save-locks.
	drift := gaussian(a.ID, p.view.Tick()/30, 19^p.seed)
	return situational*0.7 + drift*0.3
}

// outfieldFallback lets a keeper-only side still attack: it presses, plays on the ball, or
// holds shape exactly like an outfielder.
func (a *AI) outfieldFallback(p perception, plan teamPlan) sim.Intent {
	switch {
	case p.iControl:
		return a.onBall(p, plan)
	case plan.presser == p.me.ID():
		return a.press(p, plan)
	default:
		return a.offBall(p, plan)
	}
}
