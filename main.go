package main

import (
	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/ebitenutil"
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
	if ebiten.IsKeyPressed(ebiten.KeySpace) {
		shoot(g.Player, g.Ball)
	}

	deltaTime := 1.0 / 60.0

	g.Ball.Update(deltaTime)
	g.Player.Move(keys, deltaTime)
	g.Player.Update(deltaTime)

	// Handle collisions
	handleBallToBoxCollision(g.Ball, g.Box)
	handleBallToPlayerCollision(g.Ball, g.Player)
	handlePlayerToBoxCollision(g.Player, g.Box)
	attractBall(g.Player, g.Ball)

	return nil
}

// Draw draws the game state.
func (g *Game) Draw(screen *ebiten.Image) {
	screen.Fill(color.RGBA{255, 255, 255, 255})
	g.Box.Render(screen)
	g.Ball.Draw(screen)
	g.Player.Draw(screen)
	ebitenutil.DebugPrint(screen, "Use WASD to move. Press Space to shoot.")
}

// Layout sets the screen layout.
func (g *Game) Layout(outsideWidth, outsideHeight int) (int, int) {
	return outsideWidth, outsideHeight
}

func main() {
	player := NewPlayer(NewVec(100, 300), 20, 9, 1) // shoot_force set to 9
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
