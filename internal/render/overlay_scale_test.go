package render

import (
	"math"
	"testing"

	"github.com/hajimehoshi/ebiten/v2"
)

// TestFitBox checks the shared letterbox-fit math: a uniform scale on the limiting axis and a
// centring origin that splits the slack onto the other axis.
func TestFitBox(t *testing.T) {
	// A 1000x680 box into a 2000x1360 framebuffer fits exactly 2x with no margin.
	if s, ox, oy := fitBox(2000, 1360, 1000, 680); s != 2 || ox != 0 || oy != 0 {
		t.Errorf("exact-fit = (%v,%v,%v), want (2,0,0)", s, ox, oy)
	}
	// Width-limited: a square framebuffer fits the wide box on width and letterboxes top/bottom.
	if s, ox, oy := fitBox(1000, 1000, 1000, 680); s != 1 || ox != 0 || oy != 160 {
		t.Errorf("width-limited = (%v,%v,%v), want (1,0,160)", s, ox, oy)
	}
	// A degenerate destination clamps to scale 1 rather than collapsing to 0.
	if s, _, _ := fitBox(0, 0, 1000, 680); s != 1 {
		t.Errorf("degenerate scale = %v, want 1", s)
	}
}

// pitch presets the lobby offers (config.Geometry presets). The overlay must look identical on
// all of them; the world-fit pass deliberately does not.
var testPitches = []struct {
	name string
	w, h float64
}{
	{"small", 720, 520},
	{"standard", 1000, 680},
	{"large", 1480, 940},
}

// TestOverlayCanvasIsPitchIndependent is the regression guard for the reported bug: the
// scoreboard card and the "GOAL!" banner (and every other screen-space overlay) are drawn
// through newOverlayCanvas, whose transform must NOT depend on the pitch geometry. At one
// fixed window resolution the overlay scale/origin is identical on the small, standard, and
// large pitches -- so those overlays are a constant on-screen size regardless of pitch.
func TestOverlayCanvasIsPitchIndependent(t *testing.T) {
	const fbW, fbH = 1600, 900
	dst := ebiten.NewImage(fbW, fbH)

	saveW, saveH := worldW, worldH
	defer func() { worldW, worldH = saveW, saveH }()

	var want canvas
	for i, p := range testPitches {
		worldW, worldH = p.w, p.h // the pitch the world pass would set
		got := newOverlayCanvas(dst)
		if i == 0 {
			want = got
			continue
		}
		if got.scale != want.scale || got.ox != want.ox || got.oy != want.oy {
			t.Errorf("overlay transform on %s pitch = (scale=%v ox=%v oy=%v), want (scale=%v ox=%v oy=%v) -- overlay must not scale with the pitch",
				p.name, got.scale, got.ox, got.oy, want.scale, want.ox, want.oy)
		}
	}
}

// TestWorldFitCanvasTracksPitch is the companion: world-anchored decoration (the goal-spot
// ring) IS drawn through newWorldFitCanvas, which by design scales with the pitch -- so a
// larger pitch fits to the same window at a smaller scale. This both documents the intended
// split and proves the bug was real (the old HUD path used exactly this pitch-dependent scale).
func TestWorldFitCanvasTracksPitch(t *testing.T) {
	const fbW, fbH = 1600, 900
	dst := ebiten.NewImage(fbW, fbH)

	saveW, saveH := worldW, worldH
	defer func() { worldW, worldH = saveW, saveH }()

	worldW, worldH = 720, 520
	small := newWorldFitCanvas(dst).scale
	worldW, worldH = 1480, 940
	large := newWorldFitCanvas(dst).scale
	if !(large < small) {
		t.Errorf("world-fit scale: large pitch (%v) should be smaller than small pitch (%v)", large, small)
	}

	// On the standard pitch the overlay box and the world box coincide, so the two passes match
	// exactly -- the default look is unchanged by the overlay split.
	worldW, worldH = overlayW, overlayH
	if w, o := newWorldFitCanvas(dst).scale, newOverlayCanvas(dst).scale; math.Abs(w-o) > 1e-9 {
		t.Errorf("on the standard pitch world-fit (%v) and overlay (%v) scales should match", w, o)
	}
}
