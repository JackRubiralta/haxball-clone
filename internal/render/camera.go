package render

import (
	"math"

	"phootball/internal/geom"
	"phootball/internal/sim"
)

// FollowMode selects what the camera frames.
type FollowMode int

const (
	FitContent   FollowMode = iota // show the whole pitch
	FollowBall                     // track the ball, zoomed in (the default, haxball-style)
	FollowPlayer                   // track the local player, biased toward the ball
)

// Camera describes how the world is framed: a follow mode, a zoom level, and the world
// point at the centre of the view. It is client-side only -- it never affects the
// simulation or the network -- so its smoothing is frame-rate based, not deterministic.
type Camera struct {
	Mode      FollowMode
	Zoom      float64
	MinZoom   float64
	MaxZoom   float64
	Smoothing float64 // per-second lerp rate toward the target (0 = instant)
	BallBias  float64 // FollowPlayer: 0 = on the player, 1 = on the ball
	FocusID   int     // player to follow in FollowPlayer (-1 = none, falls back to the ball)

	center             geom.Vec
	contentW, contentH float64
	initialized        bool
}

// NewCamera returns the default match camera: a smooth, zoomed-in ball follow.
func NewCamera() *Camera {
	return &Camera{Mode: FollowBall, Zoom: 2, MinZoom: 0.9, MaxZoom: 4, Smoothing: 10, BallBias: 0.35, FocusID: -1}
}

// CameraModeFromName maps a flag/menu name to a follow mode.
func CameraModeFromName(name string) FollowMode {
	switch name {
	case "ball", "follow":
		return FollowBall
	case "player", "active":
		return FollowPlayer
	default:
		return FitContent
	}
}

// Reset makes the next frame snap to the target (no smoothing); call it at kickoff so a
// new match does not slide in from the previous view.
func (c *Camera) Reset() { c.initialized = false }

// prepare updates the camera for this frame: clamp the zoom, pick the target from the
// follow mode, and smoothly move the centre toward it (snapping on the first frame and
// in FitContent), kept within the pitch bounds.
func (c *Camera) prepare(contentW, contentH float64, m *sim.Match, dt float64) {
	c.contentW, c.contentH = contentW, contentH
	c.clampZoom()
	target := c.targetPoint(m)
	target.X = clampHalf(target.X, contentW, c.Zoom)
	target.Y = clampHalf(target.Y, contentH, c.Zoom)
	if !c.initialized || c.Mode == FitContent {
		c.center = target
		c.initialized = true
	} else {
		c.center = lerpVec(c.center, target, smoothAlpha(c.Smoothing, dt))
	}
	c.center.X = clampHalf(c.center.X, contentW, c.Zoom)
	c.center.Y = clampHalf(c.center.Y, contentH, c.Zoom)
}

// targetPoint returns the world point the camera wants centred (before clamping).
func (c *Camera) targetPoint(m *sim.Match) geom.Vec {
	switch c.Mode {
	case FollowPlayer:
		if p := c.focusPlayer(m); p != nil {
			return lerpVec(p.Position, m.Ball.Position, c.BallBias)
		}
		return m.Ball.Position
	case FollowBall:
		return m.Ball.Position
	default:
		c.Zoom = 1 // FitContent shows the whole pitch
		return geom.NewVec(c.contentW/2, c.contentH/2)
	}
}

func (c *Camera) focusPlayer(m *sim.Match) *sim.Player {
	if c.FocusID < 0 {
		return nil
	}
	return m.PlayerByID(c.FocusID)
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

func lerpVec(a, b geom.Vec, t float64) geom.Vec {
	return geom.NewVec(a.X+(b.X-a.X)*t, a.Y+(b.Y-a.Y)*t)
}

// smoothAlpha is the frame-rate-independent lerp factor for a per-second smoothing rate.
func smoothAlpha(perSec, dt float64) float64 {
	if perSec <= 0 || dt <= 0 {
		return 1
	}
	a := 1 - math.Exp(-perSec*dt)
	if a < 0 {
		return 0
	}
	if a > 1 {
		return 1
	}
	return a
}

// ZoomBy multiplies the zoom (clamped). It is ignored in FitContent (locked to the pitch).
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

// ToggleFollow cycles fit -> ball -> player -> fit, restoring a sensible zoom when
// leaving the fit view.
func (c *Camera) ToggleFollow() {
	switch c.Mode {
	case FitContent:
		c.Mode = FollowBall
		if c.Zoom < 1.4 {
			c.Zoom = 2
		}
	case FollowBall:
		c.Mode = FollowPlayer
	default:
		c.Mode = FitContent
	}
}
