package sim

import (
	"math"

	"phootball/internal/geom"
	"phootball/internal/physics"
)

// Ball is the game ball. It has a single mass (the Body's), used when resolving
// collisions -- including the contact with a player, where a heavier ball takes less of
// the impulse and so is harder to launch by a bump.
type Ball struct {
	*physics.Body
}

// NewBall creates the ball (friction -0.3, mass 1.5).
func NewBall(position geom.Vec, radius float64) *Ball {
	return &Ball{physics.NewCircleBody(position, radius, -0.3, 1.5)}
}

// Player is a controllable circle with per-player stats, a facing direction, and a
// role. Its body's motion is integrated by the simulation; its stats drive the
// dribble, shooting, bounce, and speed.
type Player struct {
	*physics.Body
	PlayerID     int
	Number       int // jersey number shown on the player
	Team         *Team
	Role         Role
	Stats        PlayerStats
	Facing       geom.Vec
	HomePosition geom.Vec
	WantsKick    bool

	possession    float64  // 0..1 build-up while the ball is touching anywhere; scales the grip on the ball
	control       float64  // 0..1 build-up while the ball is touching within the front arc; tracked but unused
	pullEnterSeq  uint64   // sequence stamp set when the ball entered this player's pull radius (0 = out of reach); the latest stamp is the sole possession builder
	touchCoef     float64  // -1..1 touch-quality coefficient this tick from the team possession charge
	boostDrain    float64  // 0..1 per-player erosion of THIS player's team boost while an opponent is touching it (recovers off contact)
	shootCharge   float64  // seconds the shoot button has been held this charge
	trapCharge    float64  // 0..1 trap charge; built while the trap button is held
	trapAura      float64  // 0..1 EFFECTIVE trap strength (stateful): humps while held (grow->peak->weaken), shrinks on release; drives BOTH the trap effect and its visual aura
	shootHeldPrev bool     // shoot-button state last tick, for release-edge detection
	shootCanceled bool     // current shoot charge was canceled (by a trap-tap); suppress the release-edge kick
	wantsPoke     bool     // middle-click jab requested this tick (instant min-power radial push)
	pokeFlash     float64  // 1->0 cosmetic timer set when a middle-click poke fires; drives the poke pulse animation
	trapHeldPrev  bool     // trap-button state last tick, for the trap sound's rising edge
	evictDwell    float64  // seconds spent violating a positional rule (warn-evict grace)
	moveHeading   geom.Vec // current steering direction; rotates toward input at TurnRate
}

// Charge timing constants (seconds), shared by the sim and the renderer's gauges.
const (
	shootChargeMax  = 0.75 // seconds of hold for a full-power shot (faster charge)
	trapChargeTime  = 1.25 // seconds of holding the trap button to reach full trap charge (~25% slower)
	trapChargeDecay = 3.2  // per-second decay of trap charge once released (~25% slower release/aura fade)
	pokeFlashSeconds = 0.3 // seconds for the middle-click poke pulse animation to fade out
)

// Trap strength shape: how the held trap's EFFECTIVE strength (`trapAura`) tracks the raw charge.
// This single level drives BOTH the trap effect (capture/restitution/pull/stickiness/control/
// reach/slowdown) and the visual aura, so the two always match.
const (
	trapAuraPeak           = 0.55 // charge fraction at which the trap is strongest (its max)
	trapAuraOverheldFloor  = 0.5  // strength once the trap is fully (over-)charged
	trapAuraReleaseSeconds = 0.3  // seconds for the strength/aura to shrink to nothing after release
)

// trapAuraShape maps the raw trap charge to the trap's effective strength WHILE HELD: it swells
// (ease-out) to full at trapAuraPeak, then -- as the trap is over-held toward full charge --
// weakens to trapAuraOverheldFloor, so the longer the trap is held past its peak the weaker it
// gets (and the smaller the on-screen circle). Drives both the effect and the visual aura.
func trapAuraShape(charge float64) float64 {
	if charge <= 0 {
		return 0
	}
	if charge <= trapAuraPeak {
		x := charge / trapAuraPeak
		return 1 - (1-x)*(1-x) // ease-out swell to the max at the peak
	}
	x := (charge - trapAuraPeak) / (1 - trapAuraPeak)
	return 1 - (1-trapAuraOverheldFloor)*x // weaken from the max to the floor as it is over-held
}

// ShootCharge returns the seconds the shoot button has been held this charge.
func (p *Player) ShootCharge() float64 { return p.shootCharge }

// TrapCharge returns the current 0..1 trap charge.
func (p *Player) TrapCharge() float64 { return p.trapCharge }

// TrapAura returns the current 0..1 effective trap strength: while the trap is held it swells to
// a max then weakens as the trap is over-held (see trapAuraShape), and shrinks to nothing after
// release. Drives the trap effect and is exposed for the renderer's trap glow (so they match).
func (p *Player) TrapAura() float64 { return p.trapAura }

// Possession returns the player's current 0..1 possession build-up (ball touching anywhere).
func (p *Player) Possession() float64 { return p.possession }

