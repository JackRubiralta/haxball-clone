package config

import (
	"math"

	"phootball/internal/geom"
)

// ballAngle is the angle in radians between two unit vectors (0 = aligned, pi = opposite),
// clamped against floating-point drift. The shot/aim methods below use it; sim has its own
// copy for the dribble resolver.
func ballAngle(normal, facing geom.Vec) float64 {
	cos := geom.Dot(normal, facing)
	if cos > 1 {
		cos = 1
	} else if cos < -1 {
		cos = -1
	}
	return math.Acos(cos)
}

// PlayerTuning is the full per-player parameter set: bounce, stickiness, shot power,
// speed, size, trap behaviour, and the capture/control/centre-pull cones (the section
// documented in the README). Every player on the pitch shares ONE PlayerTuning value --
// keeper, defenders, midfielders and the attacker are all physically identical (see
// TuningForRole); Role selects only AI behaviour, never stats. To change a parameter for
// everyone, edit DefaultPlayerTuning below: it is the single source of truth.
type PlayerTuning struct {
	// Body / motion. MaxSpeed is a SOFT cap: the player's own acceleration cannot push
	// past it, but a knock can exceed it and friction (not a hard clamp) bleeds the
	// excess off, so the player is never snapped down.
	Radius       float64
	Mass         float64
	Friction     float64
	MaxSpeed     float64
	Acceleration float64
	TurnRate     float64 // max radians/sec the movement heading can rotate (0 = instant)

	// Ball-control geometry (surface gaps).
	TouchRange float64
	// PullRange is the BASE reach of the centre-pull: the surface gap within which the player
	// can act on the ball. It seeds the dribble attraction (pullRadius, which a held trap
	// EXTENDS) -- see pullRadius / handleBallToPlayerInteraction.
	PullRange float64
	// PossessionRange is the reach used by the POSSESSION contest (who builds/contests/steals
	// player- and team-possession -- see Match.inPullRange / playerReach). It is a SEPARATE knob
	// from PullRange so possession reach can be tuned independently of the attraction base, and --
	// crucially -- it is NEVER trap-extended: a trap may pull the ball in from further, but it must
	// not widen who owns possession (see possessionReach). A value <= 0 means "use PullRange", so
	// it defaults to the attraction base and any PlayerTuning that omits it behaves as before.
	PossessionRange float64

	// Angle-dependent quantities (each is a curve plus its front/back endpoints).
	Restitution  CurveSpec // bounce: soft front touch -> springy back
	CaptureSpeed CurveSpec // impact speed below which the ball sticks instead of bouncing
	CenterPull   CurveSpec // radial pull toward the player centre while not yet touching
	Stickiness   CurveSpec // capped adhesion that holds a touching ball until a push overcomes it
	Control      CurveSpec // tangential pull that rolls a touching ball to the front
	Shoot        CurveSpec // shot power

	// Scalar damping / hold.
	ControlDamping float64 // bleeds off sideways speed so the ball settles at the front
	OrbitStick     float64 // inward hold proportional to the ball's orbital speed around the
	// player, so a hard turn curves the ball around it instead of flinging it off (touching only)

	// Front-cone capture (radians): the ball reliably sticks at FULL strength within
	// CaptureConeRadians of the facing, then the capture speed follows the CaptureSpeed curve from
	// that edge out to the CaptureSpeed.Back floor at the back (exactly like the control and
	// centre-pull cones), so side/back hits bounce off.
	CaptureConeRadians float64

	// Control cone (radians): roll-to-front control is at FULL strength within ControlConeRadians
	// of the facing direction, then follows the Control curve from that edge out to the back (so
	// the graph is flat-max inside the cone, then a smooth decay -- continuous at the edge).
	// UNLIKE the capture cone it is NOT scaled by the team possession buff/debuff; instead the
	// player's OWN possession widens it (ControlConePossessionBonus at full possession).
	ControlConeRadians         float64
	ControlConePossessionBonus float64 // radians added at full player possession

	// Trap cone bonuses: a held trap widens the CAPTURE cone (CaptureConeTrapBonus) and the
	// CONTROL cone (ControlConeTrapBonus) by these amounts at full trap, each per unit of trap
	// strength, so a held trap catches and steers the ball over a wider arc.
	CaptureConeTrapBonus float64
	ControlConeTrapBonus float64

	// Centre-pull cone (radians): the inward centre-pull is at FULL strength within
	// CenterPullConeRadians of the facing, then follows the CenterPull curve from that edge.
	// Like the control cone it is NOT team-buff/debuff scaled; the player's own possession
	// (CenterPullConePossessionBonus) and a held trap (CenterPullConeTrapBonus) widen it.
	CenterPullConeRadians         float64
	CenterPullConePossessionBonus float64
	CenterPullConeTrapBonus       float64

	// Ball seating: per-second rate a touching ball is drawn flush to the surface.
	SeatStrength float64

	// Possession build-up: possession grows to 1 over PossessionBuildSeconds while the ball is
	// touching ANYWHERE (and decays over PossessionReleaseSeconds otherwise).
	//
	// Possession modulates the two hold forces only MILDLY and in OPPOSITE directions:
	//   - CenterPullGripFloor sets the centre-pull's grip at possession 0; it rises to 1 at
	//     full possession, so a high floor means possession barely changes the centre-pull.
	//   - StickinessPossessionDebuff slightly REDUCES stickiness with possession: the
	//     stickiness grip is (1 - StickinessPossessionDebuff*possession), a touch lower when
	//     fully settled.
	PossessionBuildSeconds     float64
	PossessionReleaseSeconds   float64
	CenterPullGripFloor        float64
	StickinessPossessionDebuff float64

	// Possession movement penalty: while the player has the ball at its feet (touching
	// it), it moves a little slower. These scale the (soft) top speed and the
	// acceleration independently while in possession (separate vars so each tunes on its
	// own). 0.925 = ~7.5% slower.
	PossessionSpeedFactor float64
	PossessionAccelFactor float64

	// PossessionControlBonus is a small PER-PLAYER (not team) boost to the Control force
	// (the tangential roll-to-front), scaled by the player's own possession: the Control
	// strength is multiplied by (1 + PossessionControlBonus*possession), so a settled
	// carrier rolls the ball to its front a touch more crisply. 0.09 = up to +9% (x1.09) at
	// full possession.
	PossessionControlBonus float64

	// PossessionStealRate is how fast player possession transfers from the current ball-holder
	// to a CHALLENGER while BOTH are on the ball (a contest): per second, the challenger gains
	// this much possession and the holder loses the same, so a sustained challenge wins the ball
	// GRADUALLY rather than snatching it instantly. The ball changes hands once the challenger
	// holds more than the holder. (A passed ball has possession 0 -- shoot resets it -- so a
	// received pass transfers nothing; only a contested take carries possession.)
	PossessionStealRate float64

	// Charged shot: a tap fires at MinShootFactor of the angle power, a full charge at
	// the full power. While charging, the player slows even more than while trapping --
	// ShootSpeedFactor / ShootAccelFactor scale the (soft) top speed and acceleration
	// with the shoot charge (set lower than the trap factors).
	MinShootFactor   float64
	ShootSpeedFactor float64
	ShootAccelFactor float64

	// Aim assist: a shot is fired radially (player centre -> ball), but when the ball sits in the
	// fire cone the launch direction is blended toward where the player is FACING so the shot goes
	// where the player aims even when the ball isn't centred. ShootAimAssist is the blend weight
	// (0 = pure radial / raw physics; 1 = fire exactly along the facing). It applies UNIFORMLY across
	// the whole fire cone -- there is no angular falloff -- and a shot can't reach at or behind the
	// +-90deg edge. So at e.g. 0.97 the launch lands 97% of the way from the ball's radial toward the
	// facing, for any ball in front.
	ShootAimAssist float64

	// Trap ("good touch"): a 0..1 trap charge (built while the trap button is held)
	// scales these -- a stronger, longer-reach centre-pull (to trap/steal a loose
	// ball), a stronger roll-to-front control, an easier capture (less bounce), and
	// slower acceleration.
	TrapPullBonus       float64
	TrapRangeBonus      float64
	TrapControlBonus    float64
	TrapStickinessBonus float64 // trap also stiffens the sticky hold: Stickiness *= (1 + TrapStickinessBonus*trapCharge)
	TrapAccelFactor     float64 // acceleration multiplier at full trap (lower = slower to speed up)
	TrapSpeedFactor     float64 // max-speed multiplier at full trap (lower = slower top speed)
	TrapCaptureBonus    float64
	TrapRadiusBonus     float64 // 0 = the player does not change size while trapping
	// TrapRestitutionFactor is how strongly the trap charge SUPPRESSES the bounce on a hard
	// contact: the bounce restitution is scaled by (1 - min(1, trapCharge*TrapRestitutionFactor)),
	// so >1 makes the ball stop bouncing before a full trap (a held trap deadens the ball, on
	// top of the higher capture speed). 1.0 = bounce only fully killed at a full trap; 0 = trap
	// never affects bounce.
	TrapRestitutionFactor float64

	// Trap ENERGY bar (the resource model). Holding the trap drains the 0..1 energy bar at
	// TrapDrainPerSecond; releasing refills it at TrapRegenPerSecond (default ~1/3 of drain). The
	// effective strength (trapAura, which drives every trap effect + the glow) ramps toward its
	// energy-limited peak -- and back down -- at TrapAuraRatePerSecond, a constant LINEAR rate that is
	// the SAME both ways, so the fade is exactly as gradual as the come-up (not a faster collapse).
	TrapDrainPerSecond    float64
	TrapRegenPerSecond    float64
	TrapAuraRatePerSecond float64
	// TrapMinAura is the floor the effective strength holds at while the trap is HELD with the
	// energy bar fully drained -- so an empty-bar trap keeps a faint residual glow + weak good-touch
	// rather than collapsing to nothing. Released, the aura still falls all the way to 0.
	TrapMinAura float64

	// TouchQuality folds the TEAM POSSESSION CHARGE into how cleanly a player takes an
	// incoming ball (its capture speed and bounce). See the TouchQuality type.
	TouchQuality TouchQuality
}

