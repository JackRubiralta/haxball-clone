// Package input holds the human controller. It is split out from the headless
// control package because it imports Ebiten to read the keyboard and mouse; keeping
// it separate lets the authoritative server link control (the AI) without pulling in
// any graphics dependency. *Human satisfies control.Controller structurally.
package input

import (
	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/inpututil"

	"phootball/internal/geom"
	"phootball/internal/render"
	"phootball/internal/sim"
)

// Human reads the keyboard and mouse and produces an Intent.
type Human struct{}

// NewHuman creates a keyboard-and-mouse controller.
func NewHuman() *Human { return &Human{} }

// Intent reads WASD into a movement direction, the cursor into an aim point, held
// left mouse into a charging shot (fired on release), and held right mouse into a trap.
// A right-click also cancels an in-progress shot charge (CancelCharge on its rising edge):
// you abort a mistaken shot and settle the ball with the same press.
func (h *Human) Intent(_ sim.View) sim.Intent {
	var move geom.Vec
	if ebiten.IsKeyPressed(ebiten.KeyW) {
		move.Y -= 1
	}
	if ebiten.IsKeyPressed(ebiten.KeyS) {
		move.Y += 1
	}
	if ebiten.IsKeyPressed(ebiten.KeyA) {
		move.X -= 1
	}
	if ebiten.IsKeyPressed(ebiten.KeyD) {
		move.X += 1
	}

	throttle := 0.0
	if move.X != 0 || move.Y != 0 {
		throttle = 1
	}

	cursorX, cursorY := ebiten.CursorPosition()
	return sim.Intent{
		Move:         move,
		Throttle:     throttle,
		Aim:          render.ScreenToWorld(cursorX, cursorY),
		ShootHeld:    ebiten.IsMouseButtonPressed(ebiten.MouseButtonLeft),
		Trap:         ebiten.IsMouseButtonPressed(ebiten.MouseButtonRight),
		CancelCharge: inpututil.IsMouseButtonJustPressed(ebiten.MouseButtonRight),
	}
}
