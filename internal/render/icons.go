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
// they stay crisp at any resolution and carry no image assets. The HUD chips, clock,
// stage cards, and the menu/post-match screens share them, so the look is single-sourced.
//
// The drawing helpers are methods on canvas (unexported); the exported Icon* wrappers
// build a fit-to-window HUD canvas so the menu and post-match code can draw an icon in
// UI coordinates without touching Ebiten or the vector package directly.

// iconPlay draws a filled right-pointing triangle (the "Play Match" glyph).
func (c canvas) iconPlay(cx, cy, size float64, clr color.Color) {
	h := size / 2
	// A slightly inset triangle so it reads as a play button, not a sharp wedge.
	x0 := cx - h*0.7
	x1 := cx + h*0.85
	c.fillTriangle(
		geom.NewVec(x0, cy-h),
		geom.NewVec(x0, cy+h),
		geom.NewVec(x1, cy),
		clr)
}

// iconShield draws a small crest outline (a team badge): a flat-topped shield that
// tapers to a point. Used as the procedural badge on a team chip.
func (c canvas) iconShield(cx, cy, size float64, fill, edge color.Color) {
	h := size / 2
	w := size * 0.42
	top := cy - h
	shoulder := cy - h*0.45
	pts := []geom.Vec{
		geom.NewVec(cx-w, top),
		geom.NewVec(cx+w, top),
		geom.NewVec(cx+w, shoulder),
		geom.NewVec(cx, cy+h),
		geom.NewVec(cx-w, shoulder),
	}
	c.fillPolygon(pts, fill)
	c.strokePolygon(pts, math.Max(1, size*0.08), edge)
}

// iconClock draws a clock face: a ring with two hands.
func (c canvas) iconClock(cx, cy, size float64, clr color.Color) {
	r := size / 2
	w := math.Max(1, size*0.09)
	c.strokeCircle(cx, cy, r*0.92, w, clr)
	// Hour hand (up) and minute hand (right), like a 12:15-ish dial.
	c.line(cx, cy, cx, cy-r*0.5, w, clr)
	c.line(cx, cy, cx+r*0.62, cy, w, clr)
	c.fillCircle(cx, cy, w*0.9, clr)
}

// iconWhistle draws a referee whistle: a body with a mouthpiece and a pea hole.
func (c canvas) iconWhistle(cx, cy, size float64, clr color.Color) {
	r := size * 0.34
	bx := cx - size*0.1
	c.fillCircle(bx, cy, r, clr)
	// Mouthpiece stub to the upper right.
	c.fillRect(bx, cy-r*0.55, size*0.5, r*0.55, clr)
	// Pea hole punched out of the body.
	c.fillCircle(bx, cy, r*0.4, color.RGBA{0, 0, 0, 0})
	c.strokeCircle(bx, cy, r*0.4, math.Max(1, size*0.05), color.RGBA{0, 0, 0, 90})
}

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

// iconGoalNet draws a goal: two posts, a crossbar, and a couple of net verticals.
func (c canvas) iconGoalNet(cx, cy, size float64, clr color.Color) {
	h := size / 2
	w := size * 0.45
	lw := math.Max(1, size*0.09)
	top := cy - h*0.7
	bot := cy + h*0.7
	c.line(cx-w, top, cx+w, top, lw, clr) // crossbar
	c.line(cx-w, top, cx-w, bot, lw, clr) // left post
	c.line(cx+w, top, cx+w, bot, lw, clr) // right post
	// Faint net lattice.
	net := withAlpha(clr, 120)
	c.line(cx-w*0.33, top, cx-w*0.33, bot, math.Max(1, size*0.04), net)
	c.line(cx+w*0.33, top, cx+w*0.33, bot, math.Max(1, size*0.04), net)
	c.line(cx-w, cy, cx+w, cy, math.Max(1, size*0.04), net)
}

