package main

import (
	"image/color"

	"github.com/hajimehoshi/ebiten/v2"
)

// Ball represents a ball that extends Disc.
type Ball struct {
	*Disc
}

// NewBall creates a new ball.
func NewBall(position Vec, radius float64) *Ball {
	return &Ball{NewDisc(position, radius, -0.3)}
}

// Draw draws the ball on the screen with a black outline and white fill.
func (b *Ball) Draw(screen *ebiten.Image) {
	b.Disc.Draw(screen, color.White)
}
