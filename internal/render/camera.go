package render

import (
	"phootball/internal/geom"
	"phootball/internal/sim"
)

// FollowMode selects what the camera frames.
type FollowMode int

const (
	FitContent FollowMode = iota // show the whole pitch (the default look)
	FollowBall                   // track the ball, zoomed in
)

// Camera describes how the world is framed: a follow mode, a zoom level, and the world
// point at the centre of the view. It is client-side only; it never affects the
// simulation or the network. At FitContent/zoom 1 it reproduces the original fit-to-
// window look exactly.
type Camera struct {
	Mode    FollowMode
	Zoom    float64
	MinZoom float64
	MaxZoom float64

	center             geom.Vec
	contentW, contentH float64
}

// NewCamera returns a camera that fits the whole pitch.
func NewCamera() *Camera {
	return &Camera{Mode: FitContent, Zoom: 1, MinZoom: 0.75, MaxZoom: 4}
}

// CameraModeFromName maps a flag/menu name to a follow mode.
func CameraModeFromName(name string) FollowMode {
	switch name {
	case "ball", "follow":
		return FollowBall
	default:
		return FitContent
	}
}

// prepare updates the camera for this frame: clamp the zoom, choose the centre from the
// follow mode, and keep the view within the content bounds.
func (c *Camera) prepare(contentW, contentH float64, m *sim.Match) {
	c.contentW, c.contentH = contentW, contentH
	c.clampZoom()
	switch c.Mode {
	case FollowBall:
		c.center = m.Ball.Position
	default:
		c.center = geom.NewVec(contentW/2, contentH/2)
		c.Zoom = 1 // fit shows the whole pitch
	}
	c.center.X = clampHalf(c.center.X, c.contentW, c.Zoom)
	c.center.Y = clampHalf(c.center.Y, c.contentH, c.Zoom)
}

func (c *Camera) clampZoom() {
	if c.MinZoom <= 0 {
		c.MinZoom = 0.5
	}
	if c.MaxZoom <= 0 {
		c.MaxZoom = 4
	}
	if c.Zoom < c.MinZoom {
		c.Zoom = c.MinZoom
	}
	if c.Zoom > c.MaxZoom {
		c.Zoom = c.MaxZoom
	}
}

// clampHalf keeps a centre coordinate so the visible span stays within [0, size].
func clampHalf(v, size, zoom float64) float64 {
	half := size / (2 * zoom)
	if 2*half >= size {
		return size / 2
	}
	if v < half {
		return half
	}
	if v > size-half {
		return size - half
	}
	return v
}

// ZoomBy multiplies the zoom (clamped). It is ignored in FitContent, which is locked to
// the whole pitch.
func (c *Camera) ZoomBy(f float64) {
	if c.Mode == FitContent {
		return
	}
	c.Zoom *= f
	c.clampZoom()
}

// SetZoom sets the zoom directly (clamped).
func (c *Camera) SetZoom(z float64) {
	c.Zoom = z
	c.clampZoom()
}

// ToggleFollow switches between fitting the pitch and following the ball.
func (c *Camera) ToggleFollow() {
	if c.Mode == FollowBall {
		c.Mode = FitContent
		return
	}
	c.Mode = FollowBall
	if c.Zoom < 1.4 {
		c.Zoom = 1.8
	}
}
