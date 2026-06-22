package control

import (
	"phootball/internal/geom"
	"phootball/internal/sim"
)

// The ability layer turns a high-level want ("shoot at T", "go to P") into the concrete
// Move/Aim/ShootHeld/Trap of an Intent, and owns the cross-tick charge controller. The
// shoot mechanic is RADIAL: the ball leaves along (ball - player_centre), so to send it
// toward a target T the player must stand on the far side of the ball from T. These
// helpers encapsulate that geometry so the decision code can think in terms of targets.

// steer produces a Move vector and throttle toward target. The heading is routed around
// nearby bodies (avoid) so the player flows around traffic instead of grinding into it. When
// arrive is set the player eases off near the target so it settles instead of overshooting.
func (a *AI) steer(p perception, target geom.Vec, arrive bool) (geom.Vec, float64) {
	d := target.Sub(p.me.Position())
	dist := geom.Norm(d)
	if dist < a.tune.arriveRadius {
		return geom.Vec{}, 0
	}
	throttle := 1.0
	if arrive && dist < a.tune.slowRadius {
		throttle = clampFloat(dist/a.tune.slowRadius, 0.2, 1)
	}
	return a.avoid(p, d), throttle
}

// steerReceive steers a receiver to run ALONG the incoming ball's line of travel -- moving WITH
// the ball so the RELATIVE impact (approachSpeed = ballVel-playerVel along the contact normal) is
// low and the ball sticks -- instead of running across or head-on into it (the observed bug: a
// scrub showed receivers reaching the ball mis-aligned, alignment ~0.5 or even negative, which
// spikes the relative impact and either bounces the ball off or lets it sail past). The direction
// blends a sideways pull ONTO the ball's line (scaled by how far off it is, capped by
// receiveOntoMax so a with-the-ball forward component always remains) with running along the
// ball's direction. The throttle stays full for a fast ball (a race -- run flat out in its
// direction to minimise the relative speed) but eases to the ball's pace for a ball slower than
// the receiver, so it doesn't OUTRUN a gentle ball. Used ONLY for an uncontested incoming pass
// (receivingPass); a genuine 50/50 still sprints flat-out at the intercept via steer. Reuses avoid.
func (a *AI) steerReceive(p perception, mp geom.Vec) (geom.Vec, float64) {
	me := p.me.Position()
	toMp := mp.Sub(me)
	dist := geom.Norm(toMp)
	if dist < a.tune.arriveRadius {
		return geom.Vec{}, 0
	}
	ballDir := geom.Unit(p.ballVel)
	maxSpeed := p.me.Tuning().MaxSpeed
	if ballDir == (geom.Vec{}) || maxSpeed <= 0 {
		return a.avoid(p, toMp), 1 // no ball direction to align to: fall back to the meeting point
	}
	// Perpendicular offset from the ball's line (points from the line to the receiver).
	rel := me.Sub(p.ball)
	perp := rel.Sub(ballDir.Scale(geom.Dot(rel, ballDir)))
	offLine := geom.Norm(perp)
	onto := geom.Vec{}
	if offLine > 1e-6 {
		onto = perp.Scale(-1 / offLine) // unit vector back toward the line
	}
	w := clampFloat(offLine/a.tune.receiveSlowRadius, 0, a.tune.receiveOntoMax)
	dir := geom.Unit(ballDir.Scale(1 - w).Add(onto.Scale(w)))
	if dir == (geom.Vec{}) {
		dir = geom.Unit(toMp)
	}
	// Don't outrun a ball slower than us: once roughly on-line, pace to its speed so it catches a
	// receiver moving with it; sprint full when off-line or when the ball is faster than we are.
	th := 1.0
	if ballSpd := geom.Norm(p.ballVel); ballSpd < maxSpeed {
		th = lerp(clampFloat(ballSpd/maxSpeed, a.tune.receiveThrottleFloor, 1), 1, w)
	}
	return a.avoid(p, dir), th
}

