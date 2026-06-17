package sim

import (
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

	possession    float64 // 0..1 possession build-up; scales the grip on the ball
	shootCharge   float64 // seconds the shoot button has been held this charge
	trapCharge    float64 // 0..1 trap charge; built while the trap button is held
	shootHeldPrev bool    // shoot-button state last tick, for release-edge detection
	trapHeldPrev  bool    // trap-button state last tick, for the trap sound's rising edge
	evictDwell    float64 // seconds spent violating a positional rule (warn-evict grace)
}

// Charge timing constants (seconds), shared by the sim and the renderer's gauges.
const (
	shootChargeMax  = 1.0 // seconds of hold for a full-power shot
	trapChargeTime  = 1.0 // seconds of holding the trap button to reach full trap charge
	trapChargeDecay = 4.0 // per-second decay of trap charge once the button is released
)

// ShootCharge returns the seconds the shoot button has been held this charge.
func (p *Player) ShootCharge() float64 { return p.shootCharge }

// TrapCharge returns the current 0..1 trap charge.
func (p *Player) TrapCharge() float64 { return p.trapCharge }

// Possession returns the player's current 0..1 possession build-up.
func (p *Player) Possession() float64 { return p.possession }

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
	if length := geom.Norm(direction); length > 0 {
		unit := direction.Scale(1 / length)
		p.Acceleration = unit.Scale(p.Stats.Acceleration * throttle)
	}
}

// FaceTowards points the player toward the given point (the cursor for a human, the
// ball or a goal for the AI).
func (p *Player) FaceTowards(point geom.Vec) {
	direction := point.Sub(p.Position)
	if length := geom.Norm(direction); length > 0 {
		p.Facing = direction.Scale(1 / length)
	}
}

// Obstacle is a fixed, immovable shape (such as a cone) that the ball and players
// bounce off but never move.
type Obstacle struct {
	*physics.Body
}

// NewConeObstacle creates a static circular obstacle.
func NewConeObstacle(position geom.Vec, radius float64) *Obstacle {
	return &Obstacle{physics.NewStaticCircle(position, radius)}
}