// TouchQuality tunes how the TEAM POSSESSION CHARGE modulates an incoming ball's capture
// speed and restitution (bounce) for a player. The charge is a 0..1 strength owned by one
// team (it builds while that team holds the ball and persists/decays after a pass -- see
// Match.advanceTeamPossession). Each player's per-tick "quality coefficient" is derived from
// it and folds into a single value in [-1, +1]:
//
//   - coefficient 0 reproduces the BASELINE -- the unscaled CaptureSpeed / Restitution
//     curves (today's values). This is a loose ball, or either team at zero charge.
//   - POSITIVE (the OWNING team, scaled by charge up to OwnTeamMax) is a cleaner touch: the
//     ball sticks at higher impact speeds (capture up) and bounces off less (restitution
//     down) -- so built-up possession is received and carried cleanly.
//   - NEGATIVE (the OTHER team, scaled by the owner's charge down to OtherTeam) is a worse
//     touch: harder to capture and springs off harder -- so a shot blocked by a team that
//     has conceded possession flies further off them.
//
// Capture and restitution each map the coefficient through their own worst/best multipliers,
// anchored at 1.0 for coefficient 0.
type TouchQuality struct {
	OwnTeamMax float64 // coefficient for the owning team at full charge (> 0: the cleanest touch)
	OtherTeam  float64 // coefficient for the other team at the owner's full charge (< 0: ball flies off)

	CaptureWorst float64 // capture-speed multiplier at coefficient -1 (< 1: harder to capture)
	CaptureBest  float64 // capture-speed multiplier at coefficient +1 (> 1: sticks at higher speed)

	RestitutionWorst float64 // restitution multiplier at coefficient -1 (> 1: bouncier, ball flies)
	RestitutionBest  float64 // restitution multiplier at coefficient +1 (< 1: deader, ball sticks)

	// The reliable capture cone scales with the team-possession coefficient, ASYMMETRICALLY:
	// the buff WIDENS it by ConeBonusRadians per +coefficient (a slight grow -- biggest cone at
	// full possession), while the debuff NARROWS it by the larger ConeDebuffRadians per
	// -coefficient (a debuffed opponent's cone gets WAY smaller, so it catches far less off the
	// dead-on line). Net: buff = biggest, no buff = baseline, debuff = much smaller.
	ConeBonusRadians  float64
	ConeDebuffRadians float64
}