// launchAligned reports whether firing NOW would send the ball at target. The sim adds the
// kick impulse to the ball's CURRENT velocity from the ball's own position, so the true launch
// is (ballVel + shotImpulse) and it must point from the BALL toward the target (target - ball),
// NOT (target - me). The impulse is predicted from the SAME function the sim fires with --
// sim.ShootLaunchVelocity -- so the prediction includes the aim-assist blend AND the off-front
// power falloff exactly: a ball sitting off the front cone fires weaker (so ballVel pulls the
// shot more), and accounting for that here stops the AI from releasing a shot it thinks is lined
// up but the reduced-power launch sends wide. Accounting for the ball's existing motion AND
// measuring from the ball (not the player centre) is what makes shots/passes land on target
// instead of releasing early and slightly off angle.
func (a *AI) launchAligned(p perception, target geom.Vec, charge, tolRad float64) bool {
	d := p.ball.Sub(p.me.Position())
	dist := geom.Norm(d)
	if dist < 1e-9 {
		return false
	}
	dir := d.Scale(1 / dist)
	launch := p.ballVel.Add(p.me.Tuning().ShootLaunchVelocity(dir, p.me.Facing(), charge))
	return geom.AngleBetween(launch, target.Sub(p.ball)) < tolRad
}

// desiredCharge picks how hard to hit a shot by distance to goal: firmer the farther out,
// but never a limp tap -- a higher floor keeps even close shots hard (harder to save).
func (a *AI) desiredCharge(distToGoal float64) float64 {
	return clampFloat(smoothstep(a.tune.tapRange, a.tune.fullRange, distToGoal), a.tune.minShootCharge, 1)
}

// passAimWant returns where the carrier should FACE, and the charge it should wind to, so that a PASS
// leaves the ball travelling straight at a.shotTarget at the calibrated arrive speed -- accounting for
// the ball's CURRENT velocity. The sim ADDS the kick impulse to the ball's existing velocity (and aims
// it ~97% along the facing), so facing the target directly sends a moving ball off by its sideways
// velocity ("passes a bit off") and, off a fast ball, forces the charge to build huge to line up (the
// "way too much power" rocket). Inverting the launch physics: to land velocity `s*u` at the target, the
// impulse must supply L = s*u - ballVel; so face unit(L) and charge to |L|. Then launch = ballVel + L =
// s*u -> on target, soft, at low charge. Uses only observable state (ball pos/vel, own pos/tuning) and
// reads Shoot.Front/MinShootFactor live, so it auto-calibrates if the physics is retuned. The
// launchAligned release gate (vs the real target) still fine-tunes the residual aim-assist/radial term.
func (a *AI) passAimWant(p perception) (aim geom.Vec, charge float64, ok bool) {
	u := geom.Unit(a.shotTarget.Sub(p.ball))
	if u == (geom.Vec{}) {
		return geom.Vec{}, 0, false // degenerate (ball on the target)
	}
	s := a.passSpeedFor(p, a.shotTarget)
	l := u.Scale(s).Sub(p.ballVel) // the impulse vector the kick must supply
	dir := geom.Unit(l)
	if dir == (geom.Vec{}) {
		return geom.Vec{}, 0, false
	}
	// Only compensate when unit(L) sits inside the fire cone of the ball (within passAimConeRad of the
	// radial player->ball), i.e. the carrier can fire the compensated kick THIS tick. For a normal
	// forward pass the radial already points down the lane so this holds and the ball launches exactly
	// on target. For an awkward across/back pass unit(L) swings off the cone; compensating there would
	// either make the carrier reposition (a long hold) or, if forced, fire partly-compensated and OFF
	// target -- both measured WORSE than baseline. So bail (ok=false) and let shootAt fall back to the
	// plain face-the-target path, which reaches the man ~90% on those.
	if radial := geom.Unit(p.ball.Sub(p.me.Position())); radial == (geom.Vec{}) || geom.AngleBetween(dir, radial) > a.tune.passAimConeRad {
		return geom.Vec{}, 0, false
	}
	front := p.me.Tuning().Shoot.Front
	msf := p.me.Tuning().MinShootFactor
	if front <= 0 || msf >= 1 {
		return geom.Vec{}, 0, false
	}
	aim = p.me.Position().Add(dir.Scale(aimProjectDist))        // FACE unit(L): launch = ballVel + |L|*unit(L) = s*u
	charge = clampFloat((geom.Norm(l)/front-msf)/(1-msf), 0, 1) // power^-1(|L|): launch speed stays s (capture-safe)
	return aim, charge, true
}

