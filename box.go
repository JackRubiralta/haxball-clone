package main

import (
	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/vector"
	"image/color"
)

type Box struct {
	Position    Vec
	Width       float64
	Height      float64
	GoalWidth   float64
	GoalHeight  float64
}

func NewBox(x, y, width, height, goalWidth, goalHeight float64) *Box {
	return &Box{
		Position:   NewVec(x, y),
		Width:      width,
		Height:     height,
		GoalWidth:  goalWidth,
		GoalHeight: goalHeight,
	}
}

// Render draws the box on the screen with goals and improved visuals.
func (b *Box) Render(screen *ebiten.Image) {
	// Draw grass
	vector.DrawFilledRect(screen, float32(b.Position.X), float32(b.Position.Y), float32(b.Width), float32(b.Height), color.RGBA{34, 139, 34, 255}, true)

	// Draw white lines
	lineColor := color.RGBA{255, 255, 255, 255}
	lineWidth := float32(5)
	vector.StrokeLine(screen, float32(b.Position.X), float32(b.Position.Y), float32(b.Position.X+b.Width), float32(b.Position.Y), lineWidth, lineColor, true)
	vector.StrokeLine(screen, float32(b.Position.X), float32(b.Position.Y), float32(b.Position.X), float32(b.Position.Y+b.Height), lineWidth, lineColor, true)
	vector.StrokeLine(screen, float32(b.Position.X+b.Width), float32(b.Position.Y), float32(b.Position.X+b.Width), float32(b.Position.Y+b.Height), lineWidth, lineColor, true)
	vector.StrokeLine(screen, float32(b.Position.X), float32(b.Position.Y+b.Height), float32(b.Position.X+b.Width), float32(b.Position.Y+b.Height), lineWidth, lineColor, true)

	// Draw goals
	goalColor := color.RGBA{0, 0, 0, 255}
	vector.DrawFilledRect(screen, float32(b.Position.X)-float32(b.GoalWidth), float32(b.Position.Y)+(float32(b.Height)-float32(b.GoalHeight))/2, float32(b.GoalWidth), float32(b.GoalHeight), goalColor, true)
	vector.DrawFilledRect(screen, float32(b.Position.X+b.Width), float32(b.Position.Y)+(float32(b.Height)-float32(b.GoalHeight))/2, float32(b.GoalWidth), float32(b.GoalHeight), goalColor, true)
}

// CheckGoal checks if the ball is in the goal
func (b *Box) CheckGoal(ball *Ball) int {
	if ball.Position.X-ball.Radius < b.Position.X-b.GoalWidth && ball.Position.Y > b.Position.Y+(b.Height-b.GoalHeight)/2 && ball.Position.Y < b.Position.Y+(b.Height+b.GoalHeight)/2 {
		return 1 // Left goal
	}
	if ball.Position.X+ball.Radius > b.Position.X+b.Width+b.GoalWidth && ball.Position.Y > b.Position.Y+(b.Height-b.GoalHeight)/2 && ball.Position.Y < b.Position.Y+(b.Height+b.GoalHeight)/2 {
		return 2 // Right goal
	}
	return 0 // No goal
}
