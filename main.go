package main

import (
	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/ebitenutil"
	"github.com/hajimehoshi/ebiten/v2/inpututil"
	"image/color"
	"log"
)

// Game represents the game state.
type Game struct {
	Player *Player
	Ball   *Ball
	Box    *Box
}

// Update updates the game state.
func (g *Game) Update() error {
	// Aim the player toward the mouse cursor.
	cursorX, cursorY := ebiten.CursorPosition()
	g.Player.FaceTowards(NewVec(float64(cursorX), float64(cursorY)))

	keys := map[ebiten.Key]struct{}{}
	if ebiten.IsKeyPressed(ebiten.KeyW) {
		keys[ebiten.KeyW] = struct{}{}
	}
	if ebiten.IsKeyPressed(ebiten.KeyS) {
		keys[ebiten.KeyS] = struct{}{}
	}
	if ebiten.IsKeyPressed(ebiten.KeyA) {
		keys[ebiten.KeyA] = struct{}{}
	}
	if ebiten.IsKeyPressed(ebiten.KeyD) {
		keys[ebiten.KeyD] = struct{}{}
	}
	if inpututil.IsKeyJustPressed(ebiten.KeySpace) {
		shoot(g.Player, g.Ball)
	}

	deltaTime := 1.0 / 60.0

	g.Ball.Update(deltaTime)
	g.Player.Move(keys, deltaTime)
	g.Player.Update(deltaTime)

	// Handle collisions and the ball-player interaction (bounce + dribble).
	handleBallToBoxCollision(g.Ball, g.Box)
	handleBallToPlayerInteraction(g.Ball, g.Player, deltaTime)
	handlePlayerToBoxCollision(g.Player, g.Box)

	return nil
}

// Draw draws the game state.
func (g *Game) Draw(screen *ebiten.Image) {
	screen.Fill(color.RGBA{255, 255, 255, 255})
	g.Box.Render(screen)
	g.Ball.Draw(screen)
	g.Player.Draw(screen)
	ebitenutil.DebugPrint(screen, "Use WASD to move, aim with the mouse, press Space to shoot.")
}

// Layout sets the screen layout.
func (g *Game) Layout(outsideWidth, outsideHeight int) (int, int) {
	return outsideWidth, outsideHeight
}

func main() {
	player := NewPlayer(NewVec(100, 300), 20, 500, 1) // shoot speed (px/s) along the facing direction
	ball := NewBall(NewVec(400, 300), 10)

	// Get the screen size in fullscreen mode
	screenWidth, screenHeight := ebiten.ScreenSizeInFullscreen()

	// Calculate box position to center it in the middle of the screen
	boxWidth, boxHeight := 700.0, 400.0
	goalWidth, goalHeight := 50.0, 200.0
	boxX := (float64(screenWidth) - boxWidth) / 2
	boxY := (float64(screenHeight) - boxHeight) / 2
	box := NewBox(boxX, boxY, boxWidth, boxHeight, goalWidth, goalHeight) // Centered box with goals

	game := &Game{
		Player: player,
		Ball:   ball,
		Box:    box,
	}

	ebiten.SetWindowSize(screenWidth, screenHeight)
	ebiten.SetWindowTitle("Go Game with Ebiten")
	if err := ebiten.RunGame(game); err != nil {
		log.Fatal(err)
	}
}
