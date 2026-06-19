// Package input holds the human controller. It is split out from the headless
// control package because it imports Ebiten to read the keyboard and mouse; keeping
// it separate lets the authoritative server link control (the AI) without pulling in
// any graphics dependency. *Human satisfies control.Controller structurally.
package input

import (
	"github.com/hajimehoshi/ebiten/v2"

	"phootball/internal/geom"
	"phootball/internal/render"
	"phootball/internal/sim"
)

// Human reads the keyboard and mouse and produces an Intent. It holds the most recent
// frame's Viewport (set by the App via SetViewport) so it can map the cursor into world
// space without reading any render-package global.
type Human struct {
	vp render.Viewport
}

// NewHuman creates a keyboard-and-mouse controller.
func NewHuman() *Human { return &Human{} }

// SetViewport tells the controller which frame transform to invert the cursor with. The
// App calls it each frame before gathering intents, passing the viewport it last drew.
func (h *Human) SetViewport(vp render.Viewport) { h.vp = vp }

// Intent reads WASD into a movement direction, the cursor into an aim point, and the three
// mouse buttons into the ball abilities: held left = a charging shot (fired on release), held
// right = a trap, held middle = a push jab. A right-click also cancels an in-progress shot
// charge: you abort a mistaken shot and settle the ball with the same press.
//
// Every signal is reported as a LEVEL (the button's held state), and the sim reconstructs the
// edges authoritatively -- the shoot release from shootHeldPrev, the push jab from pushHeldPrev.
// This is what keeps all three abilities working over the network, where an intent is re-applied
// across server ticks: a one-frame "pulse" would be duplicated or dropped, but a held level is
// idempotent. (This is the same reason shoot/trap already worked networked and push did not.)
//
// The three abilities are MUTUALLY EXCLUSIVE -- Precedence Trap > Push > Shoot: a held trap (right)
// blocks a middle push entirely (pushHeld is gated on !right) and, like a held push, overrides a
// shoot charge. Precedence is expressed through CancelCharge (also a level): while a higher-priority
// ability is held AND shoot is held, the charge is dropped and latched canceled, so the shot never
// fires. This is enforced here, in the human controller, rather than in the sim, because the AI
// deliberately traps WHILE charging a shot as a recover move (CancelCharge false so its shot
// survives -- see Match.applyIntent); the AI sets its own Intent fields and is unaffected.
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

	left := ebiten.IsMouseButtonPressed(ebiten.MouseButtonLeft)
	right := ebiten.IsMouseButtonPressed(ebiten.MouseButtonRight)
	// A held trap (right) blocks the push entirely: middle can never push while right is held.
	pushHeld := ebiten.IsMouseButtonPressed(ebiten.MouseButtonMiddle) && !right

	trapHeld := right
	// Report the raw held shoot button -- the sim reconstructs the release edge from shootHeldPrev.
	// Express the Trap > Push > Shoot precedence through CancelCharge (a level, not an edge): while a
	// higher-priority ability (trap or push) is held AND shoot is held, drop and latch-cancel the
	// charge so the shot never fires. As a level this survives the network and preserves the
	// right-click-cancels-a-charge behaviour.
	shootHeld := left
	cancelCharge := left && (trapHeld || pushHeld)

	cursorX, cursorY := ebiten.CursorPosition()
	return sim.Intent{
		Move:          move,
		Throttle:      throttle,
		Aim:           h.vp.ScreenToWorld(cursorX, cursorY),
		AimFromCursor: true, // turn toward the cursor at TurnRate -- no instant snap of the disk
		ShootHeld:     shootHeld,
		Trap:          trapHeld,
		CancelCharge:  cancelCharge,
		Push:          pushHeld,
	}
}
