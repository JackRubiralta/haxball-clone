package sim

import (
	"math"

	"phootball/internal/config"
	"phootball/internal/geom"
	"phootball/internal/physics"
)

// Ball is the game ball. It has a single mass (the Body's), used when resolving
// collisions -- including the contact with a player, where a heavier ball takes less of
// the impulse and so is harder to launch by a bump.
type Ball struct {
	*physics.Body
}

// NewBall creates the ball at position with the given radius and the default ball physics
// (friction/mass sourced from config.DefaultTuning, the single source of truth). For a real
// match Match.applyConfig then stamps the configured radius/friction/mass over these, so a
// custom Tuning still wins; the defaults are byte-equal to those config values.
func NewBall(position geom.Vec, radius float64) *Ball {
	t := config.DefaultTuning()
	return &Ball{physics.NewCircleBody(position, radius, t.BallFriction, t.BallMass)}
}

// Player is a controllable circle with per-player tuning, a facing direction, and a
// role. Its body's motion is integrated by the simulation; its tuning drives the
// dribble, shooting, bounce, and speed.
type Player struct {
	*physics.Body
	PlayerID     int
	Number       int // jersey number shown on the player
	Team         *Team
	Role         Role
	Tuning       config.PlayerTuning
	Facing       geom.Vec
	HomePosition geom.Vec
	WantsKick    bool

	possession    float64  // 0..1 build-up while the ball is touching anywhere; scales the grip on the ball
	pullEnterSeq  uint64   // sequence stamp set when the ball entered this player's pull radius (0 = out of reach); the latest stamp is the sole possession builder
	touchCoef     float64  // -1..1 touch-quality coefficient this tick from the team possession charge
	boostDrain    float64  // 0..1 per-player erosion of THIS player's team boost while an opponent is touching it (recovers off contact)
	shootCharge   float64  // seconds the shoot button has been held this charge
	trapCharge    float64  // 0..1 trap ENERGY bar (the resource): drains while the trap is held, regenerates at ~1/3 rate otherwise. (Name kept for wire/getter/NN stability; it is energy, not a charge.)
	trapAura      float64  // 0..1 EFFECTIVE trap strength (stateful): rises to an energy-limited peak while held, holds, then shrinks to 0 once the bar drains out or on release; drives BOTH the trap effect and its visual aura
	trapPeak      float64  // the peak trapAura rises to this press; captured on the trap rising edge = the energy available then (so a half-full bar gives a smaller, "not fully big" peak)
	shootHeldPrev bool     // shoot-button state last tick, for release-edge detection
	shootCanceled bool     // current shoot charge was canceled (by a trap-tap); suppress the release-edge kick
	wantsPush     bool     // middle-click jab requested this tick (instant min-power radial push)
	pushHeldPrev  bool     // push-button state last tick, for the jab's rising-edge detection (idempotent over the network)
	pushFlash     float64  // 1->0 cosmetic timer set whenever a middle-click push is ATTEMPTED (even a whiff with no ball in reach); drives the push pulse animation over the player
	trapHeldPrev  bool     // trap-button state last tick, for the trap sound's rising edge
	evictDwell    float64  // seconds spent violating a positional rule (warn-evict grace)
	moveHeading   geom.Vec // current steering direction; rotates toward input at TurnRate
	heldOrbital   float64  // signed orbital speed (CCW+, relative-to-player tangential) THIS player's hold forces are responsible for; the dribble anti-fling/damping act only on this, so a turning carrier holds the ball while a stray ball's own incoming momentum is not arrested (reset to 0 whenever the ball is not touching)
}

// Charge timing constants (seconds), shared by the sim and the renderer's gauges.
const (
	shootChargeMax   = 0.75 // seconds of hold for a full-power shot (faster charge)
	pushFlashSeconds = 0.4  // seconds for the middle-click push ring to expand outward and fade away
)

// trapAuraApproach moves the trap aura toward target at a constant (LINEAR) rate -- the SAME rate
// whether growing or shrinking, so the fade is exactly as gradual as the come-up (no fast initial
// drop when the bar runs out). Snaps to the target once within a step.
func trapAuraApproach(cur, target, rate, dt float64) float64 {
	step := rate * dt
	switch {
	case target > cur:
		if cur += step; cur > target {
			cur = target
		}
	case target < cur:
		if cur -= step; cur < target {
			cur = target
		}
	}
	return cur
}

// ShootCharge returns the seconds the shoot button has been held this charge.
func (p *Player) ShootCharge() float64 { return p.shootCharge }

// TrapCharge returns the current 0..1 trap ENERGY (the resource bar): 1 = full, draining while the
// trap is held and regenerating at ~1/3 the drain rate otherwise. (Named "Charge" for wire/getter
// stability; it is the energy bar.)
func (p *Player) TrapCharge() float64 { return p.trapCharge }