// CaptureMul maps a coefficient in [-1,1] to the capture-speed multiplier (1.0 at 0).
func (tq TouchQuality) CaptureMul(coef float64) float64 {
	return triLerp(tq.CaptureWorst, 1, tq.CaptureBest, clampUnitSigned(coef))
}

// RestitutionMul maps a coefficient in [-1,1] to the restitution multiplier (1.0 at 0).
func (tq TouchQuality) RestitutionMul(coef float64) float64 {
	return triLerp(tq.RestitutionWorst, 1, tq.RestitutionBest, clampUnitSigned(coef))
}

// triLerp interpolates a value across three anchor points by a parameter t in [-1, 1]:
// worst at t=-1, mid at t=0, best at t=+1, linear on each side of the midpoint.
func triLerp(worst, mid, best, t float64) float64 {
	if t >= 0 {
		return mid + (best-mid)*t
	}
	return mid + (mid-worst)*t
}

// clampUnitSigned clamps a value to [-1, 1]; clampUnit clamps to [0, 1].
func clampUnitSigned(v float64) float64 {
	if v > 1 {
		return 1
	}
	if v < -1 {
		return -1
	}
	return v
}

func clampUnit(v float64) float64 {
	if v > 1 {
		return 1
	}
	if v < 0 {
		return 0
	}
	return v
}

