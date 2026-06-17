package control

import (
	"phootball/internal/geom"
)

// Small vector helpers used throughout the AI. They live here (rather than in geom) so
// the shared geometry package stays minimal; everything here is a thin convenience over
// geom's primitives. Direction/angle primitives (unit, angle-between) now live in geom
// itself, shared with the simulation, so this file only keeps AI-specific helpers.

// perp returns a vector perpendicular to v (rotated a quarter-turn, pi/2), preserving length.
func perp(v geom.Vec) geom.Vec { return geom.NewVec(-v.Y, v.X) }

// clampFloat constrains v to [lo, hi].
func clampFloat(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// clampVec constrains a point to the axis-aligned box [min, max].
func clampVec(p, min, max geom.Vec) geom.Vec {
	return geom.NewVec(clampFloat(p.X, min.X, max.X), clampFloat(p.Y, min.Y, max.Y))
}

// segPointDist returns the distance from point p to the segment ab.
func segPointDist(p, a, b geom.Vec) float64 {
	ab := b.Sub(a)
	len2 := geom.Dot(ab, ab)
	if len2 < 1e-9 {
		return geom.Dist(p, a)
	}
	t := clampFloat(geom.Dot(p.Sub(a), ab)/len2, 0, 1)
	proj := a.Add(ab.Scale(t))
	return geom.Dist(p, proj)
}

// lerp linearly interpolates between a and b by t (t is not clamped).
func lerp(a, b, t float64) float64 { return a + (b-a)*t }

// smoothstep maps x in [edge0, edge1] to a smooth 0..1 ramp (flat outside the range).
func smoothstep(edge0, edge1, x float64) float64 {
	if edge0 == edge1 {
		if x < edge0 {
			return 0
		}
		return 1
	}
	t := clampFloat((x-edge0)/(edge1-edge0), 0, 1)
	return t * t * (3 - 2*t)
}

// rotateToward rotates the unit direction from toward to by at most maxAngle radians, so a
// heading can't whip around in a single step (a sharp turn flings the ball off the front).
func rotateToward(from, to geom.Vec, maxAngle float64) geom.Vec {
	from, to = geom.Unit(from), geom.Unit(to)
	if from == (geom.Vec{}) {
		return to
	}
	if to == (geom.Vec{}) {
		return from
	}
	ang := geom.AngleBetween(from, to)
	if ang <= maxAngle {
		return to
	}
	// Rotate by maxAngle in the direction of `to` (sign from the 2D cross product).
	sign := 1.0
	if geom.Cross(from, to) < 0 {
		sign = -1
	}
	return from.Rotate(sign*maxAngle, geom.Vec{})
}