// shootAt commits to (and continues) a charged shot at target. It faces the target and
// drives at it so the control force keeps the ball in front and on the shooting line,
// holds the shoot button to build charge, and releases (firing) once lined up and charged
// enough. Commitment is stored on the AI so a half-charged shot is seen through rather
// than flip-flopping with dribble each tick.
func (a *AI) shootAt(p perception, in sim.Intent, target geom.Vec, desired, baseTol float64) sim.Intent {
	if !a.charging {
		a.charging = true
		a.shotTarget = target
		a.shotDesired = desired
		a.shotAlignRad = baseTol
		a.chargeStart = p.view.Tick()
		a.chargedAt = 0
	}

	// Face smoothly while keeping the ball on the front (scooping a strayed ball home first). For a
	// PASS, face the VELOCITY-COMPENSATED direction (passAimWant) and commit the matching low charge, so
	// the moving ball launches straight at the target at the soft calibrated speed instead of off-by-its-
	// -sideways-velocity (and without the charge escalating into a rocket). For a shot/clear, face the
	// target directly as before.
	aimWant := a.shotTarget
	if a.passReceiver != nil {
		if w, ch, ok := a.passAimWant(p); ok {
			aimWant = w           // velocity-compensated facing (recomputed each tick)
			a.shotDesired = ch    // matching low charge (no rocket)
		}
		// else: off the fire cone -- fall back to facing the target with the committed charge (baseline)
	}
	in.Aim = a.aimKeepingBall(p, aimWant)

	// Capability boundary: a human cannot trap and charge a kick in the same tick (the three mouse
	// buttons are mutually exclusive). SEQUENCE the two instead of combining them: if the ball has
	// drifted off the front cone (recovering), TRAP this tick to scoop it back to the front, do NOT
	// hold the charge, and KEEP the commitment so charging resumes the instant the ball is settled.
	// The block below then charges, lines up and releases WITHOUT ever trapping -- so the AI never
	// requests trap-while-charging and the exclusivity clamp stays a no-op (the decision is already
	// exclusive). This is the human motion of right-clicking to settle a ball that has got away,
	// then winding up the kick once it is back under control -- trap first, charge second.
	if a.recovering { // set by aimKeepingBall above (hysteretic)
		mv, th := a.steer(p, a.shotTarget, false)
		in.Move, in.Throttle, in.Trap = mv, th*a.tune.recoverThrottle, true
		switch {
		case !a.recoverTrap && sim.NormShootCharge(p.myCharge) > 0:
			// Just entered recovery with a charge already wound up: abandon it WITHOUT firing.
			// A bare release would fire the half-charged shot, so cancel it explicitly (cancel is
			// only honoured while shoot reads held). The clamp will also assert CancelCharge from
			// the trap above; setting it here makes the intent self-consistent. Next recovery tick
			// takes the release branch below, which clears the sim's cancel latch.
			in.ShootHeld = true
			in.CancelCharge = true
		default:
			// No charge to abandon (or already cancelled): release the shoot button so the charge
			// stays at zero and the cancel latch clears. With charge 0 and the latch handling, this
			// never fires a stray kick.
			in.ShootHeld = false
		}
		a.recoverTrap = true
		return in
	}
	a.recoverTrap = false

	cur := sim.NormShootCharge(p.myCharge)
	charged := cur+a.params.chargeSlack >= a.shotDesired
	if charged && a.chargedAt == 0 {
		a.chargedAt = p.view.Tick() // start the aim-lineup clock only once we're charged
	}
	// Release ONLY when firing now would actually send the ball at the target -- predicting the
	// true launch velocity (ball motion + radial kick) from the ball's position. Once charged we
	// hold a TIGHT tolerance to line up accurately, RELAXING it only up to shootAlignMaxRad if the
	// lineup drags on -- never beyond, so we never fire a wild, off-target shot to nowhere.
	relax := 0.0
	if a.chargedAt != 0 {
		relax = clampFloat(float64(p.view.Tick()-a.chargedAt)/float64(a.tune.aimRelaxTicks), 0, 1)
	}
	// Relaxing only ever LOOSENS the tolerance: never let it drop below the committed baseTol.
	// A clear commits an already-wide tolerance (> shootAlignMaxRad); without this it would
	// TIGHTEN as the lineup dragged on, so a clear that wasn't lined up instantly could never
	// fire -- it would time out and the keeper would be stranded dribbling instead of booting it.
	relaxTo := a.tune.shootAlignMaxRad
	if a.shotAlignRad > relaxTo {
		relaxTo = a.shotAlignRad
	}
	tol := lerp(a.shotAlignRad, relaxTo, relax)
	aligned := p.iControl && a.launchAligned(p, a.shotTarget, cur, tol)
	overtime := p.view.Tick()-a.chargeStart > a.tune.maxChargeTicks
	switch {
	case charged && aligned:
		in.ShootHeld = false // release -- genuinely lined up at the target
		a.charging = false
		a.kickCooldown = p.view.Tick() + a.tune.kickCooldownTicks
	case overtime:
		// Couldn't line up the ball in a realistic time (it's stuck on our side). Don't fire a
		// shot to nothing: CANCEL the charge (no kick) and go dribble it round to reposition.
		in.ShootHeld = true
		in.CancelCharge = true
		a.charging = false
		a.lastOnBall = actDribble
	default:
		in.ShootHeld = true // keep charging and lining up
	}

	// Drive at the target to keep the ball in front and on the shooting line. (Recovery -- when
	// the ball is off the front cone -- is handled above by trapping WITHOUT charging, so here the
	// ball is settled on the front and we never trap while holding the charge.)
	mv, th := a.steer(p, a.shotTarget, false)
	in.Move, in.Throttle = mv, th
	return in
}

