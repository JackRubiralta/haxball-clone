// vec.go
package main

import (
	"fmt"
	"math"
)

type Vec struct {
	X, Y float64
}

// NewVec creates a new vector
func NewVec(x, y float64) Vec {
	return Vec{x, y}
}

// Add adds two vectors
func (v Vec) Add(other Vec) Vec {
	return Vec{v.X + other.X, v.Y + other.Y}
}

// Sub subtracts two vectors
func (v Vec) Sub(other Vec) Vec {
	return Vec{v.X - other.X, v.Y - other.Y}
}

// Mul multiplies the vector by another vector or a scalar
func (v Vec) Mul(other interface{}) Vec {
	switch o := other.(type) {
	case Vec:
		return Vec{v.X * o.X, v.Y * o.Y}
	case float64:
		return Vec{v.X * o, v.Y * o}
	case int:
		return Vec{v.X * float64(o), v.Y * float64(o)}
	default:
		panic(fmt.Sprintf("Multiplication with type %T not supported", other))
	}
}

// RMul is called if 4 * v for instance
func (v Vec) RMul(other interface{}) Vec {
	return v.Mul(other)
}

// Div divides the vector by another vector or a scalar
func (v Vec) Div(other interface{}) Vec {
	switch o := other.(type) {
	case Vec:
		return Vec{v.X / o.X, v.Y / o.Y}
	case float64:
		return Vec{v.X / o, v.Y / o}
	case int:
		return Vec{v.X / float64(o), v.Y / float64(o)}
	default:
		panic(fmt.Sprintf("Division with type %T not supported", other))
	}
}

// String returns the string representation of the vector
func (v Vec) String() string {
	return fmt.Sprintf("<x: %.2f, y: %.2f>", v.X, v.Y)
}

// Dist calculates the distance between two vectors
func Dist(vec1, vec2 Vec) float64 {
	return math.Sqrt(math.Pow(vec1.X-vec2.X, 2) + math.Pow(vec1.Y-vec2.Y, 2))
}

// Norm calculates the magnitude of the vector
func Norm(v Vec) float64 {
	return math.Sqrt(v.X*v.X + v.Y*v.Y)
}

// Dot calculates the dot product of two vectors
func Dot(vec1, vec2 Vec) float64 {
	return vec1.X*vec2.X + vec1.Y*vec2.Y
}

// Rotate rotates the vector around another vector by the given radians
func (v *Vec) Rotate(radians float64, around Vec) Vec {
	newCx := (v.X-around.X)*math.Cos(radians) - (v.Y-around.Y)*math.Sin(radians)
	newCy := (v.X-around.X)*math.Sin(radians) + (v.Y-around.Y)*math.Cos(radians)
	v.X = newCx + around.X
	v.Y = newCy + around.Y
	return *v
}
