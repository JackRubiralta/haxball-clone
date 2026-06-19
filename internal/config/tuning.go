package config

import "fmt"

// Tuning collects ALL the physics/gameplay constants the simulation reads, so a match can
// be retuned from ONE place instead of through scattered package constants. It records the
// ball's body, how much energy the walls and goal frame absorb on a bounce, the per-player
// physics/feel profile (Player), and the team-possession charge timings (Possession).
// DefaultTuning() reproduces today's hand-tuned constants exactly.
type Tuning struct {
	BallRadius   float64
	BallFriction float64
	BallMass     float64

	BallWallRestitution   float64 // speed the ball keeps off a pitch wall / goal frame
	PlayerWallRestitution float64 // speed a player keeps off a wall (damped harder)
	ObstacleRestitution   float64 // bounce off fixed cone obstacles
	NetRestitution        float64 // low, so the net catches the ball instead of springing it

	// Player is the per-player physics/feel profile. Every player shares this one profile
	// (all roles are physically identical); the sim stamps it onto every player in
	// Match.applyConfig. It is plain data -- the angle-curve SHAPES are hardcoded in the
	// PlayerTuning evaluator methods, so only the numbers travel/serialize.
	Player PlayerTuning
	// Possession is the team-possession charge timings and contest drain rates.
	Possession PossessionTuning

	// Movement model: how a player's speed relates to its facing. The DEFAULT is MoveDirectional --
	// speed scales with alignment to facing (MoveForward ahead, MoveSide at 90deg off, MoveBack at
	// 180deg). MoveStandard is omnidirectional (equal speed every direction, the original feel); the
	// factors are ignored under it. See sim applyIntent / directionalSpeedMul.
	MoveModel   MoveModel
	MoveForward float64
	MoveSide    float64
	MoveBack    float64
}

// MoveModel selects how movement speed relates to facing (see Tuning.MoveModel).
type MoveModel int

const (
	// MoveStandard is the original feel: equal speed in every direction. It is the zero value, but
	// NOT the config default any more -- DefaultTuning now defaults to MoveDirectional.
	MoveStandard MoveModel = iota
	// MoveDirectional scales speed by alignment with facing (fast ahead, slow back). WASD stay
	// world-absolute, so the player can still move any direction -- it is just slower off-aim. ("Strafe".)
	MoveDirectional
	// MoveDirectionalLocked is MoveDirectional plus a facing-relative WASD frame for the human
	// (W = toward the aim, S = back, A/D = strafe). The AI is unaffected by the frame. ("Locked".)
	MoveDirectionalLocked
)

// PossessionTuning holds the team-possession charge timings and contest drain rates -- the
// "how long a possession buff builds / holds / decays" knobs (read off Match.Tuning each tick
// by the team-possession machinery in sim).
type PossessionTuning struct {
	BuildSeconds   float64 // seconds of team possession to build the charge to full
	HoldSeconds    float64 // seconds the charge holds at full strength after release (no touch)
	DecaySeconds   float64 // seconds until the charge has fully decayed after release (no touch)
	BuildExponent  float64 // build-curve exponent: higher = stays low for most of the build, spiking near the end
	DrainPerSecond float64 // owner buff-suppression gained per second while the carrier is contested (recovered per second otherwise)

	BoostContactDrainPerSecond   float64 // fraction of a player's own boost drained per second while an opponent body-touches it
	BoostContactRecoverPerSecond float64 // fraction recovered per second once no opponent is touching it
}

// Validate reports the first range error in the tuning that would break the simulation --
// the divide-by-zero traps (zero masses, zero possession-build seconds) and nonsensical sizes.
// The menu steppers clamp to sane ranges, but this is the authoritative gate: MatchSetup.Build
// and the host's applyConfig path both run it.
func (t Tuning) Validate() error {
	switch {
	case t.BallMass <= 0:
		return fmt.Errorf("ball mass must be positive")
	case t.BallRadius <= 0:
		return fmt.Errorf("ball radius must be positive")
	case t.Player.Mass <= 0:
		return fmt.Errorf("player mass must be positive")
	case t.Player.Radius <= 0:
		return fmt.Errorf("player radius must be positive")
	case t.Player.PossessionBuildSeconds <= 0 || t.Player.PossessionReleaseSeconds <= 0:
		return fmt.Errorf("player possession build/release seconds must be positive")
	case t.Possession.BuildSeconds <= 0:
		return fmt.Errorf("team possession build seconds must be positive")
	case t.Possession.DecaySeconds < t.Possession.HoldSeconds:
		return fmt.Errorf("team possession decay seconds must be >= hold seconds")
	case t.MoveModel != MoveStandard && (t.MoveForward <= 0 || t.MoveSide <= 0 || t.MoveBack <= 0):
		return fmt.Errorf("directional movement factors must be positive")
	}
	return nil
}

