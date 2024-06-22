package main

import (
	"image/color"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/vector"
)

// Player represents a player that extends Disc.
type Player struct {
	*Disc
	PlayerID         int
	AttractionRadius float64
	ShootForce       float64
}

// NewPlayer creates a new player.
func NewPlayer(position Vec, radius float64, shootForce float64, playerID int) *Player {
	return &Player{
		Disc:             NewDisc(position, radius, -1.5),
		PlayerID:         playerID,
		AttractionRadius: radius * 1.3,
		ShootForce:       shootForce,
	}
}

// Move updates the player's position based on keyboard input.
func (p *Player) Move(keys map[ebiten.Key]struct{}, deltaTime float64) {
	const (
		MAX_SPEED    = 100
		ACCELERATION = 300
	)

	p.Acceleration = Vec{0, 0}
	if _, ok := keys[ebiten.KeyW]; ok {
		p.Acceleration.Y -= ACCELERATION
	}
	if _, ok := keys[ebiten.KeyS]; ok {
		p.Acceleration.Y += ACCELERATION
	}
	if _, ok := keys[ebiten.KeyA]; ok {
		p.Acceleration.X -= ACCELERATION
	}
	if _, ok := keys[ebiten.KeyD]; ok {
		p.Acceleration.X += ACCELERATION
	}

	p.Velocity = p.Velocity.Add(p.Acceleration.Mul(deltaTime))
	speed := Norm(p.Velocity)
	if speed > MAX_SPEED {
		p.Velocity = p.Velocity.Mul(MAX_SPEED / speed)
	}

	frictionForce := p.Velocity.Mul(p.Friction)
	p.Velocity = p.Velocity.Add(frictionForce.Mul(deltaTime))
	p.Position = p.Position.Add(p.Velocity.Mul(deltaTime))
}

// Draw draws the player on the screen along with its attraction radius.
func (p *Player) Draw(screen *ebiten.Image) {
	// Draw the attraction disc (outer disc) with partial transparency
	//outerDiscColor := color.RGBA{255, 255, 255, 128}
	//vector.DrawFilledCircle(screen, float32(p.Position.X), float32(p.Position.Y), float32(p.AttractionRadius), outerDiscColor, true)

	// Draw the player disc with a black outline and colored fill
	//vector.DrawFilledCircle(screen, float32(p.Position.X), float32(p.Position.Y), float32(p.Radius+2), color.Black, true)
	vector.DrawFilledCircle(screen, float32(p.Position.X), float32(p.Position.Y), float32(p.Radius), color.RGBA{255, 100, 100, 255}, true)
}