// Control returns the player's current 0..1 control build-up (ball touching within the
// front arc). It is tracked but not yet used by any mechanic.
func (p *Player) Control() float64 { return p.control }

// TouchCoefficient returns the player's current -1..1 touch-quality coefficient this tick,
// derived from the team possession charge: positive (its team owns the charge) is a cleaner
// touch -- higher capture, lower bounce; negative (the other team owns it) is a worse touch
// so the ball flies off; 0 is the baseline. Exposed for the on-screen test bars.
func (p *Player) TouchCoefficient() float64 { return p.touchCoef }

// pullRadius is the surface gap within which the player can act on the ball with its
// centre-pull: the base PullRange extended by the trap's effective strength (`trapAura`, which
// swells then weakens as the trap is over-held), so a trap reaches further at its peak and the
// reach shrinks back as it fades. Shared by the dribble attraction and the possession-steal contest.
func (p *Player) pullRadius() float64 {
	return p.Stats.PullRange + p.Stats.TrapRangeBonus*p.trapAura
}

// NormShootCharge maps held seconds to a 0..1 charge fraction.
func NormShootCharge(seconds float64) float64 {
	t := seconds / shootChargeMax
	if t < 0 {
		return 0
	}
	if t > 1 {
		return 1
	}
	return t
}

// NewPlayer creates a player from a stats preset.
func NewPlayer(id int, position geom.Vec, stats PlayerStats, team *Team) *Player {
	body := physics.NewCircleBody(position, stats.Radius, stats.Friction, stats.Mass)
	body.MaxSpeed = stats.MaxSpeed
	return &Player{
		Body:         body,
		PlayerID:     id,
		Team:         team,
		Stats:        stats,
		Facing:       geom.NewVec(1, 0),
		HomePosition: position,
	}
}

// Move sets the player's acceleration from a movement intent: direction is the
// desired heading (normalised here) and throttle in [0,1] scales the player's
// acceleration. Humans, AI, and network input all call this identically; maxSpeed
// and acceleration come from the player's stats rather than hard-coded constants.
// Integration (velocity, speed cap, friction, position) happens in the body's
// Update during Match.Step.
func (p *Player) Move(direction geom.Vec, throttle, deltaTime float64) {
	if throttle < 0 {
		throttle = 0
	} else if throttle > 1 {
		throttle = 1
	}
	p.Acceleration = geom.NewVec(0, 0)
	length := geom.Norm(direction)
	if length == 0 {
		return
	}
	desired := direction.Scale(1 / length)
	// Rate-limit how fast the movement heading can swing, so a player cannot redirect
	// instantly -- a hard reverse curves around instead of snapping. A fresh heading (or
	// no turn limit) snaps straight to the input.
	if p.Stats.TurnRate <= 0 || geom.Norm(p.moveHeading) == 0 {
		p.moveHeading = desired
	} else {
		p.moveHeading = rotateToward(p.moveHeading, desired, p.Stats.TurnRate*deltaTime)
	}
	p.Acceleration = p.moveHeading.Scale(p.Stats.Acceleration * throttle)
}

// rotateToward rotates the unit vector from toward the unit vector to by at most maxRad
// radians, snapping to to once within range. It picks the shorter direction via the
// 2D cross product's sign.
func rotateToward(from, to geom.Vec, maxRad float64) geom.Vec {
	dot := geom.Dot(from, to)
	if dot > 1 {
		dot = 1
	} else if dot < -1 {
		dot = -1
	}
	if angle := math.Acos(dot); angle <= maxRad {
		return to
	}
	step := maxRad
	if from.X*to.Y-from.Y*to.X < 0 {
		step = -maxRad
	}
	return from.Rotate(step, geom.Vec{})
}

// FaceTowards points the player instantly toward the given point. Used for the AI (whose aim is
// smoothed on-ball and rate-limited off-ball in the control layer) and the network path.
func (p *Player) FaceTowards(point geom.Vec) {
	direction := point.Sub(p.Position)
	if length := geom.Norm(direction); length > 0 {
		p.Facing = direction.Scale(1 / length)
	}
}

// faceTowardLimited rotates the facing toward point at up to TurnRate radians/sec, so a human's
// cursor aim turns at a limited rate instead of snapping (the disk can't instantly re-orient).
// With TurnRate 0 or no current facing yet it snaps.
func (p *Player) faceTowardLimited(point geom.Vec, deltaTime float64) {
	direction := point.Sub(p.Position)
	length := geom.Norm(direction)
	if length == 0 {
		return
	}
	desired := direction.Scale(1 / length)
	if p.Stats.TurnRate <= 0 || p.Facing == (geom.Vec{}) {
		p.Facing = desired
		return
	}
	p.Facing = rotateToward(p.Facing, desired, p.Stats.TurnRate*deltaTime)
}

// Obstacle is a fixed, immovable shape (such as a cone) that the ball and players
// bounce off but never move. No mode places obstacles today, but the capability is kept
// so a field can add them.
type Obstacle struct {
	*physics.Body
}

// NewConeObstacle creates a static circular obstacle.
func NewConeObstacle(position geom.Vec, radius float64) *Obstacle {
	return &Obstacle{physics.NewStaticCircle(position, radius)}
}
