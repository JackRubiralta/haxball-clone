// Package geom provides the 2D vector type shared by the physics, simulation, and
// rendering layers. It depends only on the standard library so it can be linked
// into a headless server as easily as into a graphical client.
package geom

import (
	"fmt"
	"math"
)

// Vec is a 2D vector.
type Vec struct {
	X, Y float64
}

// NewVec creates a new vector.
func NewVec(x, y float64) Vec {
	return Vec{x, y}
}

// Add returns the sum of two vectors.
func (v Vec) Add(other Vec) Vec {
	return Vec{v.X + other.X, v.Y + other.Y}
}

// Sub returns the difference of two vectors.
func (v Vec) Sub(other Vec) Vec {
	return Vec{v.X - other.X, v.Y - other.Y}
}

// Scale returns the vector scaled by a scalar.
func (v Vec) Scale(s float64) Vec {
	return Vec{v.X * s, v.Y * s}
}

// Hadamard returns the component-wise product of two vectors.
func (v Vec) Hadamard(other Vec) Vec {
	return Vec{v.X * other.X, v.Y * other.Y}
}

// DivScale returns the vector divided by a scalar.
func (v Vec) DivScale(s float64) Vec {
	return Vec{v.X / s, v.Y / s}
}

// String returns the string representation of the vector.
func (v Vec) String() string {
	return fmt.Sprintf("<x: %.2f, y: %.2f>", v.X, v.Y)
}

// Dist returns the distance between two vectors.
func Dist(a, b Vec) float64 {
	return math.Sqrt(math.Pow(a.X-b.X, 2) + math.Pow(a.Y-b.Y, 2))
}

// Norm returns the magnitude of the vector.
func Norm(v Vec) float64 {
	return math.Sqrt(v.X*v.X + v.Y*v.Y)
}

// Dot returns the dot product of two vectors.
func Dot(a, b Vec) float64 {
	return a.X*b.X + a.Y*b.Y
}

// Rotate returns the vector rotated around a point by the given radians.
func (v Vec) Rotate(radians float64, around Vec) Vec {
	x := (v.X-around.X)*math.Cos(radians) - (v.Y-around.Y)*math.Sin(radians)
	y := (v.X-around.X)*math.Sin(radians) + (v.Y-around.Y)*math.Cos(radians)
	return Vec{x + around.X, y + around.Y}
}

// Unit returns v scaled to length 1, or the zero vector if v is (near) zero. It is the
// shared direction primitive: a heading, an aim, or a normal is "which way", and dividing
// by the magnitude is how you ask for that without the length getting in the way.
func Unit(v Vec) Vec {
	n := Norm(v)
	if n < 1e-9 {
		return Vec{}
	}
	return v.Scale(1 / n)
}

// Angle returns the absolute heading of v in radians (atan2(y, x)), in (-pi, pi].
func Angle(v Vec) float64 {
	return math.Atan2(v.Y, v.X)
}

// Cross returns the 2D cross product a x b (a.X*b.Y - a.Y*b.X). Its sign tells you which
// way b sits relative to a (positive = b is counter-clockwise from a), which is how a
// turn picks the shorter arc.
func Cross(a, b Vec) float64 {
	return a.X*b.Y - a.Y*b.X
}

// AngleBetween returns the unsigned angle in radians between two vectors (0..pi). It is
// the magnitude of the turn from a to b regardless of direction -- e.g. how far off a
// player's facing is from the ball.
func AngleBetween(a, b Vec) float64 {
	na, nb := Norm(a), Norm(b)
	if na < 1e-9 || nb < 1e-9 {
		return 0
	}
	c := Dot(a, b) / (na * nb)
	if c < -1 {
		c = -1
	} else if c > 1 {
		c = 1
	}
	return math.Acos(c)
}

// SignedAngle returns the angle in radians to turn from a to b, in (-pi, pi]: positive is
// counter-clockwise, negative clockwise. It answers "which way and how far" in one number.
func SignedAngle(a, b Vec) float64 {
	return math.Atan2(Cross(a, b), Dot(a, b))
}
