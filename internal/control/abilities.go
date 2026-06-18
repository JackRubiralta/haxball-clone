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

// aimProjectDist is how far an Aim point is projected from the player. The simulation faces
// the player toward the Aim POINT, so projecting it far (well beyond the pitch) makes the
// facing essentially a direction that barely shifts as the player shuffles around. This is
// what stops the facing from JITTERING when an intent is reused across the reaction-delay
// window: a near aim point would swing wildly as the player moved past it; a far one does not.
const aimProjectDist = 1000

// aimToward returns an Aim point that faces the player toward target from its current
// position, projected far so it stays stable under reaction-delay caching.
func (a *AI) aimToward(p perception, target geom.Vec) geom.Vec {
	dir := geom.Unit(target.Sub(p.me.Position()))
	if dir == (geom.Vec{}) {
		dir = p.me.Facing()
	}
	return p.me.Position().Add(dir.Scale(aimProjectDist))
}

// launchAligned reports whether firing NOW would send the ball at target. The sim adds the
// kick impulse along the radial (ball - player_centre) to the ball's CURRENT velocity, both
// from the ball's own position -- so the true launch direction is (ballVel + radial*power),
// and it must point from the BALL toward the target (target - ball), NOT (target - me).
// Accounting for the ball's existing motion AND measuring from the ball (not the player
// centre) is what makes passes land on target instead of releasing early and slightly off
// angle. power is estimated from the current charge so the prediction matches what will fire.
func (a *AI) launchAligned(p perception, target geom.Vec, charge, tolRad float64) bool {
	d := p.ball.Sub(p.me.Position())
	dist := geom.Norm(d)
	if dist < 1e-9 {
		return false
	}
	dir := d.Scale(1 / dist)
	factor := p.me.Stats().MinShootFactor + (1-p.me.Stats().MinShootFactor)*charge
	power := p.me.Stats().Shoot.Eval(0) * factor // front power (we aim the ball at the front)
	// Predict the TRUE launch direction: the radial nudged toward our facing by the aim
	// assist (same rule the sim fires with), so the lineup check matches the real shot and
	// the assist's forgiveness is exploited rather than fought.
	launchDir := p.me.Stats().ShootDirection(dir, p.me.Facing())
	launch := p.ballVel.Add(launchDir.Scale(power))
	return geom.AngleBetween(launch, target.Sub(p.ball)) < tolRad
}

// desiredCharge picks how hard to hit a shot by distance to goal: firmer the farther out,
// but never a limp tap -- a higher floor keeps even close shots hard (harder to save).
func (a *AI) desiredCharge(distToGoal float64) float64 {
	return clampFloat(smoothstep(a.tune.tapRange, a.tune.fullRange, distToGoal), a.tune.minShootCharge, 1)
}

// ballOffArc reports whether the ball is currently outside the player's front control arc.
func (a *AI) ballOffArc(p perception) bool {
	toBall := geom.Unit(p.ball.Sub(p.me.Position()))
	return toBall != (geom.Vec{}) &&
		geom.AngleBetween(p.me.Facing(), toBall) > p.me.Stats().PossessionArcRadians
}

// updateRecovering applies HYSTERESIS to the "scoop the ball back to the front" state: the
// player starts recovering once the ball drifts past the front arc, and keeps recovering
// until the ball is well back inside the arc (half-arc). Without this band the facing
// toggles between the ball and the target every time the ball grazes the arc edge -- the
// turning jitter. It returns whether the player is recovering.
func (a *AI) updateRecovering(p perception) bool {
	toBall := geom.Unit(p.ball.Sub(p.me.Position()))
	if toBall == (geom.Vec{}) {
		a.recovering = false
		return false
	}
	ang := geom.AngleBetween(p.me.Facing(), toBall)
	arc := p.me.Stats().PossessionArcRadians
	if ang > arc {
		a.recovering = true
	} else if ang < arc*0.5 {
		a.recovering = false
	}
	return a.recovering
}

// aimKeepingBall returns the Aim point for a player CONTROLLING the ball that wants to end up
// facing `want` (a travel heading, a shot target, a pass target...). It keeps the ball on
// the front -- where the pull is strongest -- by rotating the facing SMOOTHLY rather than
// snapping it (a snap flings the ball and looks like jitter). If the ball has drifted off the
// front arc it first turns to face the BALL, scooping it back to the front (with hysteresis,
// so it doesn't flip-flop at the arc edge), and only then turns on toward `want`. The turn
// rate scales with how settled the ball is, since a loose ball lags a turning facing. This is
// the single shared rule for dribbling, shooting, passing and clearing.
func (a *AI) aimKeepingBall(p perception, want geom.Vec) geom.Vec {
	toBall := geom.Unit(p.ball.Sub(p.me.Position()))
	recovering := a.updateRecovering(p)
	desiredFace := geom.Unit(want.Sub(p.me.Position()))
	if recovering {
		desiredFace = toBall // recover: face the ball first
	}
	if desiredFace == (geom.Vec{}) {
		desiredFace = p.me.Facing()
	}

	turn := lerp(a.tune.minTurnRad, a.tune.maxTurnRad, a.ballSettled(p))
	// Don't out-turn the ball: when turning the facing AWAY from the ball (toward the target),
	// slow down as the ball drifts toward the edge of the front arc, so it stays glued and we
	// never leave it behind -- leaving it behind is what restarts recovery and jitters the
	// facing back and forth. (When recovering we are turning TOWARD the ball, so no cap.)
	if !recovering && toBall != (geom.Vec{}) {
		ballAng := geom.AngleBetween(p.me.Facing(), toBall)
		arc := p.me.Stats().PossessionArcRadians
		turn *= clampFloat(1-ballAng/arc, 0.3, 1)
	}
	newFace := rotateToward(p.me.Facing(), desiredFace, turn)
	return p.me.Position().Add(newFace.Scale(aimProjectDist)) // project far: cache-stable facing
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

	// Face the target smoothly while keeping the ball on the front. If the ball is at our
	// back this actively scoops it round to the front first (instead of holding a charge and
	// waiting for it), then lines up the shot -- the same recover-then-turn rule as dribbling.
	in.Aim = a.aimKeepingBall(p, a.shotTarget)

	// Capability boundary: a human cannot trap and charge a kick in the same tick (the three mouse
	// buttons are mutually exclusive). SEQUENCE the two instead of combining them: if the ball has
	// drifted off the front arc (recovering), TRAP this tick to scoop it back to the front, do NOT
	// hold the charge, and KEEP the commitment so charging resumes the instant the ball is settled.
	// The block below then charges, lines up and releases WITHOUT ever trapping -- so the AI never
	// requests trap-while-charging and the exclusivity clamp stays a no-op (the decision is already
	// exclusive). This is the human motion of right-clicking to settle a ball that has got away,
	// then winding up the kick once it is back under control -- trap first, charge second.
	if a.recovering { // set by aimKeepingBall above (hysteretic)
		mv, th := a.steer(p, a.shotTarget, false)
		in.Move, in.Throttle, in.Trap = mv, th*0.6, true
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
	// the ball is off the front arc -- is handled above by trapping WITHOUT charging, so here the
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
	captureFront := p.me.Stats().CaptureSpeed.Front
	return closing > captureFront*a.tune.trapReceiveFactor && p.gapToBall < a.tune.trapReceiveRange
}

// wantTrapSteal reports whether the player is close enough to an enemy-held ball that a
// trap (stronger, longer centre-pull) could wrest it away.
func (a *AI) wantTrapSteal(p perception) bool {
	return p.carrierEnemy && p.gapToBall < p.me.Stats().PullRange+a.tune.stealRange
}
