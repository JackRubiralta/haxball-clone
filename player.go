package main

import (
	"image/color"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/vector"
)

// Player represents a player that extends Disc.
type Player struct {
	*Disc
	PlayerID   int
	TouchRange float64 // surface gap under which the ball counts as touching (control + shoot)
	PullRange  float64 // surface gap under which the centre pull can reach the ball

	// Each curve maps the ball's angle (0 deg = dead in front, 180 deg = directly
	// behind) to a quantity, between its front value and its back value. Swap the
	// curve to change how the value transitions across the angle.
	RestitutionCurve AngleCurve // bounce: soft front touch -> springy back
	FrontRestitution float64
	BackRestitution  float64

	// Below this impact speed (angle-dependent) the ball is captured and sticks on
	// the first touch; above it the ball bounces off with the restitution above.
	CaptureSpeedCurve AngleCurve
	FrontCaptureSpeed float64
	BackCaptureSpeed  float64

	// Force 1: a radial pull toward the player centre. It reaches the ball before
	// it touches (stronger the closer it gets) and is stronger toward the front.
	// This is what holds the ball to the player so it sticks.
	CenterPullCurve AngleCurve
	FrontCenterPull float64
	BackCenterPull  float64
	ApproachDamping float64 // eases the ball's inward speed so it settles instead of bouncing

	// Force 2: a tangential pull that rolls a touching ball around to the front
	// (0 deg). Stronger at the front than the back.
	ControlCurve   AngleCurve
	FrontControl   float64
	BackControl    float64
	ControlDamping float64 // bleeds off sideways (orbital) speed so the ball settles at the front

	ShootCurve      AngleCurve // shot power, by angle
	FrontShootForce float64
	BackShootForce  float64

	Facing Vec // unit vector pointing toward the mouse cursor
}

// NewPlayer creates a new player.
func NewPlayer(position Vec, radius float64, shootForce float64, playerID int) *Player {
	return &Player{
		Disc:       NewDisc(position, radius, -1.5, 30),
		PlayerID:   playerID,
		TouchRange: 2,
		PullRange:  8,

		RestitutionCurve: QuadraticCurve,
		FrontRestitution: 0.0,
		BackRestitution:  0.6,

		CaptureSpeedCurve: LinearCurve,
		FrontCaptureSpeed: 250,
		BackCaptureSpeed:  80,

		CenterPullCurve: QuadraticCurve,
		FrontCenterPull: 1000,
		BackCenterPull:  0,
		ApproachDamping: 10,

		ControlCurve:   LinearCurve,
		FrontControl:   900,
		BackControl:    0,
		ControlDamping: 8,

		ShootCurve:      LinearCurve,
		FrontShootForce: shootForce,
		BackShootForce:  shootForce * 0.3,

		Facing: NewVec(1, 0),
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

// FaceTowards points the player toward the given point (e.g. the mouse cursor).
func (p *Player) FaceTowards(point Vec) {
	direction := point.Sub(p.Position)
	if length := Norm(direction); length > 0 {
		p.Facing = direction.Mul(1 / length)
	}
}

// Draw draws the player on the screen along with the cone showing where it faces.
func (p *Player) Draw(screen *ebiten.Image) {
	p.drawFacingCone(screen)
	vector.DrawFilledCircle(screen, float32(p.Position.X), float32(p.Position.Y), float32(p.Radius), color.RGBA{255, 100, 100, 255}, true)
}

// whiteImage is a 1x1 white texture used to fill vector shapes such as the cone.
var whiteImage *ebiten.Image

// drawFacingCone draws a cone in front of the player pointing toward the cursor.
func (p *Player) drawFacingCone(screen *ebiten.Image) {
	if whiteImage == nil {
		whiteImage = ebiten.NewImage(1, 1)
		whiteImage.Fill(color.White)
	}

	const (
		coneLength    = 34.0 // how far the cone reaches past the player
		coneHalfWidth = 16.0 // half the width of the cone's base
	)

	forward := p.Facing
	perp := NewVec(-forward.Y, forward.X)
	apex := p.Position.Add(forward.Mul(p.Radius))
	baseCenter := p.Position.Add(forward.Mul(p.Radius + coneLength))
	left := baseCenter.Add(perp.Mul(coneHalfWidth))
	right := baseCenter.Sub(perp.Mul(coneHalfWidth))

	var path vector.Path
	path.MoveTo(float32(apex.X), float32(apex.Y))
	path.LineTo(float32(left.X), float32(left.Y))
	path.LineTo(float32(right.X), float32(right.Y))
	path.Close()

	vertices, indices := path.AppendVerticesAndIndicesForFilling(nil, nil)
	for i := range vertices {
		vertices[i].SrcX = 0
		vertices[i].SrcY = 0
		vertices[i].ColorR = 1
		vertices[i].ColorG = 0.85
		vertices[i].ColorB = 0.1
		vertices[i].ColorA = 1
	}
	screen.DrawTriangles(vertices, indices, whiteImage, &ebiten.DrawTrianglesOptions{AntiAlias: true})
}
