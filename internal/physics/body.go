package physics

import "phootball/internal/geom"

// Body is the dynamic rigid-body state shared by every collidable in the game
// (players, the ball, obstacles). Motion lives here; geometry is delegated to Shape.
//
// A Body with InvMass == 0 is immovable (infinite mass): it never accelerates and a
// collision never displaces it. That single rule expresses both arena walls and
// fixed obstacles such as a cone.
//
// MaxSpeed, when > 0, caps the body's speed each integration step (0 == unlimited):
// the ball is uncapped, a player caps to its stats.
type Body struct {
	Position     geom.Vec
	Velocity     geom.Vec
	Acceleration geom.Vec
	Friction     float64
	InvMass      float64
	MaxSpeed     float64
	Shape        Shape
}

// NewCircleBody creates a circular dynamic body of the given mass.
func NewCircleBody(position geom.Vec, radius, friction, mass float64) *Body {
	return &Body{
		Position: position,
		Friction: friction,
		InvMass:  1 / mass,
		Shape:    Circle{Radius: radius},
	}
}

// NewStaticCircle creates an immovable circular body (infinite mass), such as a
// fixed obstacle.
func NewStaticCircle(position geom.Vec, radius float64) *Body {
	return &Body{
		Position: position,
		InvMass:  0,
		Shape:    Circle{Radius: radius},
	}
}

// NewStaticSegment creates an immovable line-segment body, such as a wall.
func NewStaticSegment(a, b geom.Vec) *Body {
	return &Body{
		InvMass: 0,
		Shape:   Segment{A: a, B: b},
	}
}

// Static reports whether the body is immovable.
func (b *Body) Static() bool { return b.InvMass == 0 }

// Radius returns the body's radius if it is a circle, or 0 otherwise.
func (b *Body) Radius() float64 {
	if c, ok := b.Shape.(Circle); ok {
		return c.Radius
	}
	return 0
}

// SetRadius sets a circular body's radius (a no-op for non-circle shapes). Used to
// grow a player slightly while it is trapping the ball.
func (b *Body) SetRadius(r float64) {
	if _, ok := b.Shape.(Circle); ok {
		b.Shape = Circle{Radius: r}
	}
}

// Mass returns the body's mass (the reciprocal of its inverse mass).
func (b *Body) Mass() float64 { return 1 / b.InvMass }

// Update integrates the body forward by deltaTime: acceleration, optional speed cap,
// friction, then position, in that order.
func (b *Body) Update(deltaTime float64) {
	prevSpeed := geom.Norm(b.Velocity)
	b.Velocity = b.Velocity.Add(b.Acceleration.Scale(deltaTime))
	if b.MaxSpeed > 0 {
		// Soft cap: acceleration may not push the body past MaxSpeed, but a knock can
		// exceed it -- don't snap it down; let friction bleed the excess off. So clamp
		// only to whichever is larger, MaxSpeed or the pre-acceleration speed.
		limit := b.MaxSpeed
		if prevSpeed > limit {
			limit = prevSpeed
		}
		if speed := geom.Norm(b.Velocity); speed > limit {
			b.Velocity = b.Velocity.Scale(limit / speed)
		}
	}
	frictionForce := b.Velocity.Scale(b.Friction)
	b.Velocity = b.Velocity.Add(frictionForce.Scale(deltaTime))
	b.Position = b.Position.Add(b.Velocity.Scale(deltaTime))
}

// Left returns the left edge of a circular body.
func (b *Body) Left() float64 { return b.Position.X - b.Radius() }

// Right returns the right edge of a circular body.
func (b *Body) Right() float64 { return b.Position.X + b.Radius() }

// Top returns the top edge of a circular body.
func (b *Body) Top() float64 { return b.Position.Y - b.Radius() }

// Bottom returns the bottom edge of a circular body.
func (b *Body) Bottom() float64 { return b.Position.Y + b.Radius() }
