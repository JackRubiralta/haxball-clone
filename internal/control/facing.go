package control

import (
	"phootball/internal/config"
	"phootball/internal/geom"
	"phootball/internal/sim"
)

// Facing module. One place that decides where a player POINTS (the Intent.Aim), which under the
// directional move model also decides how FAST it moves: speed scales with how aligned the Move
// vector is with Facing (directionalSpeedMul -- MoveForward ahead, MoveSide at 90deg, MoveBack at
// 180deg), so facing is not cosmetic, it is the throttle.
//
// Jack's rule, and the whole policy here: FACE WHERE YOU RUN to move fast, and turn to face the
// BALL only when you are about to interact with it (receive / control / shoot). The three contexts:
//
//	off the ball   -> faceAim: face the travel direction while transiting, turn to the action
//	                  target within a turn-time LEAD before receiving. Hysteresis (faceActioning +
//	                  a release band) keeps the travel<->action decision from flip-flopping (jitter).
//	on the ball    -> aimKeepingBall: rotate SMOOTHLY toward the want-direction while keeping the
//	                  ball glued to the front cone, scooping a strayed ball back first (recovering).
//	everywhere     -> capAim: a global rate-limit so the disk can never snap-turn faster than its
//	                  own turn rate when it is away from the ball (where it uses the instant aim
//	                  primitives); near the ball aimKeepingBall already turns within this cap.
//
// The Standard move model is speed-neutral, so under it faceAim always faces the action target
// (byte-identical to the pre-directional behaviour). All the directional nuance lives below and is
// driven by aiTuning (faceActionGap / faceLeadMargin / faceMoveThrottle / faceReleaseBand), so it
// is sweepable.

// aimProjectDist is how far an Aim point is projected from the player. The simulation faces
// the player toward the Aim POINT, so projecting it far (well beyond the pitch) makes the
// facing essentially a direction that barely shifts as the player shuffles around. This is
// what stops the facing from JITTERING when an intent is reused across the reaction-delay
// window: a near aim point would swing wildly as the player moved past it; a far one does not.
const aimProjectDist = 1000

// aimCapGap is the surface gap to the ball beyond which the AI's facing is rate-limited (it can
// only re-orient at maxTurnRad). Inside it the player is near enough to interact with the ball,
// so the facing must stay responsive (rate-limiting it there fights the centre-pull control loop
// and jitters the scoop).
const aimCapGap = 60.0

// aimToward returns an Aim point that faces the player toward target from its current
// position, projected far so it stays stable under reaction-delay caching.
func (a *AI) aimToward(p perception, target geom.Vec) geom.Vec {
	dir := geom.Unit(target.Sub(p.me.Position()))
	if dir == (geom.Vec{}) {
		dir = p.me.Facing()
	}
	return p.me.Position().Add(dir.Scale(aimProjectDist))
}

// faceAim chooses the Aim point (facing) for an OFF-BALL player so it MOVES FAST under the
// directional move model while still receiving cleanly. Jack's rule: under directional you move
// faster the more your facing aligns with your travel direction, but you receive the ball better
// facing it -- so face where you RUN while transiting, and turn to face the BALL just before you
// receive. Concretely:
//   - Standard model: facing is speed-neutral, so always face the action target. Byte-identical to
//     the old behaviour, which is why this is gated on the model (the standard path can't regress).
//   - Directional model: face the action target when not moving, when a pass is genuinely incoming
//     (receivingPass/wantTrapReceive -> turn to receive NOW), or when close enough that we must
//     start turning to be aligned in time (shouldFaceAction, a TURN-TIME lead). Otherwise face the
//     TRAVEL direction so directionalSpeedMul grants forward (MoveForward) speed instead of the
//     side/back penalty.
//
// Must be called AFTER in.Move is set (it reads in.Move as the travel heading). It carries HYSTERESIS
// (a.faceActioning + a release band) so the travel<->action decision can't flip-flop tick to tick and
// jitter the facing.
func (a *AI) faceAim(p perception, in sim.Intent, actionTarget geom.Vec) geom.Vec {
	if p.moveModel == config.MoveStandard {
		a.faceActioning = true
		return a.aimToward(p, actionTarget)
	}
	// Not moving meaningfully, or a pass is genuinely incoming -> face the action (and stay there).
	if geom.Norm(in.Move) < 1e-6 || in.Throttle < a.tune.faceMoveThrottle || a.receivingPass(p) || a.wantTrapReceive(p) {
		a.faceActioning = true
		return a.aimToward(p, actionTarget)
	}
	lead := a.faceLeadDist(p, actionTarget)
	gap := geom.Dist(p.me.Position(), actionTarget)
	if a.faceActioning {
		if gap > lead*a.tune.faceReleaseBand { // clearly back in transit -> face the run
			a.faceActioning = false
		}
	} else if gap < lead { // close enough that we must turn now to be aligned in time
		a.faceActioning = true
	}
	if a.faceActioning {
		return a.aimToward(p, actionTarget)
	}
	return a.aimToward(p, p.me.Position().Add(in.Move))
}