// TrapAura returns the current 0..1 effective trap strength: while the trap is held it rises to a
// peak bounded by the energy available when pressed, holds there, then shrinks to 0 once the energy
// bar drains out (or on release). Drives the trap effect and the renderer's trap glow (so they match).
func (p *Player) TrapAura() float64 { return p.trapAura }

// PushFlash returns the current 1->0 push-press animation timer: 1 the tick a middle-click push is
// pressed (whether or not it connects with the ball), fading to 0 over pushFlashSeconds. Exposed for
// the renderer's push pulse (local play).
func (p *Player) PushFlash() float64 { return p.pushFlash }

// PushRange returns the surface-gap reach of the middle-click push -- the BASE PullRange, NOT the
// trap-extended pullRadius (the push fires on any ball whose surface gap is below this; see push).
// Exposed so the renderer can size and gate the push-pulse animation to the push's actual reach.
func (p *Player) PushRange() float64 { return p.Tuning.PullRange }

// Possession returns the player's current 0..1 possession build-up (ball touching anywhere).
func (p *Player) Possession() float64 { return p.possession }

// TouchCoefficient returns the player's current -1..1 touch-quality coefficient this tick,
// derived from the team possession charge: positive (its team owns the charge) is a cleaner
// touch -- higher capture, lower bounce; negative (the other team owns it) is a worse touch
// so the ball flies off; 0 is the baseline. Exposed for the on-screen test bars.
func (p *Player) TouchCoefficient() float64 { return p.touchCoef }

// pullRadius is the surface gap within which the player can act on the ball with its
// centre-pull: the base PullRange extended by the trap's effective strength (`trapAura`, which
// swells then weakens as the trap is over-held), so a trap reaches further at its peak and the
// reach shrinks back as it fades. Used ONLY by the dribble attraction (handleBallToPlayerInteraction);
// the possession contest deliberately uses the BASE PullRange instead (see Match.inPullRange),
// so a trap extends the ball pull but NOT who builds/contests possession.
func (p *Player) pullRadius() float64 {
	return p.Tuning.PullRange + p.Tuning.TrapRangeBonus*p.trapAura
}

// possessionReach is the surface gap within which the player contests POSSESSION (the reach
// behind Match.inPullRange / playerReach). Unlike pullRadius it is NEVER trap-extended -- a held
// trap pulls the ball in from further but must not widen who owns/steals possession. It is the
// player's PossessionRange, falling back to the base PullRange when PossessionRange is unset
// (<= 0), so possession reach equals the attraction base by default yet can be tuned on its own.
func (p *Player) possessionReach() float64 {
	if p.Tuning.PossessionRange > 0 {
		return p.Tuning.PossessionRange
	}
	return p.Tuning.PullRange
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

// NewPlayer creates a player from a tuning preset.
func NewPlayer(id int, position geom.Vec, tuning config.PlayerTuning, team *Team) *Player {
	body := physics.NewCircleBody(position, tuning.Radius, tuning.Friction, tuning.Mass)
	body.MaxSpeed = tuning.MaxSpeed
	return &Player{
		Body:         body,
		PlayerID:     id,
		Team:         team,
		Tuning:       tuning,
		Facing:       geom.NewVec(1, 0),
		HomePosition: position,
		trapCharge:   1, // start with a full trap energy bar
	}
}

// SetTuning replaces the player's tuning and re-syncs the body fields DERIVED from it
// (friction, mass->InvMass, radius, max speed), so a re-stamp of the match's configured
// tuning (see Match.applyConfig) actually reaches the physics body. Mirrors how applyConfig
// restamps the ball. Mass is guarded against a zero divide.
func (p *Player) SetTuning(t config.PlayerTuning) {
	p.Tuning = t
	p.Body.Friction = t.Friction
	if t.Mass > 0 {
		p.Body.InvMass = 1 / t.Mass
	}
	p.Body.SetRadius(t.Radius)
	p.Body.MaxSpeed = t.MaxSpeed
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
	if p.Tuning.TurnRate <= 0 || geom.Norm(p.moveHeading) == 0 {
		p.moveHeading = desired
	} else {
		p.moveHeading = rotateToward(p.moveHeading, desired, p.Tuning.TurnRate*deltaTime)
	}
	p.Acceleration = p.moveHeading.Scale(p.Tuning.Acceleration * throttle)
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

// FaceTowards points the player instantly toward the given point. Used for the AI, whose aim is
// rate-limited in the control layer (smoothed on-ball by aimKeepingBall, throttled off-ball and
// for the keeper by AI.capAim) so it still cannot turn faster than a human despite this snap.
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
	if p.Tuning.TurnRate <= 0 || p.Facing == (geom.Vec{}) {
		p.Facing = desired
		return
	}
	p.Facing = rotateToward(p.Facing, desired, p.Tuning.TurnRate*deltaTime)
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
