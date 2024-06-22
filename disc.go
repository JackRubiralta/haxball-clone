package main

import (
	"image/color"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/vector"
)

// Disc represents a disc with a position, radius, velocity, and acceleration.
type Disc struct {
	Position     Vec
	Radius       float64
	Velocity     Vec
	Acceleration Vec
	Friction     float64
}

// NewDisc creates a new disc.
func NewDisc(position Vec, radius float64, friction float64) *Disc {
	return &Disc{
		Position:     position,
		Radius:       radius,
		Velocity:     NewVec(0, 0),
		Acceleration: NewVec(0, 0),
		Friction:     friction,
	}
}

// Update updates the disc's position based on its velocity and acceleration.
func (c *Disc) Update(deltaTime float64) {
	c.Velocity = c.Velocity.Add(c.Acceleration.Mul(deltaTime))
	frictionForce := c.Velocity.Mul(c.Friction)
	c.Velocity = c.Velocity.Add(frictionForce.Mul(deltaTime))
	c.Position = c.Position.Add(c.Velocity.Mul(deltaTime))
}

// Draw draws the disc on the screen.
func (c *Disc) Draw(screen *ebiten.Image, clr color.Color) {

	vector.DrawFilledCircle(screen, float32(c.Position.X), float32(c.Position.Y), float32(c.Radius), clr, true)
}

// Left returns the left edge of the disc.
func (c *Disc) Left() float64 {
	return c.Position.X - c.Radius
}

// Right returns the right edge of the disc.
func (c *Disc) Right() float64 {
	return c.Position.X + c.Radius
}

// Top returns the top edge of the disc.
func (c *Disc) Top() float64 {
	return c.Position.Y - c.Radius
}

// Bottom returns the bottom edge of the disc.
func (c *Disc) Bottom() float64 {
	return c.Position.Y + c.Radius
}