// CenterPullGrip is the centre-pull's grip at the given possession: it rises from
// CenterPullGripFloor (possession 0) to 1 (full possession). A high floor means possession
// changes the centre-pull only a little.
func (s PlayerTuning) CenterPullGrip(possession float64) float64 {
	return s.CenterPullGripFloor + (1-s.CenterPullGripFloor)*possession
}

// StickinessGrip is the stickiness grip at the given possession: 1 at a fresh touch, trimmed
// slightly DOWN with possession by StickinessPossessionDebuff (a settled carrier is a hair
// less sticky).
func (s PlayerTuning) StickinessGrip(possession float64) float64 {
	return 1 - s.StickinessPossessionDebuff*possession
}

// CaptureCone is the reliable-capture cone half-angle adjusted by the touch
// coefficient, ASYMMETRICALLY: the buff (coef > 0) WIDENS it a little (ConeBonusRadians per
// unit) for the owning team, while the debuff (coef < 0) NARROWS it a lot (the larger
// ConeDebuffRadians per unit) so a debuffed opponent's cone gets way smaller and it catches
// far less off the dead-on line. Never negative. (Dead-on, angle 0, is always inside the cone,
// so shots/captures straight on are unchanged -- only off-axis catching shrinks.)
func (s PlayerTuning) CaptureCone(coef, trapAura float64) float64 {
	per := s.TouchQuality.ConeBonusRadians
	if coef < 0 {
		per = s.TouchQuality.ConeDebuffRadians
	}
	if r := s.CaptureConeRadians + per*coef + s.CaptureConeTrapBonus*trapAura; r > 0 {
		return r
	}
	return 0
}

