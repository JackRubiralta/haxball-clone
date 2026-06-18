package sim

import (
	"math"

	"phootball/internal/geom"
)

// CurveSpec binds an AngleCurve to its front (0 rad) and back (pi rad) endpoints,
// so a single value fully describes an angle-dependent quantity.
type CurveSpec struct {
	Curve AngleCurve
	Front float64
	Back  float64
}

// Eval evaluates the quantity at the given angle in RADIANS, where 0 is dead in front of
// the player and pi is directly behind.
func (s CurveSpec) Eval(angle float64) float64 {
	return s.Curve(s.Front, s.Back, 0, math.Pi, angle)
}

// PlayerStats is the full per-player tuning. A role preset is simply a PlayerStats
// value, so different players configure distinct bounce, stickiness, shot power,
// speed, and size (a defensive keeper versus an attacking striker).
type PlayerStats struct {
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
	PullRange  float64

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

	// Front-cone capture (radians): the ball reliably sticks only within CaptureConeRadians
	// of the facing direction; over the next CaptureConeSoft radians capture decays to the
	// CaptureSpeed.Back floor, so side/back hits bounce off.
	CaptureConeRadians float64
	CaptureConeSoft    float64 // radians

	// Ball seating: per-second rate a touching ball is drawn flush to the surface.
	SeatStrength float64

	// Possession / control build-up: possession grows to 1 over PossessionBuildSeconds
	// while the ball is touching ANYWHERE (and decays over PossessionReleaseSeconds
	// otherwise); control uses the same timing but builds only while the ball is touching
	// within PossessionArcRadians of the front. PossessionArcRadians now gates only the
	// (currently unused) control state, not possession.
	//
	// Possession modulates the two hold forces only MILDLY and in OPPOSITE directions:
	//   - CenterPullGripFloor sets the centre-pull's grip at possession 0; it rises to 1 at
	//     full possession, so a high floor means possession barely changes the centre-pull.
	//   - StickinessPossessionDebuff slightly REDUCES stickiness with possession: the
	//     stickiness grip is (1 - StickinessPossessionDebuff*possession), a touch lower when
	//     fully settled.
	PossessionBuildSeconds     float64
	PossessionReleaseSeconds   float64
	PossessionArcRadians       float64
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

	// Aim assist: a shot is fired radially (player centre -> ball), but when the ball sits in
	// the front hemisphere the direction is nudged toward where the player is FACING, so the
	// shot goes where the player aims even if the ball isn't perfectly centred. ShootAimAssist
	// is the max blend weight (0 = pure radial, the raw physics; 1 = fire fully along the
	// facing direction). The assist holds at full strength within ShootAimAssistConeRadians,
	// then degrades across the rest of the front hemisphere to zero at +-90deg (much worse near
	// the edges -- see aimAssistWeight/frontShotFalloff). ShootAimAssistSoftRadians is no longer
	// used (the falloff now spans the whole hemisphere). A shot can't reach behind +-90deg.
	ShootAimAssist            float64
	ShootAimAssistConeRadians float64
	ShootAimAssistSoftRadians float64 // deprecated: superseded by the hemisphere falloff

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

// captureMul maps a coefficient in [-1,1] to the capture-speed multiplier (1.0 at 0).
func (tq TouchQuality) captureMul(coef float64) float64 {
	return triLerp(tq.CaptureWorst, 1, tq.CaptureBest, clampUnitSigned(coef))
}

// restitutionMul maps a coefficient in [-1,1] to the restitution multiplier (1.0 at 0).
func (tq TouchQuality) restitutionMul(coef float64) float64 {
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

// centerPullGrip is the centre-pull's grip at the given possession: it rises from
// CenterPullGripFloor (possession 0) to 1 (full possession). A high floor means possession
// changes the centre-pull only a little.
func (s PlayerStats) centerPullGrip(possession float64) float64 {
	return s.CenterPullGripFloor + (1-s.CenterPullGripFloor)*possession
}

// stickinessGrip is the stickiness grip at the given possession: 1 at a fresh touch, trimmed
// slightly DOWN with possession by StickinessPossessionDebuff (a settled carrier is a hair
// less sticky).
func (s PlayerStats) stickinessGrip(possession float64) float64 {
	return 1 - s.StickinessPossessionDebuff*possession
}

// captureConeRadians is the reliable-capture cone half-angle adjusted by the touch
// coefficient, ASYMMETRICALLY: the buff (coef > 0) WIDENS it a little (ConeBonusRadians per
// unit) for the owning team, while the debuff (coef < 0) NARROWS it a lot (the larger
// ConeDebuffRadians per unit) so a debuffed opponent's cone gets way smaller and it catches
// far less off the dead-on line. Never negative. (Dead-on, angle 0, is always inside the cone,
// so shots/captures straight on are unchanged -- only off-axis catching shrinks.)
func (s PlayerStats) captureConeRadians(coef float64) float64 {
	per := s.TouchQuality.ConeBonusRadians
	if coef < 0 {
		per = s.TouchQuality.ConeDebuffRadians
	}
	if r := s.CaptureConeRadians + per*coef; r > 0 {
		return r
	}
	return 0
}

// shotFalloffExp shapes how a shot's power and aim assist fall off across the front 180deg
// hemisphere: > 1 keeps them near full for most of the front and then drops MUCH faster toward
// the +-90deg edges (and to nothing behind). So a left-click shot "works for the whole front
// but gets much worse at the ends" -- both weaker and less accurately aimed near the sides.
const shotFalloffExp = 3.0

// frontShotFalloff is 1 at dead front (angle 0), falls to 0 at the front-hemisphere edge
// (pi/2), and is 0 beyond (a shot can't reach behind), dropping much faster toward the edge.
func frontShotFalloff(angle float64) float64 {
	x := angle / (math.Pi / 2)
	if x >= 1 {
		return 0
	}
	return 1 - math.Pow(x, shotFalloffExp)
}

// aimAssistWeight returns the shot aim-assist blend weight for a ball sitting `angle` radians
// off the facing direction: full (ShootAimAssist) within the front cone, then degrading across
// the rest of the front hemisphere to zero at +-90deg (much worse toward the edge), and zero
// behind. Returns 0 when the assist is disabled (ShootAimAssist <= 0).
func (s PlayerStats) aimAssistWeight(angle float64) float64 {
	if s.ShootAimAssist <= 0 || angle >= math.Pi/2 {
		return 0
	}
	cone := s.ShootAimAssistConeRadians
	if angle <= cone {
		return s.ShootAimAssist
	}
	x := (angle - cone) / (math.Pi/2 - cone) // 0 at the cone edge -> 1 at +-90deg
	return s.ShootAimAssist * (1 - math.Pow(x, shotFalloffExp))
}

// ShootDirection returns the actual launch direction of a shot: the radial direction
// (player centre -> ball, the raw kick direction) blended toward `facing` by the aim
// assist when the ball sits within the front cone. Both inputs must be unit vectors. This
// is the single source of truth for the shot direction, used by the sim to fire and by the
// AI to predict the launch so its aim matches the physics.
func (s PlayerStats) ShootDirection(radial, facing geom.Vec) geom.Vec {
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

// DefaultStats returns the baseline player tuning.
func DefaultStats(shootForce float64) PlayerStats {
	return PlayerStats{
		Radius:         18,
		Mass:           20,
		Friction:       -1.5,
		MaxSpeed:       140,
		Acceleration:   300,
		TurnRate:       14, // snappy but non-instant: a full 180 turn takes ~0.22s (limits both movement and the human cursor aim)
		TouchRange:     2,
		PullRange:      5,                                            // reduced reach (was 6)
		Restitution:    CurveSpec{InverseQuadraticCurve, 0.24, 0.20}, // front 0.30->0.24: baseline capture improved (a neutral receiver sometimes catches a blast); buff/debuff endpoints held via the multipliers
		CaptureSpeed:   CurveSpec{LinearCurve, 230, 30},              // baseline front 230 (left as-is): the buff endpoint (~236) is barely above it, so raising baseline would invert the capture buff; capture improved via restitution+control instead
		CenterPull:     CurveSpec{InverseQuadraticCurve, 800, 0},     // power reduced (950 -> 800)
		Stickiness:     CurveSpec{InverseQuadraticCurve, 420, 30},    // front restored to 420; small baseline hold at the back (0 -> 30)
		Control:        CurveSpec{LinearCurve, 1850, 340},            // roll-to-front speed raised further (1700->1850) to help capture
		Shoot:          CurveSpec{LinearCurve, shootForce, shootForce * 0.3},
		ControlDamping: 11,
		OrbitStick:     8,

		CaptureConeRadians: 0.3839724354387525, // ~22deg (widened: bigger cone)
		CaptureConeSoft:    0.5235987755982988, // ~30deg (wider falloff)

		SeatStrength: 14,

		PossessionBuildSeconds:     1.5,
		PossessionReleaseSeconds:   0.4,
		PossessionArcRadians:       0.8726646259971648, // ~50deg
		CenterPullGripFloor:        0.65,               // possession changes the centre-pull much less than before (0.65 -> 1.0, vs the old 0.3 -> 1.0)
		StickinessPossessionDebuff: 0.03,               // possession trims stickiness a hair (down to 0.97 at full)

		PossessionSpeedFactor: 0.925, // ~7.5% slower top speed while carrying the ball
		PossessionAccelFactor: 0.925, // ~7.5% slower acceleration while carrying the ball

		PossessionControlBonus: 0.09, // up to +9% roll-to-front control at full possession (x1.09)
		PossessionStealRate:    1.0,  // a challenger drains/gains 1.0 possession per second while contesting the ball

		MinShootFactor:   0.35,
		ShootSpeedFactor: 0.35,
		ShootAccelFactor: 0.4,

		ShootAimAssist:            1.0,                // full snap to the facing direction inside the cone
		ShootAimAssistConeRadians: 0.2617993877991494, // ~15deg: full assist within the front cone either way
		ShootAimAssistSoftRadians: 0,                  // no soft band (side/back = pure radial)

		TrapPullBonus:         1.0,
		TrapRangeBonus:        6,
		TrapControlBonus:      1.25,
		TrapStickinessBonus:   0.5, // a held trap stiffens the sticky hold (up to +50% at full trap)
		TrapAccelFactor:       0.55,
		TrapSpeedFactor:       0.5,
		TrapCaptureBonus:      60, // small capture bump; the trap now relies on deadening the bounce
		TrapRadiusBonus:       0,
		TrapRestitutionFactor: 0.4, // reduced further (was 0.8): even a full trap only damps a bounce to ~60%, so a max shot deflects off a trapping keeper

		TouchQuality: TouchQuality{
			OwnTeamMax:        1.0,                 // owning team at full charge -> the cleanest touch
			OtherTeam:         -1.0,                // other team at the owner's full charge -> worst-case touch (ball flies off)
			CaptureWorst:      0.628,               // debuffed front capture ~144 (230*0.628): a conceding opponent absorbs even less, so the ball bounces off it sooner
			CaptureBest:       1.025,               // buffed front capture ~236 (230*1.025): a buffed teammate captures slightly firmer balls than baseline (still far below a full shot, so it also bounces a point-blank blast)
			RestitutionWorst:  1.875,               // debuffed front bounce ~0.45 (0.24*1.875): HELD at the prior value, springier than neutral so a conceding team still deflects the ball off
			RestitutionBest:   0.844,               // buffed front bounce ~0.20 (0.24*0.844): HELD at the prior value, a buffed teammate deflects gentler than neutral (still bounces a blast)
			ConeBonusRadians:  0.05235987755982988, // ~3deg: a slight cone widening at full team buff (biggest cone)
			ConeDebuffRadians: 0.20943951023931953, // ~12deg: a debuffed opponent's cone shrinks a lot (to ~10deg) but a bit less than before (was ~15deg/~7deg) -- still well under the baseline
		},
	}
}
