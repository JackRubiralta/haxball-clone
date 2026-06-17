// Package physics is the headless, deterministic rigid-body engine. It depends only
// on the standard library and internal/geom -- never on Ebiten -- so the exact same
// collision code runs in the graphical client and in the authoritative server.
package physics

import (
	"math"

	"phootball/internal/geom"
)

// AABB is an axis-aligned bounding box used for the broad phase.
type AABB struct {
	Min, Max geom.Vec
}

// Overlaps reports whether two boxes intersect.
func (a AABB) Overlaps(b AABB) bool {
	return a.Min.X <= b.Max.X && a.Max.X >= b.Min.X &&
		a.Min.Y <= b.Max.Y && a.Max.Y >= b.Min.Y
}

type shapeKind int

const (
	kindCircle shapeKind = iota
	kindSegment
)

// Shape is the collision geometry attached to a Body. It is the polymorphism that
// lets the engine grow new collidables: a fixed cone is a static Circle, an arena
// wall or goal post is a Segment. The kind method keeps the set closed so the
// resolver can dispatch without reflection.
type Shape interface {
	// Bounds returns the world-space bounding box of the shape centred at center.
	Bounds(center geom.Vec) AABB
	kind() shapeKind
}

// Circle is a circular shape of the given radius.
type Circle struct {
	Radius float64
}

func (c Circle) kind() shapeKind { return kindCircle }

// Bounds returns the circle's world-space bounding box.
func (c Circle) Bounds(center geom.Vec) AABB {
	r := geom.NewVec(c.Radius, c.Radius)
	return AABB{Min: center.Sub(r), Max: center.Add(r)}
}

// Segment is a line segment whose endpoints are stored relative to the body's
// position, so a static wall keeps its body position at the origin with A and B in
// world space, while a moving segment could translate with its body.
type Segment struct {
	A, B geom.Vec
}

func (s Segment) kind() shapeKind { return kindSegment }

// Bounds returns the segment's world-space bounding box.
func (s Segment) Bounds(center geom.Vec) AABB {
	a, b := center.Add(s.A), center.Add(s.B)
	return AABB{
		Min: geom.NewVec(math.Min(a.X, b.X), math.Min(a.Y, b.Y)),
		Max: geom.NewVec(math.Max(a.X, b.X), math.Max(a.Y, b.Y)),
	}
}