// DefaultTuning returns the baseline physics values that match the original game.
func DefaultTuning() Tuning {
	return Tuning{
		BallRadius:            7.5,
		BallFriction:          -0.3,
		BallMass:              1.5,
		BallWallRestitution:   0.90,
		PlayerWallRestitution: 0.50,
		ObstacleRestitution:   0.5,
		NetRestitution:        0.2,
		Player:                DefaultPlayerTuning(),
		Possession: PossessionTuning{
			BuildSeconds:                 1.5,
			HoldSeconds:                  1.5,
			DecaySeconds:                 3.5,
			BuildExponent:                3.0,
			DrainPerSecond:               1.0,
			BoostContactDrainPerSecond:   2.0,
			BoostContactRecoverPerSecond: 1.5,
		},
		MoveModel:   MoveDirectional, // default: speed scales with facing (forward fastest, slower sideways/back)
		MoveForward: 1.2,             // moving toward your aim is 20% faster than base
		MoveSide:    0.5,             // strafing is half speed
		MoveBack:    0.2,             // backpedalling is very slow
	}
}

// playerShootForce is the front shot power every player is built with (the back endpoint is
// 30% of it). It is the single canonical shot force -- there is no 500/575 split any more.
const playerShootForce = 575

// DefaultPlayerTuning is the editable per-player profile -- THE single place for every player
// number: speed, mass, the restitution/capture/control/centre-pull/shoot curve ENDPOINTS, the
// cone sizes, possession build, trap behaviour, and the team buff/debuff multipliers. The
// angle-curve SHAPES are fixed in the PlayerTuning evaluator methods (playertuning.go); only
// these numbers are tunable. config.Tuning.Player defaults to this; the menu edits a copy of
// it per match, and the sim stamps it onto every player in Match.applyConfig.
func DefaultPlayerTuning() PlayerTuning {
	return PlayerTuning{
		Radius:          18,
		Mass:            20,
		Friction:        -1.5,
		MaxSpeed:        140,
		Acceleration:    300,
		TurnRate:        14, // snappy but non-instant: a full 180 turn takes ~0.22s (limits both movement and the human cursor aim)
		TouchRange:      2,
		PullRange:       5,                     // base centre-pull reach (the dribble attraction; a held trap extends this)
		PossessionRange: 5,                     // possession-contest reach: same value as PullRange, but a SEPARATE knob and never trap-extended (see possessionReach)
		Restitution:     CurveSpec{0.21, 0.24}, // front 0.21: controlled front touch (lowered 0.23 -> 0.22 -> 0.21 for a firmer front capture, kept >0.20 so a hard pass still deflects, not sticks); back 0.24: springier behind. Multipliers unchanged -> buffed ~0.18, debuffed ~0.39
		CaptureSpeed:    CurveSpec{360, 40},    // baseline front 360, back floor 40 (off-front/side touches stick less); the team buff is multiplicative (CaptureBest), so the buffed endpoint (~396) scales with it and stays under a full shot (575), so a point-blank blast still bounces
		CenterPull:      CurveSpec{770, 0},     // baseline pull trimmed a touch (800 -> 770)
		Stickiness:      CurveSpec{350, 30},    // front 350 baseline sticky hold; back 30 small hold behind the player
		Control:         CurveSpec{1200, 320},  // roll-to-front: baseline front 1200, back 320; a held trap multiplies it by (1+TrapControlBonus)
		Shoot:           CurveSpec{playerShootForce, playerShootForce * 0.3},
		ControlDamping:  11,
		OrbitStick:      8,

		CaptureConeRadians: 0.6544984694978735, // 37.5deg reliable-capture cone for a good touch (widened 25% from 30deg)
		CaptureConeSoft:    0.9599310885968813, // 55deg falloff band past the reliable cone

		ControlConeRadians:         0.3839724354387525,  // 22deg: full roll-to-front control within this cone (44deg total)
		ControlConePossessionBonus: 0.08726646259971647, // +5deg at full player possession (-> 27deg)
		CaptureConeTrapBonus:       0.3490658503988659,  // +20deg to the capture cone at full trap (37.5 -> 57.5deg)
		ControlConeTrapBonus:       0.03490658503988659, // +2deg to the control cone at full trap

		CenterPullConeRadians:         0.08726646259971647,  // 5deg/side: full centre-pull cone (10deg total baseline)
		CenterPullConePossessionBonus: 0.017453292519943295, // +1deg/side at full player possession
		CenterPullConeTrapBonus:       0.03490658503988659,  // +2deg/side at full trap (-> 8deg/side, 16deg total at max)

		SeatStrength: 14,

		PossessionBuildSeconds:     1.5,
		PossessionReleaseSeconds:   0.4,
		CenterPullGripFloor:        0.65, // possession changes the centre-pull much less than before (0.65 -> 1.0, vs the old 0.3 -> 1.0)
		StickinessPossessionDebuff: 0.03, // possession trims stickiness a hair (down to 0.97 at full)

		PossessionSpeedFactor: 0.8,   // 80% top speed while carrying the ball (20% slower)
		PossessionAccelFactor: 0.925, // ~7.5% slower acceleration while carrying the ball

		PossessionControlBonus: 0.09, // up to +9% roll-to-front control at full possession (x1.09)
		PossessionStealRate:    1.0,  // a challenger drains/gains 1.0 possession per second while contesting the ball

		MinShootFactor:   0.35,
		ShootSpeedFactor: 0.35,
		ShootAccelFactor: 0.4,

		ShootAimAssist: 0.97, // blend 97% from the ball's radial toward the facing, uniformly across the front hemisphere

		TrapPullBonus:         0.2, // a held trap strengthens the centre-pull a little (up to +20% at full trap)
		TrapRangeBonus:        6,
		TrapControlBonus:      2.2,  // multiplier on the roll-to-front control for a held trap; full-trap control front = 1200*(1+2.2) = 3840
		TrapStickinessBonus:   0.05, // a held trap stiffens the sticky hold a hair (up to +5% at full trap)
		TrapAccelFactor:       0.4,  // applied as a CONSTANT while the trap is held (see match.go speedMul/accelMul)
		TrapSpeedFactor:       0.4,  // 40% top speed while trapping; applied as a CONSTANT the moment the trap is held
		TrapCaptureBonus:      120,  // capture-speed bump (+120 at full trap): the trap absorbs much firmer balls
		TrapRadiusBonus:       0,
		TrapRestitutionFactor: 0.4, // reduced further (was 0.8): even a full trap only damps a bounce to ~60%, so a max shot deflects off a trapping keeper

		TrapDrainPerSecond:    0.8,  // full energy bar lasts ~1.25s of trapping
		TrapRegenPerSecond:    0.27, // ~1/3 of drain: a fully spent bar refills in ~3.7s
		TrapAuraRatePerSecond: 2.0,  // constant LINEAR rate the aura GROWS and SHRINKS at -- same both ways, ~0.5s peak<->0; the fade is exactly as gradual as the come-up
		TrapMinAura:           0.06, // residual aura while a drained bar is still held (glow ~ the base pull radius); a faint good-touch, never nothing

		TouchQuality: TouchQuality{
			OwnTeamMax:        1.0,                 // owning team at full charge -> the cleanest touch
			OtherTeam:         -1.0,                // other team at the owner's full charge -> worst-case touch (ball flies off)
			CaptureWorst:      0.628,               // debuffed front capture ~226 (360*0.628): a conceding opponent absorbs even less, so the ball bounces off it sooner
			CaptureBest:       1.1,                 // buffed front capture ~396 (360*1.1): a buffed teammate captures notably firmer balls than baseline (still under a full shot, so it also bounces a point-blank blast)
			RestitutionWorst:  1.43,                // debuffed front bounce ~0.30 (0.21*1.43): softened from 1.875 so a conceding team deflects the ball off less harshly, but still springier than the neutral 0.21
			RestitutionBest:   0.844,               // buffed front bounce ~0.18 (0.21*0.844): a buffed teammate deflects gentler than neutral (still bounces a blast)
			ConeBonusRadians:  0.05235987755982988, // ~3deg: a slight cone widening at full team buff (biggest cone)
			ConeDebuffRadians: 0.2792526803190927,  // ~16deg: a debuffed opponent's reliable cone shrinks WAY down (37.5 -> ~21.5deg, ~57% of baseline) -- catches far less off the dead-on line
		},
	}
}
