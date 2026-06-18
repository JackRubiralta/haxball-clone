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

// Intent reads WASD into a movement direction, the cursor into an aim point, and the three
// mouse buttons into the ball abilities: held left = a charging shot (fired on release), held
// right = a trap, middle = an instant poke jab. A right-click also cancels an in-progress shot
// charge: you abort a mistaken shot and settle the ball with the same press.
//
// The three abilities are MUTUALLY EXCLUSIVE -- a player can only do one at a time, so holding
// or pressing one stops the others. Precedence is Trap > Poke > Shoot: a held trap (right) blocks
// a middle-click jab entirely (you can never poke while a trap is held) and also overrides a
// shoot charge (matching the existing right-click-cancels-shoot); a middle-click jab otherwise
// overrides a shoot charge; and shoot (left) only acts when neither of the others is engaged.
// Engaging a higher-priority ability also CANCELS an in-progress shot so it does not fire.
//
// This is enforced here, in the human controller, rather than in the sim, because the AI
// deliberately traps WHILE charging a shot as a recover move (with CancelCharge false so its
// shot survives -- see Match.applyIntent). A sim-level lock would break that; the exclusivity
// is a property of the three mouse buttons, so it belongs with the buttons.
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
	rightEdge := inpututil.IsMouseButtonJustPressed(ebiten.MouseButtonRight)
	// A held trap (right) blocks the poke entirely: middle-click can never fire while right is held.
	poke := inpututil.IsMouseButtonJustPressed(ebiten.MouseButtonMiddle) && !right

	// Resolve the precedence Trap > Poke > Shoot:
	trapHeld := right // a held trap takes precedence; it has already suppressed the poke above
	// Shoot is suppressed whenever a higher-priority ability is engaged...
	suppressShoot := trapHeld || poke
	// ...but on the very tick one TAKES OVER (a trap just pressed, or a poke), keep the shoot
	// button asserted so the cancel below actually drops a live charge -- the sim only honours a
	// cancel while shoot reads held; a bare release would instead FIRE the charged shot.
	takeover := rightEdge || poke
	shootHeld := left && (!suppressShoot || takeover)
	// A higher-priority ability (a just-pressed trap, or a poke) cancels a charging shot so it is
	// dropped, not released as a shot. (rightEdge preserves the original right-click cancel.)
	cancelCharge := rightEdge || poke

	cursorX, cursorY := ebiten.CursorPosition()
	return sim.Intent{
		Move:          move,
		Throttle:      throttle,
		Aim:           render.ScreenToWorld(cursorX, cursorY),
		AimFromCursor: true, // turn toward the cursor at TurnRate -- no instant snap of the disk
		ShootHeld:     shootHeld,
		Trap:          trapHeld,
		CancelCharge:  cancelCharge,
		Poke:          poke,
	}
}