// abortCharge ends an in-progress shot. If the ball is still at the player's feet it lets
// the button go (firing a shot roughly goalward rather than freezing mid-charge); if the
// ball is gone it simply drops the charge. Used when switching away from shooting.
func (a *AI) abortCharge(p perception, in sim.Intent) sim.Intent {
	a.charging = false
	a.passReceiver = nil
	in.ShootHeld = false
	return in
}

// wantTrapReceive reports whether a ball is arriving toward the player and is close enough
// that setting trap now gives a clean, damped first touch (a much bigger capture window)
// instead of the ball bouncing off. It fires for any genuinely incoming pass within the
// setup range, not just the hardest balls -- facing the ball plus trap is how the AI
// receives cleanly.
func (a *AI) wantTrapReceive(p perception) bool {
	approach := p.ball.Sub(p.me.Position())
	closing := -geom.Dot(p.ballVel, geom.Unit(approach)) // speed of the ball toward me
	captureFront := p.me.Tuning().CaptureSpeed.Front
	return closing > captureFront*a.tune.trapReceiveFactor && p.gapToBall < a.tune.trapReceiveRange
}

// wantTrapSteal reports whether the player is close enough to an enemy-held ball that a
// trap (stronger, longer centre-pull) could wrest it away. The trap aura is a limited,
// recharging resource, so a steal -- discretionary and often unsuccessful -- only fires when
// the bar has enough energy left (trapStealMinEnergy); otherwise the bar is saved for the
// high-value use of receiving an incoming pass cleanly.
func (a *AI) wantTrapSteal(p perception) bool {
	return p.carrierEnemy && p.gapToBall < p.me.Tuning().PullRange+a.tune.stealRange &&
		p.myTrap >= a.tune.trapStealMinEnergy
}
