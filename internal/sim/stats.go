package sim

// CurveSpec binds an AngleCurve to its front (0 deg) and back (180 deg) endpoints,
// so a single value fully describes an angle-dependent quantity.
type CurveSpec struct {
	Curve AngleCurve
	Front float64
	Back  float64
}

// Eval evaluates the quantity at the given angle, where 0 deg is dead in front of
// the player and 180 deg is directly behind.
func (s CurveSpec) Eval(angle float64) float64 {
	return s.Curve(s.Front, s.Back, 0, 180, angle)
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

	// Front-cone capture: the ball reliably sticks only within CaptureConeDegrees of
	// the facing direction; over the next CaptureConeSoft degrees capture decays to the
	// CaptureSpeed.Back floor, so side/back hits bounce off.
	CaptureConeDegrees float64
	CaptureConeSoft    float64

	// Ball seating: per-second rate a touching ball is drawn flush to the surface.
	SeatStrength float64

	// Possession build-up: while controlling the ball within PossessionArcDegrees of
	// the front, possession grows to 1 over PossessionBuildSeconds and decays over
	// PossessionReleaseSeconds otherwise. A grip multiplier (GripFloor at a fresh
	// touch, 1 at full possession) scales the centre-pull and stickiness, so a fresh
	// touch is easily stolen and established possession survives quick turns.
	PossessionBuildSeconds   float64
	PossessionReleaseSeconds float64
	PossessionArcDegrees     float64
	GripFloor                float64

	// Charged shot: a tap fires at MinShootFactor of the angle power, a full charge at
	// the full power. While charging, the player slows even more than while trapping --
	// ShootSpeedFactor / ShootAccelFactor scale the (soft) top speed and acceleration
	// with the shoot charge (set lower than the trap factors).
	MinShootFactor   float64
	ShootSpeedFactor float64
	ShootAccelFactor float64

	// Trap ("good touch"): a 0..1 trap charge (built while the trap button is held)
	// scales these -- a stronger, longer-reach centre-pull (to trap/steal a loose
	// ball), a stronger roll-to-front control, an easier capture (less bounce), and
	// slower acceleration.
	TrapPullBonus    float64
	TrapRangeBonus   float64
	TrapControlBonus float64
	TrapAccelFactor  float64 // acceleration multiplier at full trap (lower = slower to speed up)
	TrapSpeedFactor  float64 // max-speed multiplier at full trap (lower = slower top speed)
	TrapCaptureBonus float64
	TrapRadiusBonus  float64 // 0 = the player does not change size while trapping
}

// DefaultStats returns the baseline player tuning.
func DefaultStats(shootForce float64) PlayerStats {
	return PlayerStats{
		Radius:          18,
		Mass:            20,
		Friction:        -1.5,
		MaxSpeed:        140,
		Acceleration:    300,
		TouchRange:      2,
		PullRange:       6,
		Restitution:     CurveSpec{InverseQuadraticCurve, 0.08, 0.80},
		CaptureSpeed:    CurveSpec{LinearCurve, 320, 70},
		CenterPull:      CurveSpec{InverseQuadraticCurve, 1000, 0},
		Stickiness:      CurveSpec{InverseQuadraticCurve, 420, 0},
		Control:         CurveSpec{LinearCurve, 1500, 300},
		Shoot:           CurveSpec{LinearCurve, shootForce, shootForce * 0.3},
		ControlDamping: 11,
		OrbitStick:     8,

		CaptureConeDegrees: 15,
		CaptureConeSoft:    25,

		SeatStrength: 14,

		PossessionBuildSeconds:   1.5,
		PossessionReleaseSeconds: 0.4,
		PossessionArcDegrees:     50,
		GripFloor:                0.3,

		MinShootFactor:   0.35,
		ShootSpeedFactor: 0.35,
		ShootAccelFactor: 0.4,

		TrapPullBonus:    1.5,
		TrapRangeBonus:   10,
		TrapControlBonus: 1.2,
		TrapAccelFactor:  0.55,
		TrapSpeedFactor:  0.5,
		TrapCaptureBonus: 220,
		TrapRadiusBonus:  0,
	}
}
