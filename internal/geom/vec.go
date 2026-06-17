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