// ControlCone is the half-angle within which roll-to-front control is at full strength.
// It is NOT affected by the team possession buff/debuff: the player's OWN possession widens it
// (ControlConePossessionBonus at full) and a held trap widens it a little (ControlConeTrapBonus),
// so a settled or trapping carrier steers the ball over a wider arc. Inputs are clamped to [0,1].
func (s PlayerTuning) ControlCone(possession, trapAura float64) float64 {
	return s.ControlConeRadians + s.ControlConePossessionBonus*clampUnit(possession) + s.ControlConeTrapBonus*clampUnit(trapAura)
}

// CenterPullCone is the half-angle within which the centre-pull is at full strength. Like
// the control cone it is NOT team-buff/debuff scaled; the player's own possession and a held trap
// widen it. Inputs are clamped to [0,1].
func (s PlayerTuning) CenterPullCone(possession, trapAura float64) float64 {
	return s.CenterPullConeRadians + s.CenterPullConePossessionBonus*clampUnit(possession) + s.CenterPullConeTrapBonus*clampUnit(trapAura)
}

// The curve SHAPE for each angle-dependent quantity is FIXED here (hardcoded, never a
// tunable) -- only the front/back endpoints in the CurveSpec are data. RestitutionAt evaluates
// across the whole 0..pi arc; CaptureSpeedAt/CenterPullAt/StickinessAt/ControlAt are FULL
// strength within a cone (front..coneEdge) and then follow the curve from the cone edge to the
// back, so coneEdge is passed in by the caller.

// RestitutionAt is the bounce restitution at `angle` (0 = front, pi = back): inverse-quadratic.
func (s PlayerTuning) RestitutionAt(angle float64) float64 {
	return InverseQuadraticCurve(s.Restitution.Front, s.Restitution.Back, 0, math.Pi, angle)
}

// CaptureSpeedAt is the capture speed at `angle`: full (the front peak) within the cone
// (angle <= coneEdge), then linear from the cone edge to the back.
func (s PlayerTuning) CaptureSpeedAt(coneEdge, angle float64) float64 {
	return LinearCurve(s.CaptureSpeed.Front, s.CaptureSpeed.Back, coneEdge, math.Pi, angle)
}

// CenterPullAt is the centre-pull at `angle`: full within the cone (angle <= coneEdge), then
// inverse-quadratic from the cone edge to the back.
func (s PlayerTuning) CenterPullAt(coneEdge, angle float64) float64 {
	return InverseQuadraticCurve(s.CenterPull.Front, s.CenterPull.Back, coneEdge, math.Pi, angle)
}

// StickinessAt is the sticky hold at `angle`: full within the cone, then inverse-quadratic
// from the cone edge to the back.
func (s PlayerTuning) StickinessAt(coneEdge, angle float64) float64 {
	return InverseQuadraticCurve(s.Stickiness.Front, s.Stickiness.Back, coneEdge, math.Pi, angle)
}

// ControlAt is the roll-to-front control at `angle`: full within the cone, then linear from
// the cone edge to the back.
func (s PlayerTuning) ControlAt(coneEdge, angle float64) float64 {
	return LinearCurve(s.Control.Front, s.Control.Back, coneEdge, math.Pi, angle)
}

// shotFalloffExp shapes how shot power tapers across the falloff band -- from the full-power cone
// edge out to the fire edge. At 1.0 the taper is LINEAR: power falls steadily from full to 0 across
// the band; > 1 would hold it high for most of the band and drop faster only near the +-90deg edge.
const shotFalloffExp = 1.0

// fireConeHalfAngle is the half-angle of the FIRE CONE: the left-click shot fires -- and the aim
// assist applies -- only within +-90deg of the facing, a 180deg total cone (the full front
// hemisphere). A ball at or behind +-90deg can't be shot (poke it with the middle-click push). The
// aim assist covers exactly this same fire cone, uniformly -- it is not a separate region.
const fireConeHalfAngle = 90 * math.Pi / 180

