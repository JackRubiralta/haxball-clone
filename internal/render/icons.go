package render

import (
	"image/color"
	"math"

	"github.com/hajimehoshi/ebiten/v2"

	"phootball/internal/geom"
)

// Procedural vector icons. Each helper draws a small glyph centred on (cx, cy) and
// sized to fit a square of side `size` (in the canvas's units, scaled by c.scale like
// every other primitive). They are built only from the existing vector primitives so
// they stay crisp at any resolution and carry no image assets.
//
// The drawing helpers are methods on canvas (unexported); the exported wrappers build a
// fit-to-window HUD canvas so the menu and post-match code can draw an icon in UI
// coordinates without touching Ebiten or the vector package directly.

// iconTrophy draws a winner's cup: a bowl, handles, stem, and base.
func (c canvas) iconTrophy(cx, cy, size float64, clr color.Color) {
	h := size / 2
	w := size * 0.34
	top := cy - h*0.75
	bowlBot := cy + h*0.1
	// Bowl (trapezoid narrowing downward).
	c.fillPolygon([]geom.Vec{
		geom.NewVec(cx-w, top),
		geom.NewVec(cx+w, top),
		geom.NewVec(cx+w*0.55, bowlBot),
		geom.NewVec(cx-w*0.55, bowlBot),
	}, clr)
	// Handles.
	lw := math.Max(1, size*0.09)
	c.strokeCircle(cx-w*1.05, top+h*0.28, w*0.5, lw, clr)
	c.strokeCircle(cx+w*1.05, top+h*0.28, w*0.5, lw, clr)
	// Stem and base.
	c.fillRect(cx-w*0.16, bowlBot, w*0.32, h*0.45, clr)
	c.fillRect(cx-w*0.7, cy+h*0.55, w*1.4, h*0.25, clr)
}

// fillPolygon fills a convex polygon as a fan of triangles drawn via thin slabs. The
// vector package has no filled-polygon primitive, so we triangulate around the first
// vertex and paint each triangle by stroking a tight fan of lines.
func (c canvas) fillPolygon(pts []geom.Vec, clr color.Color) {
	if len(pts) < 3 {
		return
	}
	// Paint by scanning the polygon as a fan of slim quads from the centroid; using the
	// existing line primitive keeps it resolution-independent. We approximate the fill by
	// drawing many radial spokes from the centroid to the edge.
	var cx, cy float64
	for _, p := range pts {
		cx += p.X
		cy += p.Y
	}
	cx /= float64(len(pts))
	cy /= float64(len(pts))
	center := geom.NewVec(cx, cy)
	// Spoke count per edge tracks its on-screen length so the fan never gaps (long edge)
	// and never over-draws (tiny edge); spokes step ~1px apart at the rim.
	for i := 0; i < len(pts); i++ {
		a := pts[i]
		b := pts[(i+1)%len(pts)]
		edgePx := float64(c.ln(geom.Norm(b.Sub(a))))
		spokes := int(edgePx)
		if spokes < 4 {
			spokes = 4
		}
		if spokes > 256 {
			spokes = 256
		}
		for j := 0; j <= spokes; j++ {
			t := float64(j) / float64(spokes)
			edge := geom.NewVec(a.X+(b.X-a.X)*t, a.Y+(b.Y-a.Y)*t)
			c.line(center.X, center.Y, edge.X, edge.Y, 1.6, clr)
		}
	}
}

// withAlpha returns clr with its alpha replaced (RGB preserved). This is only valid when clr is
// already DARK -- every channel <= a -- so the result stays a valid alpha-premultiplied colour
// (e.g. a near-black outline). To fade a BRIGHT colour (a team tint, an accent), use fade/fadeU8
// instead, which also scale the RGB; replacing only the alpha on a bright colour produces an
// invalid premultiplied value that renders additively (bright, non-fading, hue-shifted).
func withAlpha(clr color.Color, a uint8) color.RGBA {
	r, g, b, _ := clr.RGBA()
	return color.RGBA{uint8(r >> 8), uint8(g >> 8), uint8(b >> 8), a}
}

// fade premultiplies base by a STRAIGHT alpha a in [0,1], returning a VALID alpha-premultiplied
// color.RGBA. color.RGBA is premultiplied, so making a colour translucent means scaling the RGB
// channels by alpha too -- not just lowering A. Lowering only A on a bright colour yields an
// invalid premultiplied value that Ebiten renders additively (bright, non-fading, skewed toward
// cyan). This is the one correct way to fade a colour in this package (see drawPushPulse).
func fade(base color.RGBA, a float64) color.RGBA {
	if a <= 0 {
		return color.RGBA{}
	}
	if a > 1 {
		a = 1
	}
	return color.RGBA{
		uint8(float64(base.R) * a),
		uint8(float64(base.G) * a),
		uint8(float64(base.B) * a),
		uint8(255 * a),
	}
}

// fadeU8 is fade with the alpha given as a 0..255 byte (a/255 straight alpha), for call sites whose
// opacity is already expressed in 0..255 steps (e.g. a gradient between uint8 endpoints).
func fadeU8(base color.RGBA, a uint8) color.RGBA { return fade(base, float64(a)/255) }

// additiveGlow returns base's FULL rgb with a low alpha -- deliberately NOT a valid premultiplied
// colour, so Ebiten blends it additively: the full base colour is added on top of the background
// (brightening/tinting it) while the alpha only dims the background a little. This is INTENTIONAL
// for the shoot-charge gauge, whose classic look is an additive team-coloured glow that intensifies
// toward full charge (at a=255 it lands on the solid team colour). For a normal translucent colour
// that should genuinely fade, use fade/fadeU8 instead -- those premultiply correctly.
func additiveGlow(base color.RGBA, a uint8) color.RGBA { return color.RGBA{base.R, base.G, base.B, a} }

// Exported UI-coordinate icon wrappers for the menu and post-match screens. They build a
// fixed-overlay-box canvas (it never touches the aim transform, and -- like the rest of the
// screen-space UI -- it is sized in the pitch-independent overlay box) and draw in UI units.

// TeamSwatch draws the small team-colour dot (the same mark the HUD card uses) centred at
// (cx, cy) in UI units, so the result header and the HUD share one consistent shape -- not
// a shield. col is the team colour; it gets a subtle dark outline so a light colour reads.
func TeamSwatch(screen *ebiten.Image, cx, cy, size float64, col color.RGBA) {
	c := newOverlayCanvas(screen)
	r := size / 2
	c.fillCircle(cx, cy, r, col)
	c.strokeCircle(cx, cy, r-0.75, 1.5, withAlpha(outlineColor, 200))
}

// IconTrophy draws the trophy icon centred at (cx, cy) in UI units.
func IconTrophy(screen *ebiten.Image, cx, cy, size float64, clr color.Color) {
	newOverlayCanvas(screen).iconTrophy(cx, cy, size, clr)
}
