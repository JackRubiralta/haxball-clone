package physics

import "phootball/internal/geom"

// Collide resolves a generic collision between two bodies using the given
// restitution (0 = perfectly inelastic; higher values bounce). It is
// shape-polymorphic: it dispatches on the bodies' shapes and is gameplay-agnostic.
// It returns true if the pair was in contact.
//
// Both the positional correction and the impulse are weighted by inverse mass, so a
// static body (InvMass 0) is never displaced and absorbs no impulse, while two equal
// masses split the correction evenly.
func Collide(a, b *Body, restitution float64) bool {
	switch {
	case a.Shape.kind() == kindCircle && b.Shape.kind() == kindCircle:
		return resolveCircleCircle(a, b, restitution)
	case a.Shape.kind() == kindCircle && b.Shape.kind() == kindSegment:
		return resolveCircleSegment(a, b, restitution)
	case a.Shape.kind() == kindSegment && b.Shape.kind() == kindCircle:
		return resolveCircleSegment(b, a, restitution)
	default:
		return false
	}
}

// Resolve is the perfectly inelastic (restitution 0) collision used for
// player-vs-player contact.
func Resolve(a, b *Body) bool {
	return Collide(a, b, 0)
}

func resolveCircleCircle(a, b *Body, restitution float64) bool {
	normal := b.Position.Sub(a.Position)
	distance := geom.Norm(normal)
	if distance == 0 {
		return false // sharing a centre; no sensible normal
	}
	overlap := (a.Radius() + b.Radius()) - distance
	if overlap <= 0 {
		return false
	}
	normal = normal.Scale(1 / distance) // unit, pointing from a to b

	invSum := a.InvMass + b.InvMass
	if invSum == 0 {
		return true // both immovable
	}

	// Mass-weighted positional correction (equal masses => overlap/2 each).
	a.Position = a.Position.Sub(normal.Scale(overlap * a.InvMass / invSum))
	b.Position = b.Position.Add(normal.Scale(overlap * b.InvMass / invSum))

	relativeVelocity := b.Velocity.Sub(a.Velocity)
	velocityAlongNormal := geom.Dot(relativeVelocity, normal)
	if velocityAlongNormal > 0 {
		return true // separating already
	}

	impulseScalar := -(1 + restitution) * velocityAlongNormal
	impulseScalar /= invSum
	impulse := normal.Scale(impulseScalar)

	a.Velocity = a.Velocity.Sub(impulse.Scale(a.InvMass))
	b.Velocity = b.Velocity.Add(impulse.Scale(b.InvMass))
	return true
}

func resolveCircleSegment(c, s *Body, restitution float64) bool {
	seg, ok := s.Shape.(Segment)
	if !ok {
		return false
	}
	a := s.Position.Add(seg.A)
	b := s.Position.Add(seg.B)
	closest := closestPointOnSegment(c.Position, a, b)

	delta := c.Position.Sub(closest)
	distance := geom.Norm(delta)
	r := c.Radius()
	if distance == 0 || distance >= r {
		return false
	}
	normal := delta.Scale(1 / distance) // points from the segment to the circle
	penetration := r - distance

	invSum := c.InvMass + s.InvMass
	if invSum == 0 {
		return true
	}
	c.Position = c.Position.Add(normal.Scale(penetration * c.InvMass / invSum))
	s.Position = s.Position.Sub(normal.Scale(penetration * s.InvMass / invSum))

	relativeVelocity := c.Velocity.Sub(s.Velocity)
	velocityAlongNormal := geom.Dot(relativeVelocity, normal)
	if velocityAlongNormal > 0 {
		return true
	}
	impulseScalar := -(1 + restitution) * velocityAlongNormal / invSum
	impulse := normal.Scale(impulseScalar)
	c.Velocity = c.Velocity.Add(impulse.Scale(c.InvMass))
	s.Velocity = s.Velocity.Sub(impulse.Scale(s.InvMass))
	return true
}

// closestPointOnSegment returns the point on segment ab nearest to p.
func closestPointOnSegment(p, a, b geom.Vec) geom.Vec {
	ab := b.Sub(a)
	denom := geom.Dot(ab, ab)
	if denom == 0 {
		return a // degenerate segment
	}
	t := geom.Dot(p.Sub(a), ab) / denom
	if t < 0 {
		t = 0
	} else if t > 1 {
		t = 1
	}
	return a.Add(ab.Scale(t))
}

// ReflectInside reflects a circular body off the inside faces of an axis-aligned
// rectangle: the body is clamped to the wall and the velocity component normal to
// that wall is flipped. X and Y are handled independently in one pass.
func ReflectInside(b *Body, minX, minY, maxX, maxY float64) {
	r := b.Radius()
	if b.Position.X-r < minX {
		b.Position.X = minX + r
		b.Velocity.X = -b.Velocity.X
	} else if b.Position.X+r > maxX {
		b.Position.X = maxX - r
		b.Velocity.X = -b.Velocity.X
	}
	if b.Position.Y-r < minY {
		b.Position.Y = minY + r
		b.Velocity.Y = -b.Velocity.Y
	} else if b.Position.Y+r > maxY {
		b.Position.Y = maxY - r
		b.Velocity.Y = -b.Velocity.Y
	}
}

// ClampInside keeps a circular body inside an axis-aligned rectangle, stopping the
// velocity component normal to any wall it hits.
func ClampInside(b *Body, minX, minY, maxX, maxY float64) {
	r := b.Radius()
	if b.Position.X-r < minX {
		b.Position.X = minX + r
		b.Velocity.X = 0
	} else if b.Position.X+r > maxX {
		b.Position.X = maxX - r
		b.Velocity.X = 0
	}
	if b.Position.Y-r < minY {
		b.Position.Y = minY + r
		b.Velocity.Y = 0
	} else if b.Position.Y+r > maxY {
		b.Position.Y = maxY - r
		b.Velocity.Y = 0
	}
}