// iconGear draws a settings cog: a hub with radial teeth.
func (c canvas) iconGear(cx, cy, size float64, clr color.Color) {
	r := size * 0.34
	const teeth = 8
	tooth := size * 0.12
	lw := math.Max(2, size*0.16)
	for i := 0; i < teeth; i++ {
		a := 2 * math.Pi * float64(i) / float64(teeth)
		x0 := cx + (r)*math.Cos(a)
		y0 := cy + (r)*math.Sin(a)
		x1 := cx + (r+tooth)*math.Cos(a)
		y1 := cy + (r+tooth)*math.Sin(a)
		c.line(x0, y0, x1, y1, lw, clr)
	}
	c.strokeCircle(cx, cy, r, math.Max(1, size*0.12), clr)
	c.fillCircle(cx, cy, r*0.42, clr)
}

// fillTriangle fills a triangle from three world/UI points.
func (c canvas) fillTriangle(a, b, cc geom.Vec, clr color.Color) {
	c.fillPolygon([]geom.Vec{a, b, cc}, clr)
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

// strokePolygon outlines a closed polygon.
func (c canvas) strokePolygon(pts []geom.Vec, w float64, clr color.Color) {
	if len(pts) < 2 {
		return
	}
	for i := 0; i < len(pts); i++ {
		a := pts[i]
		b := pts[(i+1)%len(pts)]
		c.line(a.X, a.Y, b.X, b.Y, w, clr)
	}
}

// withAlpha returns clr with its alpha replaced (preserving RGB).
func withAlpha(clr color.Color, a uint8) color.RGBA {
	r, g, b, _ := clr.RGBA()
	return color.RGBA{uint8(r >> 8), uint8(g >> 8), uint8(b >> 8), a}
}

// Exported UI-coordinate icon wrappers for the menu and post-match screens. They build a
// fixed-overlay-box canvas (it never touches the aim transform, and -- like the rest of the
// screen-space UI -- it is sized in the pitch-independent overlay box) and draw in UI units.

// IconPlay draws the play-triangle icon centred at (cx, cy) in UI units.
func IconPlay(screen *ebiten.Image, cx, cy, size float64, clr color.Color) {
	newOverlayCanvas(screen).iconPlay(cx, cy, size, clr)
}

// IconShield draws the team-badge crest centred at (cx, cy) in UI units.
func IconShield(screen *ebiten.Image, cx, cy, size float64, fill, edge color.Color) {
	newOverlayCanvas(screen).iconShield(cx, cy, size, fill, edge)
}

// TeamSwatch draws the small team-colour dot (the same mark the HUD card uses) centred at
// (cx, cy) in UI units, so the result header and the HUD share one consistent shape -- not
// a shield. col is the team colour; it gets a subtle dark outline so a light colour reads.
func TeamSwatch(screen *ebiten.Image, cx, cy, size float64, col color.RGBA) {
	c := newOverlayCanvas(screen)
	r := size / 2
	c.fillCircle(cx, cy, r, col)
	c.strokeCircle(cx, cy, r-0.75, 1.5, withAlpha(outlineColor, 200))
}

// IconClock draws the clock icon centred at (cx, cy) in UI units.
func IconClock(screen *ebiten.Image, cx, cy, size float64, clr color.Color) {
	newOverlayCanvas(screen).iconClock(cx, cy, size, clr)
}

// IconWhistle draws the whistle icon centred at (cx, cy) in UI units.
func IconWhistle(screen *ebiten.Image, cx, cy, size float64, clr color.Color) {
	newOverlayCanvas(screen).iconWhistle(cx, cy, size, clr)
}

// IconTrophy draws the trophy icon centred at (cx, cy) in UI units.
func IconTrophy(screen *ebiten.Image, cx, cy, size float64, clr color.Color) {
	newOverlayCanvas(screen).iconTrophy(cx, cy, size, clr)
}

// IconGoalNet draws the goal-net icon centred at (cx, cy) in UI units.
func IconGoalNet(screen *ebiten.Image, cx, cy, size float64, clr color.Color) {
	newOverlayCanvas(screen).iconGoalNet(cx, cy, size, clr)
}

// IconGear draws the settings cog centred at (cx, cy) in UI units.
func IconGear(screen *ebiten.Image, cx, cy, size float64, clr color.Color) {
	newOverlayCanvas(screen).iconGear(cx, cy, size, clr)
}