// faceLeadDist is the distance to the action target within which an off-ball player must already be
// turning toward it to be aligned in time: MaxSpeed * (angle-to-turn / TurnRate) * faceLeadMargin,
// plus a floor (faceActionGap) so a player right on top of the ball always faces it.
func (a *AI) faceLeadDist(p perception, target geom.Vec) float64 {
	tr := p.me.Tuning().TurnRate
	if tr <= 0 {
		return 1e9
	}
	ang := geom.AngleBetween(p.me.Facing(), target.Sub(p.me.Position()))
	return p.me.Tuning().MaxSpeed*(ang/tr)*a.tune.faceLeadMargin + a.tune.faceActionGap
}

// updateRecovering applies HYSTERESIS to the "scoop the ball back to the front" state: the
// player starts recovering once the ball drifts past the front control cone, and keeps recovering
// until the ball is well back inside it (half the cone). Without this band the facing toggles
// between the ball and the target every time the ball grazes the cone edge -- the turning jitter.
// It returns whether the player is recovering.
func (a *AI) updateRecovering(p perception) bool {
	toBall := geom.Unit(p.ball.Sub(p.me.Position()))
	if toBall == (geom.Vec{}) {
		a.recovering = false
		return false
	}
	ang := geom.AngleBetween(p.me.Facing(), toBall)
	cone := a.tune.recoverConeRad
	if ang > cone {
		a.recovering = true
	} else if ang < cone*0.5 {
		a.recovering = false
	}
	return a.recovering
}

// aimKeepingBall returns the Aim point for a player CONTROLLING the ball that wants to end up
// facing `want` (a travel heading, a shot target, a pass target...). It keeps the ball on
// the front -- where the pull is strongest -- by rotating the facing SMOOTHLY rather than
// snapping it (a snap flings the ball and looks like jitter). If the ball has drifted off the
// front cone it first turns to face the BALL, scooping it back to the front (with hysteresis,
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
	// slow down as the ball drifts toward the edge of the front cone, so it stays glued and we
	// never leave it behind -- leaving it behind is what restarts recovery and jitters the
	// facing back and forth. (When recovering we are turning TOWARD the ball, so no cap.)
	if !recovering && toBall != (geom.Vec{}) {
		ballAng := geom.AngleBetween(p.me.Facing(), toBall)
		cone := a.tune.recoverConeRad
		turn *= clampFloat(1-ballAng/cone, 0.3, 1)
	}
	newFace := rotateToward(p.me.Facing(), desiredFace, turn)
	return p.me.Position().Add(newFace.Scale(aimProjectDist)) // project far: cache-stable facing
}

// capAim caps how fast the AI can re-orient: it rotates the player's current facing toward the
// aimed direction by at most maxTurnRad and re-projects far, so the AI's disk can never
// snap-turn instantly -- it can only switch direction at its own turn rate. aimKeepingBall
// already turns within this cap (on-ball aim is unchanged); this only reins in the faster
// instant-aim paths (aimToward, used off-ball, by the keeper, and during recovery).
func (a *AI) capAim(p perception, in sim.Intent) sim.Intent {
	if in.Aim == (geom.Vec{}) {
		return in
	}
	facing := p.me.Facing()
	desired := geom.Unit(in.Aim.Sub(p.me.Position()))
	if facing == (geom.Vec{}) || desired == (geom.Vec{}) {
		return in
	}
	capped := rotateToward(facing, desired, a.tune.maxTurnRad)
	in.Aim = p.me.Position().Add(capped.Scale(aimProjectDist))
	return in
}
