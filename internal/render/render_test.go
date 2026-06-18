package render

import (
	"math"
	"testing"
)

func TestCameraModeFromName(t *testing.T) {
	cases := map[string]FollowMode{
		"ball": FollowBall, "follow": FollowBall,
		"player": FollowPlayer, "active": FollowPlayer,
		"fit": FitContent, "": FitContent, "nonsense": FitContent,
	}
	for name, want := range cases {
		if got := CameraModeFromName(name); got != want {
			t.Errorf("CameraModeFromName(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestClampZoom(t *testing.T) {
	c := &Camera{Zoom: 10, MinZoom: 0.9, MaxZoom: 4}
	c.clampZoom()
	if c.Zoom != 4 {
		t.Errorf("clampZoom high = %v, want 4", c.Zoom)
	}
	c = &Camera{Zoom: 0.1, MinZoom: 0.9, MaxZoom: 4}
	c.clampZoom()
	if c.Zoom != 0.9 {
		t.Errorf("clampZoom low = %v, want 0.9", c.Zoom)
	}
}

func TestClampHalf(t *testing.T) {
	// At zoom 1 the visible span equals size, so the centre is pinned to the middle.
	if got := clampHalf(10, 100, 1); got != 50 {
		t.Errorf("clampHalf full-span = %v, want 50", got)
	}
	// At zoom 2 the half-span is 25; a centre near an edge is pushed in to keep the view inside.
	if got := clampHalf(0, 100, 2); got != 25 {
		t.Errorf("clampHalf near-min = %v, want 25", got)
	}
	if got := clampHalf(100, 100, 2); got != 75 {
		t.Errorf("clampHalf near-max = %v, want 75", got)
	}
	if got := clampHalf(50, 100, 2); got != 50 {
		t.Errorf("clampHalf centred = %v, want 50", got)
	}
}

func TestSmoothAlpha(t *testing.T) {
	if got := smoothAlpha(0, 0.016); got != 1 {
		t.Errorf("smoothAlpha(0,_) = %v, want 1 (instant)", got)
	}
	if got := smoothAlpha(10, 0); got != 1 {
		t.Errorf("smoothAlpha(_,0) = %v, want 1", got)
	}
	a := smoothAlpha(10, 1.0/60.0)
	if a <= 0 || a >= 1 {
		t.Errorf("smoothAlpha in (0,1) expected, got %v", a)
	}
	if want := 1 - math.Exp(-10.0/60.0); math.Abs(a-want) > 1e-9 {
		t.Errorf("smoothAlpha = %v, want %v", a, want)
	}
}

func TestViewportScreenToWorldRoundTrip(t *testing.T) {
	vp := Viewport{scale: 2, ox: 10, oy: 20}
	// A world point -> its framebuffer pixel -> back to (approximately) the world point.
	worldX, worldY := 33.0, 44.0
	sx := int(worldX*vp.scale + vp.ox)
	sy := int(worldY*vp.scale + vp.oy)
	got := vp.ScreenToWorld(sx, sy)
	if math.Abs(got.X-worldX) > 0.5 || math.Abs(got.Y-worldY) > 0.5 {
		t.Errorf("round-trip = %v, want ~(%v,%v)", got, worldX, worldY)
	}
	// The zero viewport is identity (what a controller sees before the first frame).
	if got := (Viewport{}).ScreenToWorld(7, 9); got.X != 7 || got.Y != 9 {
		t.Errorf("zero viewport = %v, want (7,9)", got)
	}
}

func TestFormatClock(t *testing.T) {
	cases := map[float64]string{0: "0:00", 5: "0:05", 65: "1:05", 600: "10:00", -3: "0:00"}
	for sec, want := range cases {
		if got := formatClock(sec); got != want {
			t.Errorf("formatClock(%v) = %q, want %q", sec, got, want)
		}
	}
}

func TestItoa(t *testing.T) {
	cases := map[int]string{0: "0", 7: "7", 42: "42", -5: "-5", 1000: "1000"}
	for n, want := range cases {
		if got := itoa(n); got != want {
			t.Errorf("itoa(%d) = %q, want %q", n, got, want)
		}
	}
}