// fullPowerHalfAngle is the half-angle of the inner FULL-POWER cone: a shot anywhere within +-30deg
// of the facing fires at full power. Past it the power tapers (see shotFalloffExp) to exactly 0 at
// the +-90deg fire edge, so power is flat-max inside the cone, then fades to nothing by the arc edge.
const fullPowerHalfAngle = 30 * math.Pi / 180

// frontShotFalloff scales shot power by how far off-front the ball sits: 1 (full power) anywhere
// within the +-30deg full-power cone, then tapering linearly to 0 by the +-90deg fire edge, and 0
// at or behind that edge (a shot can't reach the sides or behind).
func frontShotFalloff(angle float64) float64 {
	if angle <= fullPowerHalfAngle {
		return 1
	}
	if angle >= fireConeHalfAngle {
		return 0
	}
	x := (angle - fullPowerHalfAngle) / (fireConeHalfAngle - fullPowerHalfAngle) // 0 at the cone edge -> 1 at the fire edge
	return 1 - math.Pow(x, shotFalloffExp)
}

// InFireCone reports whether a ball sitting `angle` radians off the facing is shootable at
// all: the left-click shot fires only within the front 180deg (the +-90deg fire cone). The
// cone SHAPE is fixed/hardcoded, not a tunable.
func InFireCone(angle float64) bool { return angle < fireConeHalfAngle }

// aimAssistWeight returns the shot aim-assist blend weight for a ball sitting `angle` radians off
// the facing direction: the full ShootAimAssist anywhere in the fire cone (UNIFORM -- no angular
// degradation), and 0 at or behind the +-90deg arc edge (the shot is front-180 only) or when the
// assist is disabled (ShootAimAssist <= 0).
func (s PlayerTuning) aimAssistWeight(angle float64) float64 {
	if s.ShootAimAssist <= 0 || angle >= fireConeHalfAngle {
		return 0
	}
	return s.ShootAimAssist
}

// ShootDirection returns the actual launch direction of a shot: the radial direction (player
// centre -> ball, the raw kick direction) blended toward `facing` by the aim assist
// (ShootAimAssist), uniformly for any ball in the fire cone. Both inputs must be unit
// vectors. This is the single source of truth for the shot direction, used by the sim to fire and
// by the AI to predict the launch so its aim matches the physics.
func (s PlayerTuning) ShootDirection(radial, facing geom.Vec) geom.Vec {
	w := s.aimAssistWeight(ballAngle(radial, facing))
	if w <= 0 {
		return radial
	}
	blended := radial.Scale(1 - w).Add(facing.Scale(w))
	if n := geom.Norm(blended); n > 0 {
		return blended.Scale(1 / n)
	}
	return radial
}

// ShootLaunchVelocity returns the velocity a left-click shot ADDS to the ball for a ball sitting
// along unit `radial` (player centre -> ball) with the player facing unit `facing`, at the given
// 0..1 charge: the aim-assisted launch direction (ShootDirection) scaled by the charge- and
// angle-scaled power -- MinShootFactor..1 of front power (Shoot.Front), tapered off the front
// cone by frontShotFalloff. Returns the zero vector for a ball at/behind the +-90deg fire edge (no
// shot fires there). This is the SINGLE SOURCE OF TRUTH for a shot's launch: the sim fires with it
// (see shoot) and the AI predicts from it (see control.launchAligned), so the AI's aim can never
// drift from the shot physics when the curves/cones/aim-assist change.
func (s PlayerTuning) ShootLaunchVelocity(radial, facing geom.Vec, charge float64) geom.Vec {
	angle := ballAngle(radial, facing)
	if angle >= fireConeHalfAngle {
		return geom.Vec{}
	}
	factor := s.MinShootFactor + (1-s.MinShootFactor)*charge
	power := s.Shoot.Front * factor * frontShotFalloff(angle)
	return s.ShootDirection(radial, facing).Scale(power)
}
