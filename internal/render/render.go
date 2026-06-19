// Package render draws the game with Ebiten. It is the only internal package (other
// than the human controller) that imports Ebiten, keeping graphics out of the
// simulation. It exposes small primitives so both the local game (drawing a
// sim.Match) and the network client (drawing a server snapshot) share the same look.
package render

import (
	"image"
	"image/color"
	"math"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/ebitenutil"
	"github.com/hajimehoshi/ebiten/v2/text"
	"github.com/hajimehoshi/ebiten/v2/vector"
	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gobold"
	"golang.org/x/image/font/opentype"

	"phootball/internal/config"
	"phootball/internal/geom"
	"phootball/internal/sim"
)

// World size. Gameplay and input use these fixed logical coordinates, so a client's
// cursor aim maps to the server's world 1:1 regardless of the actual window
// resolution. The world is scaled up to the real (high-DPI) framebuffer at draw time.
const (
	ScreenWidth  = 1000
	ScreenHeight = 680
)

var (
	stadiumColor = color.RGBA{18, 46, 26, 255}                     // outside the pitch
	stripeA      = color.RGBA{46, 138, 54, 255}                    // mowed grass band
	stripeB      = color.RGBA{52, 150, 60, 255}                    // alternate band
	lineColor    = color.RGBA{235, 240, 235, 255}                  // pitch markings
	netFill      = color.RGBA{14, 18, 24, 150}                     // dark goal interior
	netLine      = fade(color.RGBA{120, 132, 140, 255}, 130.0/255) // off-colour (cool grey) net mesh, premultiplied
	ballWhite    = color.RGBA{248, 248, 248, 255}
	ballOutline  = color.RGBA{60, 60, 66, 255}
	seamColor    = color.RGBA{44, 44, 52, 255}
	outlineColor = color.RGBA{28, 28, 36, 255}
	coneOuter    = color.RGBA{235, 120, 30, 255}
	coneInner    = color.RGBA{252, 206, 130, 255}
	hudColor     = color.RGBA{0, 0, 0, 110}
	bannerColor  = color.RGBA{0, 0, 0, 165}
	hudText      = color.RGBA{226, 234, 226, 255}                  // legible HUD text (matches the menu Text)
	hudDim       = color.RGBA{170, 188, 172, 235}                  // secondary HUD text (matches the menu TextDim)
	offsideLine  = fade(color.RGBA{235, 240, 235, 255}, 105.0/255) // translucent white anti-camp line, premultiplied
)

// Line widths (world units). EVERY painted pitch line -- the boundary, the goal line that closes
// each mouth, the penalty/goal boxes and the centre markings -- shares ONE width so all the pitch
// lines are uniform. The width is sourced from sim.PitchLineWidth because goal detection depends on
// it (a goal counts only once the ball has fully cleared the drawn goal line), so the white line
// drawn here is exactly the line the ball must cross. Perimeter lines are offset outward by half
// their width so the inner edge lands exactly on the physics wall.
const (
	fieldLineWidth  = sim.PitchLineWidth
	fieldLineOffset = fieldLineWidth / 2
	markingWidth    = fieldLineWidth
)

// jerseyFontSource is a parsed bold outline font, so jersey numbers render as smooth
// vectors at any resolution instead of a pixelated bitmap font.
var jerseyFontSource = mustParseFont(gobold.TTF)

func mustParseFont(b []byte) *opentype.Font {
	f, err := opentype.Parse(b)
	if err != nil {
		panic(err)
	}
	return f
}

// jerseyFaces caches font faces by integer pixel size.
var jerseyFaces = map[int]font.Face{}

func jerseyFace(sizePx int) font.Face {
	if sizePx < 1 {
		sizePx = 1
	}
	if f, ok := jerseyFaces[sizePx]; ok {
		return f
	}
	f, err := opentype.NewFace(jerseyFontSource, &opentype.FaceOptions{Size: float64(sizePx), DPI: 72, Hinting: font.HintingFull})
	if err != nil {
		panic(err)
	}
	jerseyFaces[sizePx] = f
	return f
}

// Viewport is the resolved world->framebuffer transform for one drawn frame: exactly what
// is needed to invert a cursor (framebuffer) position back to world coordinates. Frame,
// Field, and BeginUI each return the Viewport they drew with, so cursor mapping is
// REENTRANT -- a caller holds the viewport from the frame it drew rather than reading any
// hidden package state. The zero Viewport maps a cursor to itself (identity), which is what
// a controller sees before the first frame is drawn.
type Viewport struct {
	scale  float64
	ox, oy float64
}

// ScreenToWorld converts a framebuffer (cursor) coordinate back into world space using the
// transform this viewport was drawn with.
func (vp Viewport) ScreenToWorld(x, y int) geom.Vec {
	if vp.scale == 0 {
		return geom.NewVec(float64(x), float64(y))
	}
	return geom.NewVec((float64(x)-vp.ox)/vp.scale, (float64(y)-vp.oy)/vp.scale)
}

// worldW and worldH are the logical world size the canvas fits to the window. They
// default to the standard surface and are set from the field's geometry whenever a
// field is drawn, so a pitch of any size is letterboxed correctly.
var worldW, worldH float64 = ScreenWidth, ScreenHeight

// Camera state. When camActive is set (during Frame's world pass), newCanvas applies a
// pan/zoom transform centred on camCenter; otherwise it fits the world to the window
// (the original look, used for the HUD and the network client). The resolved transform is
// returned as a Viewport (by Frame/Field), which is what ScreenToWorld inverts for aim --
// there is no package-global transform.
var (
	camActive bool
	camCenter geom.Vec
	camZoom   = 1.0
)

// canvas maps the fixed world coordinates onto an offscreen image of any resolution,
// scaling the world to fit and centring it (letterboxing as needed). Drawing through
// it rasterises every vector shape at the framebuffer's full resolution, so circles
// and lines stay crisp at 4K instead of being upscaled from a small framebuffer.
type canvas struct {
	dst    *ebiten.Image
	scale  float64
	ox, oy float64
}

// fitBox computes the letterbox transform that fits a logical box (boxW x boxH) into a
// destination of dstW x dstH: a single uniform scale (the limiting axis) and the origin that
// centres the scaled box, so the box keeps its aspect ratio with equal margins on the slack
// axis. It is the ONE formula behind every fit-to-window canvas -- the camera world pass, the
// world-fit decoration pass, and the fixed overlay pass -- each of which just passes a
// different box. A degenerate destination clamps the scale to 1 rather than vanishing.
func fitBox(dstW, dstH, boxW, boxH float64) (scale, ox, oy float64) {
	scale = math.Min(dstW/boxW, dstH/boxH)
	if scale <= 0 {
		scale = 1
	}
	return scale, (dstW - boxW*scale) / 2, (dstH - boxH*scale) / 2
}

func newCanvas(dst *ebiten.Image) canvas {
	w := float64(dst.Bounds().Dx())
	h := float64(dst.Bounds().Dy())
	base, ox, oy := fitBox(w, h, worldW, worldH)
	if camActive {
		// Pan/zoom: scale by zoom and centre the view on camCenter. At zoom 1 with
		// camCenter at the world centre this is identical to the fit transform below.
		scale := base * camZoom
		return canvas{dst: dst, scale: scale, ox: w/2 - camCenter.X*scale, oy: h/2 - camCenter.Y*scale}
	}
	return canvas{dst: dst, scale: base, ox: ox, oy: oy}
}

// viewport returns the cursor-inversion transform for this canvas (its scale and origin,
// without the destination image).
func (c canvas) viewport() Viewport { return Viewport{scale: c.scale, ox: c.ox, oy: c.oy} }

// newWorldFitCanvas builds a fit-to-window canvas for the CURRENT world box (worldW/worldH)
// WITHOUT the camera pan/zoom. It does not affect the world Viewport that Frame returns for
// aim (that is captured from the camera pass), so it is safe to use after it. It is for world-anchored
// decoration drawn after the world pass (the goal-celebration ring, which sits at the ball's
// world position): such marks should scale with the pitch like the ball does. SCREEN-SPACE UI
// must NOT use it -- its scale tracks the pitch, so a bigger pitch would shrink it. Anything
// that should stay a constant on-screen size uses newOverlayCanvas instead.
func newWorldFitCanvas(dst *ebiten.Image) canvas {
	w := float64(dst.Bounds().Dx())
	h := float64(dst.Bounds().Dy())
	scale, ox, oy := fitBox(w, h, worldW, worldH)
	return canvas{dst: dst, scale: scale, ox: ox, oy: oy}
}

// newOverlayCanvas builds a fit-to-window canvas for SCREEN-SPACE overlays -- the scoreboard
// card, the "GOAL!" banner, the pause/result banners, the stage cards, and the menu icons. It
// lays out in the FIXED overlay box (overlayW x overlayH, the same logical box the menus use),
// which is deliberately INDEPENDENT of the pitch geometry. So these elements are a CONSTANT
// on-screen size on every pitch: the small, standard, and large pitches all get the identical
// scoreboard and the identical goal banner -- they no longer grow or shrink with the pitch.
// Like newWorldFitCanvas it never pans or zooms and does not affect the world Viewport that
// Frame returns for aim, so the aim transform survives a HUD drawn over the world.
func newOverlayCanvas(dst *ebiten.Image) canvas {
	w := float64(dst.Bounds().Dx())
	h := float64(dst.Bounds().Dy())
	scale, ox, oy := fitBox(w, h, overlayW, overlayH)
	return canvas{dst: dst, scale: scale, ox: ox, oy: oy}
}

func (c canvas) px(x float64) float32 { return float32(x*c.scale + c.ox) }
func (c canvas) py(y float64) float32 { return float32(y*c.scale + c.oy) }
func (c canvas) ln(v float64) float32 { return float32(v * c.scale) }

func (c canvas) fillCircle(x, y, r float64, clr color.Color) {
	vector.DrawFilledCircle(c.dst, c.px(x), c.py(y), c.ln(r), clr, true)
}

func (c canvas) strokeCircle(x, y, r, w float64, clr color.Color) {
	vector.StrokeCircle(c.dst, c.px(x), c.py(y), c.ln(r), c.ln(w), clr, true)
}

func (c canvas) line(x1, y1, x2, y2, w float64, clr color.Color) {
	vector.StrokeLine(c.dst, c.px(x1), c.py(y1), c.px(x2), c.py(y2), c.ln(w), clr, true)
}

func (c canvas) fillRect(x, y, w, h float64, clr color.Color) {
	vector.DrawFilledRect(c.dst, c.px(x), c.py(y), c.ln(w), c.ln(h), clr, true)
}

func (c canvas) strokeRect(x, y, w, h, sw float64, clr color.Color) {
	vector.StrokeRect(c.dst, c.px(x), c.py(y), c.ln(w), c.ln(h), c.ln(sw), clr, true)
}

// openBox draws a penalty/goal box -- a config.Rect taken straight from the field geometry
// (f.PenaltyArea/f.GoalArea, the SAME source the simulation uses) -- that opens onto the goal
// line on the given side: only the three inner sides, omitting the goal-line edge so it never
// doubles the boundary. Sourcing the rect (not raw depth/width params) keeps the markings and
// the logical box from ever drifting, exactly as the pitch boundary is drawn from f.Min/f.Max.
func (c canvas) openBox(r config.Rect, side sim.Side, w float64, clr color.Color) {
	top, bot := r.Min.Y, r.Max.Y
	openX, farX := r.Min.X, r.Max.X // left box: opens on Min.X (the goal line), far edge is Max.X
	if side == sim.SideRight {
		openX, farX = r.Max.X, r.Min.X // right box: opens on Max.X
	}
	c.line(openX, top, farX, top, w, clr)
	c.line(openX, bot, farX, bot, w, clr)
	c.line(farX, top, farX, bot, w, clr)
	// Fill the two far corners so the round joins are not left notched.
	c.fillCircle(farX, top, w/2, clr)
	c.fillCircle(farX, bot, w/2, clr)
}

// text draws debug-font text near a world position. The bitmap font itself is not
// scaled; dxPx and dyPx nudge it in screen pixels.
func (c canvas) text(s string, x, y float64, dxPx, dyPx int) {
	ebitenutil.DebugPrintAt(c.dst, s, int(c.px(x))+dxPx, int(c.py(y))+dyPx)
}

// number draws a jersey number centred on a world position, in a smooth vector font
// sized in world units so it scales crisply with the framebuffer.
func (c canvas) number(s string, x, y, sizeWorld float64, clr color.Color) {
	face := jerseyFace(int(sizeWorld * c.scale))
	b := text.BoundString(face, s)
	ox := float64(c.px(x)) - float64(b.Min.X) - float64(b.Dx())/2
	oy := float64(c.py(y)) - float64(b.Min.Y) - float64(b.Dy())/2
	text.Draw(c.dst, s, face, int(ox), int(oy), clr)
}

// Text alignment for the sized vector-text API.
const (
	AlignLeft   = 0
	AlignCenter = 1
	AlignRight  = 2
)

// measureRefPx is the fixed reference pixel size the layout-measuring face is
// cached at. MeasureUI measures with this face once and scales the result
// linearly, so a width is identical in both immediate-mode passes (the update
// pass runs before any canvas/scale exists).
const measureRefPx = 64

// textSized draws text at a UI/world position in the smooth vector (jersey) font,
// sized in the same units as the coordinates (world/UI units, scaled by c.scale).
// align picks the horizontal anchor at x: AlignLeft/AlignCenter/AlignRight (using the
// per-string glyph box, so the visible glyphs centre correctly). The text is vertically
// centred on y using the FONT METRICS (ascent/descent), not the per-string box, so words
// with and without tall letters share one baseline and a row of mixed words stays aligned.
func (c canvas) textSized(s string, x, y, sizePx float64, align int, clr color.Color) {
	if s == "" {
		return
	}
	face := jerseyFace(int(sizePx * c.scale))
	b := text.BoundString(face, s)
	px := float64(c.px(x))
	switch align {
	case AlignCenter:
		px -= float64(b.Min.X) + float64(b.Dx())/2
	case AlignRight:
		px -= float64(b.Min.X) + float64(b.Dx())
	default:
		px -= float64(b.Min.X)
	}
	// Vertical: place the baseline so the font's LINE box (ascent/descent) centres on y,
	// independent of which glyphs s contains -- so "easy" (no ascenders) and "hard" align.
	m := face.Metrics()
	baseline := float64(c.py(y)) + (float64(m.Ascent)-float64(m.Descent))/(2*64)
	text.Draw(c.dst, s, face, int(px), int(baseline), clr)
}

// measureUI returns the rendered width of s, in UI units, for vector text drawn
// at sizeUI. It measures with a face cached at a fixed reference size and scales
// linearly, so the result does not depend on the current canvas scale (and is
// available in the update pass, which has no canvas). Layout math built on this
// therefore agrees between the update and draw passes.
func measureUI(s string, sizeUI float64) float64 {
	if s == "" {
		return 0
	}
	b := text.BoundString(jerseyFace(measureRefPx), s)
	return float64(b.Dx()) * sizeUI / float64(measureRefPx)
}

// UIWidth and UIHeight are the fixed logical size menus lay out in, independent of the
// pitch and scaled to the window exactly like the pitch is.
const (
	UIWidth  = 1000
	UIHeight = 680
)

// overlayW and overlayH are the fixed logical box that SCREEN-SPACE overlays (the scoreboard
// card, the "GOAL!" banner, the pause/result banners, the stage cards, and the menu icons) lay
// out in -- the same box the menus use. It is deliberately INDEPENDENT of the pitch geometry,
// so those overlays stay a constant on-screen size on every pitch. (The world/pitch box, by
// contrast, varies with the chosen pitch; newCanvas/newWorldFitCanvas fit that one.) They are
// typed float64 constants so the layout math below reads cleanly without per-use conversions.
const (
	overlayW float64 = UIWidth
	overlayH float64 = UIHeight
)

// UI is an immediate-mode drawing surface for menus, laid out in fixed UI coordinates
// and scaled to the window. Menus draw through it so they never touch Ebiten or the
// vector package directly, keeping the transform single-sourced.
//
// full holds the unclipped framebuffer so PopClip can restore it after a clipped scroll
// pane. c.dst may be a SubImage of full while a clip is active; either way the canvas
// transform (scale/origin) is unchanged, so clipped content keeps the SAME coordinate
// system -- it is merely masked to the pane rectangle.
type UI struct {
	c    canvas
	full *ebiten.Image
}

// BeginUI prepares a UI surface for this frame. Call Viewport on the result to map the
// cursor into UI coordinates.
func BeginUI(screen *ebiten.Image) UI {
	worldW, worldH = UIWidth, UIHeight
	return UI{c: newCanvas(screen), full: screen}
}

// Viewport returns the cursor-inversion transform for this UI surface, so a menu can map
// the cursor into UI coordinates without any package global.
func (u UI) Viewport() Viewport { return u.c.viewport() }

// PushClip restricts subsequent drawing to the UI-coordinate rectangle [x,y,w,h],
// returning a UI whose canvas targets a SubImage of the framebuffer with the SAME
// transform -- so content drawn through it uses identical coordinates but is masked to
// the rectangle (anything outside is not painted). Pair with the returned UI's PopClip
// (or just stop using it) to resume unclipped drawing. The rectangle is mapped to
// framebuffer pixels via the current canvas transform. A zero/degenerate rect (e.g. the
// update pass's empty UI) returns the UI unchanged, so layout-only passes are unaffected.
func (u UI) PushClip(x, y, w, h float64) UI {
	if u.full == nil || u.c.dst == nil {
		return u
	}
	// Map the UI rect to framebuffer pixels, clamped to the framebuffer bounds.
	px0 := int(math.Floor(float64(u.c.px(x))))
	py0 := int(math.Floor(float64(u.c.py(y))))
	px1 := int(math.Ceil(float64(u.c.px(x + w))))
	py1 := int(math.Ceil(float64(u.c.py(y + h))))
	b := u.full.Bounds()
	if px0 < b.Min.X {
		px0 = b.Min.X
	}
	if py0 < b.Min.Y {
		py0 = b.Min.Y
	}
	if px1 > b.Max.X {
		px1 = b.Max.X
	}
	if py1 > b.Max.Y {
		py1 = b.Max.Y
	}
	if px1 <= px0 || py1 <= py0 {
		return u
	}
	sub := u.full.SubImage(image.Rect(px0, py0, px1, py1)).(*ebiten.Image)
	nc := u.c
	nc.dst = sub
	return UI{c: nc, full: u.full}
}

// Fill clears the whole surface to a colour.
func (u UI) Fill(clr color.Color) { u.c.dst.Fill(clr) }

// FillRect draws a filled rectangle in UI coordinates.
func (u UI) FillRect(x, y, w, h float64, clr color.Color) { u.c.fillRect(x, y, w, h, clr) }

// DimScreen draws clr over the ENTIRE framebuffer (alpha-blended), so a translucent dim covers the
// whole screen -- including the letterbox margins OUTSIDE the UI box. Unlike FillRect (which only
// covers the centred UI box and so leaves the screen edges undimmed at off-aspect windows) and Fill
// (which replaces opaquely). Used to dim a paused/finished match uniformly to the screen edges.
func (u UI) DimScreen(clr color.Color) {
	b := u.c.dst.Bounds()
	vector.DrawFilledRect(u.c.dst, float32(b.Min.X), float32(b.Min.Y), float32(b.Dx()), float32(b.Dy()), clr, false)
}

// StrokeRect draws a rectangle outline in UI coordinates.
func (u UI) StrokeRect(x, y, w, h, sw float64, clr color.Color) { u.c.strokeRect(x, y, w, h, sw, clr) }

// Text draws left-aligned text at a UI position.
func (u UI) Text(s string, x, y float64) { u.c.text(s, x, y, 0, 0) }

// TextS draws left-aligned vector text at a UI position, sized in UI units and
// vertically centred on y. This is the legible, scalable replacement for Text.
func (u UI) TextS(s string, x, y, sizeUI float64, clr color.Color) {
	u.c.textSized(s, x, y, sizeUI, AlignLeft, clr)
}

// TextCenteredS draws vector text centred horizontally on cx, sized in UI units.
func (u UI) TextCenteredS(s string, cx, y, sizeUI float64, clr color.Color) {
	u.c.textSized(s, cx, y, sizeUI, AlignCenter, clr)
}

// TextRightS draws vector text ending at x (right-aligned), sized in UI units.
func (u UI) TextRightS(s string, x, y, sizeUI float64, clr color.Color) {
	u.c.textSized(s, x, y, sizeUI, AlignRight, clr)
}

// MeasureUI returns the width, in UI units, of s drawn as vector text at sizeUI.
// It is scale-independent, so menu layout math is identical in both the update
// and draw passes.
func (u UI) MeasureUI(s string, sizeUI float64) float64 { return measureUI(s, sizeUI) }

// Panel draws a filled, outlined rounded-feel rectangle for a menu surface.
func (u UI) Panel(x, y, w, h float64, fill, border color.Color) {
	u.c.fillRect(x, y, w, h, fill)
	u.c.strokeRect(x, y, w, h, 2, border)
}

// Line draws a line in UI coordinates.
func (u UI) Line(x1, y1, x2, y2, w float64, clr color.Color) { u.c.line(x1, y1, x2, y2, w, clr) }

// Title draws large, crisp vector text centred on cx (using the jersey font).
func (u UI) Title(s string, cx, y, sizeWorld float64, clr color.Color) {
	u.c.number(s, cx, y, sizeWorld, clr)
}

// drawMatchOverlays draws the animated goal overlay, the stage-card transition, the
// pause banner, and the final result for a local match. It is shared by Match and Frame
// so both draw paths get the same presentation. The FX timers are client-side and purely
// cosmetic (advanced by wall time in advanceFX), so they never touch determinism.
func drawMatchOverlays(screen *ebiten.Image, m *sim.Match) {
	advanceFX()
	fx.observe(m.Celebrating(), m.Phase())
	drawStageCard(screen, m.Phase())
	if m.Celebrating() {
		drawGoalOverlay(screen, goalMessage(m), goalTint(m), m.Ball.Position, m.Field.Geo.ScreenWidth, m.Field.Geo.ScreenHeight)
	}
	switch {
	case m.Paused:
		centerBanner(screen, "P A U S E D")
	case m.Finished():
		centerBanner(screen, winnerMessage(m))
	}
}

// ZoneIndicators draws the positional-rule indicators (offside lines) over an
// already-drawn field. The network client calls it from a ruleset rebuilt from the
// snapshot, so the lines show on a remote client too.
func ZoneIndicators(screen *ebiten.Image, f *sim.Field, r config.Ruleset) {
	drawZoneIndicators(newCanvas(screen), f, r)
}

// drawZoneIndicators draws the offside line(s) as translucent white verticals. The
// player-capped boxes carry NO overlay -- they are shown only by their normal white line
// outline (drawn from the same field geometry the physics walls use), with surplus
// players walled out at that line.
func drawZoneIndicators(c canvas, f *sim.Field, r config.Ruleset) {
	if r.OffsideEnabled {
		frac := r.OffsideFrac
		if frac <= 0 {
			frac = 2.0 / 3.0
		}
		lx := f.OffsideLineX(sim.SideLeft, frac)
		rx := f.OffsideLineX(sim.SideRight, frac)
		c.line(lx, f.Min.Y, lx, f.Max.Y, markingWidth, offsideLine)
		c.line(rx, f.Min.Y, rx, f.Max.Y, markingWidth, offsideLine)
	}
}

// Frame draws a complete local match through a camera. The pitch, ball, and players go
// through the camera transform (pan/zoom); the HUD is drawn fit-to-window so it never
// pans or zooms. ScreenToWorld inverts the camera transform, so aim stays correct at any
// zoom or pan.
// Frame returns the camera Viewport it drew with, so the caller can invert the cursor for
// aim at any pan/zoom.
func Frame(screen *ebiten.Image, m *sim.Match, cam *Camera, dt float64) Viewport {
	worldW, worldH = m.Field.Geo.ScreenWidth, m.Field.Geo.ScreenHeight
	cam.prepare(worldW, worldH, m, dt)

	camActive, camCenter, camZoom = true, cam.center, cam.Zoom
	Field(screen, m.Field, m.Teams[0].Color, m.Teams[1].Color)
	drawPushPulses(screen, m) // under the ball and players, so the burst wells up from beneath the player instead of covering it
	BallAt(screen, m.Ball.Position, m.Ball.Radius())
	for _, p := range m.Players {
		PlayerAt(screen, p.Position, p.Facing, p.Radius(), p.Team.Color, p.Number,
			sim.NormShootCharge(p.ShootCharge()), p.TrapAura())
	}
	drawPossessionBarsAll(screen, m)
	drawTrapBarsAll(screen, m)
	zc := newCanvas(screen)
	drawZoneIndicators(zc, m.Field, m.Rules)
	vp := zc.viewport() // the camera transform, captured before the HUD's fit pass
	camActive = false

	DrawHUD(screen, hudFromMatch(m))
	drawMatchOverlays(screen, m)
	return vp
}

// goalMessage describes the most recent goal (scorer, assist, own goal, deflection).
func goalMessage(m *sim.Match) string {
	g := m.LastGoal
	if g == nil {
		return "G O A L !"
	}
	team := teamName(m, g.Team)
	if g.OwnGoal {
		return "OWN GOAL  " + team + scorerSuffix(m, g.HasScorer, g.Scorer)
	}
	msg := "GOAL!  " + team + scorerSuffix(m, g.HasScorer, g.Scorer)
	if g.HasAssist {
		if p := m.PlayerByID(g.Assist); p != nil {
			msg += " (assist #" + itoa(p.Number) + ")"
		}
	}
	if g.Deflected {
		msg += " (deflected)"
	}
	return msg
}

func scorerSuffix(m *sim.Match, has bool, id int) string {
	if !has {
		return ""
	}
	if p := m.PlayerByID(id); p != nil {
		return " #" + itoa(p.Number)
	}
	return ""
}

func teamName(m *sim.Match, side sim.Side) string {
	if m.Teams[0].Side == side {
		return m.Teams[0].Name
	}
	return m.Teams[1].Name
}

// winnerMessage describes a finished match's result.
func winnerMessage(m *sim.Match) string {
	switch m.Winner() {
	case sim.SideLeft:
		return m.Teams[0].Name + " WINS"
	case sim.SideRight:
		return m.Teams[1].Name + " WINS"
	default:
		return "DRAW"
	}
}

// Field draws the pitch: a striped lawn, boundary and markings, the two goals with nets,
// and any obstacles. It returns the Viewport it drew with so a caller that assembles its
// own frame (the network client) can invert the cursor for aim without a package global.
func Field(screen *ebiten.Image, f *sim.Field, leftColor, rightColor color.RGBA) Viewport {
	worldW, worldH = f.Geo.ScreenWidth, f.Geo.ScreenHeight
	screen.Fill(stadiumColor)
	c := newCanvas(screen)

	x, y := f.Min.X, f.Min.Y
	w, h := f.Width(), f.Height()
	cx, cy := f.CenterSpot.X, f.CenterSpot.Y
	gh := f.GoalHeight
	mouthTop := f.Min.Y + (h-gh)/2
	mouthBot := f.Min.Y + (h+gh)/2

	// Mowed grass stripes.
	const stripes = 12
	bandW := w / stripes
	for i := 0; i < stripes; i++ {
		col := stripeA
		if i%2 == 1 {
			col = stripeB
		}
		bx := x + float64(i)*bandW
		bw := bandW
		if i == stripes-1 {
			bw = x + w - bx
		}
		c.fillRect(bx, y, bw, h, col)
	}

	// Boundary lines. Each is offset outward by half its width so its inner edge sits
	// exactly on the physics wall: the ball and players stop flush against the line
	// instead of overlapping it. The goal mouths are left open.
	o := fieldLineOffset
	lX, rX := x-o, x+w+o
	tY, bY := y-o, y+h+o

	c.line(lX, tY, rX, tY, fieldLineWidth, lineColor)
	c.line(lX, bY, rX, bY, fieldLineWidth, lineColor)
	c.line(lX, tY, lX, mouthTop, fieldLineWidth, lineColor)
	c.line(lX, mouthBot, lX, bY, fieldLineWidth, lineColor)
	c.line(rX, tY, rX, mouthTop, fieldLineWidth, lineColor)
	c.line(rX, mouthBot, rX, bY, fieldLineWidth, lineColor)

	// Fill the four corners (round joins) so they are not left notched.
	c.fillCircle(lX, tY, o, lineColor)
	c.fillCircle(rX, tY, o, lineColor)
	c.fillCircle(lX, bY, o, lineColor)
	c.fillCircle(rX, bY, o, lineColor)

	// Halfway line, centre circle and spot -- sizes from the geometry.
	c.line(cx, y, cx, y+h, markingWidth, lineColor)
	c.strokeCircle(cx, cy, f.Geo.CenterCircleRadius, markingWidth, lineColor)
	c.fillCircle(cx, cy, f.Geo.CenterSpotMarkRadius, lineColor)

	// Penalty boxes and goal areas, drawn open on the goal-line side (three sides) so
	// their goal-line edge does not double up on the boundary near each goal. All sizes
	// come from the geometry -- the single source of truth the simulation uses too -- so
	// the markings always match the pitch. A box toggled off in the geometry is not drawn
	// (and the simulation does not enforce it either).
	if f.Geo.HasPenaltyArea {
		c.openBox(f.PenaltyArea(sim.SideLeft), sim.SideLeft, markingWidth, lineColor)
		c.openBox(f.PenaltyArea(sim.SideRight), sim.SideRight, markingWidth, lineColor)
		// Penalty spots, taken from the geometry's own spot positions (same source the sim uses).
		ls, rs := f.PenaltySpot(sim.SideLeft), f.PenaltySpot(sim.SideRight)
		c.fillCircle(ls.X, ls.Y, f.Geo.PenaltySpotMarkRadius, lineColor)
		c.fillCircle(rs.X, rs.Y, f.Geo.PenaltySpotMarkRadius, lineColor)
	}
	if f.Geo.HasGoalArea {
		c.openBox(f.GoalArea(sim.SideLeft), sim.SideLeft, markingWidth, lineColor)
		c.openBox(f.GoalArea(sim.SideRight), sim.SideRight, markingWidth, lineColor)
	}

	drawGoal(c, f.LeftGoal, f.GoalWidth, leftColor)
	drawGoal(c, f.RightGoal, f.GoalWidth, rightColor)

	for _, ob := range f.Obstacles {
		drawCone(c, ob.Position, ob.Radius())
	}
	return c.viewport()
}

// drawGoal draws a goal: a netted pocket with a team-coloured frame, posts, and goal
// line.
func drawGoal(c canvas, goal *sim.Goal, goalWidth float64, col color.RGBA) {
	top := goal.Mouth.A // top of the mouth, on the goal line
	bot := goal.Mouth.B // bottom of the mouth
	dir := 1.0
	if goal.Side == sim.SideLeft {
		dir = -1
	}
	backX := top.X + dir*goalWidth
	minX := math.Min(top.X, backX)

	// Net pocket and a uniform mesh sized to divide the pocket into whole squares (no
	// half cells cut off at the goal line). Only the INTERIOR lines are drawn in the
	// net colour; the outer edges are the team-coloured frame and the goal line, so the
	// squares are all the same size and bounded cleanly.
	c.fillRect(minX, top.Y, goalWidth, bot.Y-top.Y, netFill)
	nx := int(math.Round(goalWidth / 8))
	if nx < 1 {
		nx = 1
	}
	ny := int(math.Round((bot.Y - top.Y) / 8))
	if ny < 1 {
		ny = 1
	}
	cw := goalWidth / float64(nx)
	ch := (bot.Y - top.Y) / float64(ny)
	for i := 1; i < nx; i++ {
		lx := minX + float64(i)*cw
		c.line(lx, top.Y, lx, bot.Y, 1, netLine)
	}
	for j := 1; j < ny; j++ {
		gy := top.Y + float64(j)*ch
		c.line(minX, gy, minX+goalWidth, gy, 1, netLine)
	}

	// Frame: the team-coloured net edges (back, top, bottom). Each is offset OUTWARD by
	// half its width so its INNER edge sits exactly on the collision segment -- a player
	// resting against the net is then flush with it, never overlapping (the same
	// treatment as the pitch boundary). Back corners filled for clean joins.
	const fw = fieldLineWidth
	const fo = fieldLineWidth / 2
	bx := backX + dir*fo // back, pushed further out from the mouth
	ty := top.Y - fo     // top, pushed up off the net interior
	by := bot.Y + fo     // bottom, pushed down off the net interior
	c.line(bx, ty, bx, by, fw, col)
	c.line(top.X, ty, bx, ty, fw, col)
	c.line(bot.X, by, bx, by, fw, col)
	c.fillCircle(bx, ty, fo, col)
	c.fillCircle(bx, by, fo, col)

	// Goal line closing the mouth: offset OUTWARD (into the net) by half its width -- the same
	// offset as the boundary side segments -- so it sits collinear with the arena edge (its inner
	// edge flush on the goal line) instead of stepping inward. The ball must fully clear it to score.
	gx := top.X + dir*fo
	c.line(gx, top.Y, gx, bot.Y, fieldLineWidth, lineColor)

	// Posts (no outline): the team-coloured caps at the mouth corners. Offset outward by
	// half the frame width (the same offset as the net frame) so each cap is centred on
	// the post/frame corner instead of sitting a bit inside the mouth.
	for _, post := range goal.Posts {
		oy := fo
		if post.Position.Y < goal.Center.Y {
			oy = -fo
		}
		c.fillCircle(post.Position.X, post.Position.Y+oy, post.Radius(), col)
	}
}

// drawCone draws a fixed obstacle as a flat top-down traffic cone.
func drawCone(c canvas, pos geom.Vec, radius float64) {
	c.fillCircle(pos.X, pos.Y, radius, coneOuter)
	c.fillCircle(pos.X, pos.Y, radius*0.62, coneInner)
	c.fillCircle(pos.X, pos.Y, radius*0.3, coneOuter)
}

// BallAt draws a flat soccer ball: a white disc with a pentagon panel outlined in
// dark seams that run out to the rim (no solid centre).
func BallAt(screen *ebiten.Image, pos geom.Vec, radius float64) {
	c := newCanvas(screen)
	c.fillCircle(pos.X, pos.Y, radius, ballWhite)

	panel := regularPolygon(pos, radius*0.4, 5, -math.Pi/2)
	for i := 0; i < len(panel); i++ {
		a := panel[i]
		b := panel[(i+1)%len(panel)]
		c.line(a.X, a.Y, b.X, b.Y, 1.3, seamColor)
	}
	for _, v := range panel {
		dir := v.Sub(pos)
		if l := geom.Norm(dir); l > 0 {
			rim := pos.Add(dir.Scale(radius * 0.95 / l))
			c.line(v.X, v.Y, rim.X, rim.Y, 1.3, seamColor)
		}
	}
	// Outline inset so its outer edge sits exactly on the ball's radius (no overlap).
	const outlineWidth = 2.0
	c.strokeCircle(pos.X, pos.Y, radius-outlineWidth/2, outlineWidth, ballOutline)
}

// PlayerAt draws a player as a flat coloured disc with a thick outline, a jersey
// number, and a white dot at the front showing which way it faces.
func PlayerAt(screen *ebiten.Image, pos, facing geom.Vec, radius float64, body color.RGBA, number int, shootCharge, auraLevel float64) {
	c := newCanvas(screen)
	drawTrapAura(c, pos, radius, auraLevel, body) // glow under the body

	c.fillCircle(pos.X, pos.Y, radius, body)
	// Outline inset by half its width so its outer edge sits on the body's radius (the
	// collision surface) -- it no longer protrudes past it and overlaps a wall.
	const outlineWidth = 3.0
	c.strokeCircle(pos.X, pos.Y, radius-outlineWidth/2, outlineWidth, outlineColor)

	if geom.Norm(facing) > 0 {
		nose := pos.Add(facing.Scale(radius * 0.66))
		c.fillCircle(nose.X, nose.Y, radius*0.22, ballWhite)
	}

	c.number(itoa(number), pos.X, pos.Y, radius, ballWhite)
	drawShootCharge(c, pos, facing, radius, shootCharge, body) // power gauge over the body
}

// SnapshotEntity is one drawable entity (ball or player) in a SnapshotView.
type SnapshotEntity struct {
	IsBall                  bool
	PlayerID                int // player entities only; lets the renderer mark "you"
	Position, Facing        geom.Vec
	Radius                  float64
	Color                   color.RGBA
	Number                  int
	ShootCharge, TrapCharge float64 // TrapCharge is the 0..1 trap ENERGY bar
	TrapAura                float64 // 0..1 effective trap strength (the glow), 0 when not actively trapping
}

// SnapshotView is the render-agnostic projection of a server snapshot that FrameFromSnapshot
// draws. The render package must NOT import netcode, so callers adapt a netcode.Snapshot into this
// struct (the adapter lives in the caller, keeping render dependency-clean).
type SnapshotView struct {
	Geometry                                 config.Geometry
	LeftName, RightName                      string
	LeftColor, RightColor                    color.RGBA
	LeftScore, RightScore                    int
	ClockSeconds                             float64
	PhaseLabel                               string
	InShootout                               bool
	PenLeftGoals, PenLeftTaken               int
	PenRightGoals, PenRightTaken             int
	OffsideEnabled                           bool
	OffsideFrac                              float64
	PenaltyBoxMaxPlayers, GoalAreaMaxPlayers int
	Celebrating                              bool
	GoalText, WinnerText                     string
	Finished                                 bool
	Paused                                   bool       // the host paused the match
	GoalTint                                 color.RGBA // celebration tint (neutral for a pure client)
	Entities                                 []SnapshotEntity
	Stats                                    sim.StatsSnapshot
	SelfPlayerID                             int  // the local player's id, to mark "you" on the pitch
	HaveSelf                                 bool // whether SelfPlayerID is known
	RTTms                                    int  // round-trip latency for the corner badge (0 = hidden)
}

// FrameFromSnapshot draws a complete in-match frame from a server snapshot (no *sim.Match, no
// camera -- fit-to-window) and RETURNS the Viewport it drew with, so the caller feeds it to
// human.SetViewport next frame and the cursor maps to world aim. `field` is the caller-cached
// *sim.Field, rebuilt only when v.Geometry changes; `showStats` gates the live stats panel. This is
// the single in-match render path shared by the standalone client (cmd/client) and the menu app.
func FrameFromSnapshot(screen *ebiten.Image, v SnapshotView, field *sim.Field, showStats bool) Viewport {
	vp := Field(screen, field, v.LeftColor, v.RightColor) // also fills the screen and sets the world dims
	var ballPos geom.Vec
	for _, e := range v.Entities {
		if e.IsBall {
			ballPos = e.Position
			BallAt(screen, e.Position, e.Radius)
		} else {
			PlayerAt(screen, e.Position, e.Facing, e.Radius, e.Color, e.Number, e.ShootCharge, e.TrapAura)
			if v.HaveSelf && e.PlayerID == v.SelfPlayerID {
				PlayerSelfMarker(screen, e.Position, e.Radius)
			}
		}
	}
	if ShowTrapBars {
		tc := newCanvas(screen)
		for _, e := range v.Entities {
			if !e.IsBall {
				drawTrapEnergyBar(tc, e.Position, e.Radius, e.TrapCharge)
			}
		}
	}
	ZoneIndicators(screen, field, config.Ruleset{
		OffsideEnabled:       v.OffsideEnabled,
		OffsideFrac:          v.OffsideFrac,
		PenaltyBoxMaxPlayers: v.PenaltyBoxMaxPlayers,
		GoalAreaMaxPlayers:   v.GoalAreaMaxPlayers,
	})
	DrawHUD(screen, HUDFromSnapshot(
		v.LeftName, v.RightName, v.LeftColor, v.RightColor,
		v.LeftScore, v.RightScore, v.ClockSeconds, v.PhaseLabel,
		v.InShootout, v.PenLeftGoals, v.PenLeftTaken, v.PenRightGoals, v.PenRightTaken))
	DrawClientOverlays(screen, v.Celebrating, v.PhaseLabel, v.GoalText,
		v.GoalTint, ballPos, v.Geometry.ScreenWidth, v.Geometry.ScreenHeight,
		v.Finished, v.WinnerText)
	if showStats {
		StatsPanel(screen, StatsModelFromStats(v.Stats, v.LeftName, v.RightName, v.LeftColor, v.RightColor))
	}
	if v.Paused {
		ui := BeginUI(screen)
		ui.DimScreen(color.RGBA{0, 0, 0, 120})
		ui.Title("PAUSED BY HOST", UIWidth/2, UIHeight/2-24, 44, color.RGBA{240, 244, 240, 255})
	}
	return vp
}

// PlayerSelfMarker draws a small downward chevron above a player to mark "you" on a crowded,
// possibly-laggy networked pitch. Uses the same canvas transform PlayerAt just drew with.
func PlayerSelfMarker(screen *ebiten.Image, pos geom.Vec, radius float64) {
	c := newCanvas(screen)
	y := pos.Y - radius - 9
	w := radius * 0.5
	col := color.RGBA{255, 235, 90, 255} // bright yellow, distinct from both team colours
	c.line(pos.X-w, y, pos.X, y+w*0.85, 2.6, col)
	c.line(pos.X+w, y, pos.X, y+w*0.85, 2.6, col)
}

// pitchGlowAdd is the grass tint baked into a team colour by glowColor. Tuned so the corrected
// (alpha-blended) glow lands on the same vivid cyan (blue team) / peach (red team) the old additive
// bug produced over the pitch.
var pitchGlowAdd = color.RGBA{50, 120, 50, 0}

// glowColor reproduces how the player ability glows (trap aura, push ring, shoot charge) LOOKED
// under the old premultiplied-alpha bug: that bug ADDED the team colour over the pitch, so a blue
// glow read cyan and a red one read peach. We bake that additive-over-grass look into a proper
// OPAQUE base colour here, so fade()/fadeU8() can then make it correctly translucent (RGB scaled by
// alpha) -- the same vivid colour as before, but it now actually fades instead of glowing additively.
func glowColor(body color.RGBA) color.RGBA {
	add := func(a, b uint8) uint8 {
		if v := int(a) + int(b); v < 255 {
			return uint8(v)
		}
		return 255
	}
	return color.RGBA{add(body.R, pitchGlowAdd.R), add(body.G, pitchGlowAdd.G), add(body.B, pitchGlowAdd.B), 255}
}

// drawPushPulses draws the middle-click push effect. For each player whose push just fired
// (PushFlash > 0, set on every attempt -- even a whiff with no ball in reach), it paints an
// expanding ring centred on the PLAYER that travels outward as the press fades, so it reads as a
// shockwave radiating from the jabbing player. Drawn BEFORE the ball and players (see Match/Frame)
// so the ring renders UNDER them -- it wells up from beneath the player instead of covering it.
func drawPushPulses(screen *ebiten.Image, m *sim.Match) {
	c := newCanvas(screen)
	for _, p := range m.Players {
		flash := p.PushFlash()
		if flash <= 0 {
			continue
		}
		drawPushPulse(c, p.Position, p.Radius(), p.PushRange(), flash, p.Team.Color)
	}
}

// drawPushPulse draws one middle-click push as an expanding RING of CONSTANT thickness centred on
// the player -- a dissipating shockwave. The ring is a bright thin pulse while it is small at the
// player, then bursts outward (easeOut) and FADES as it grows: opacity falls on an easeOut curve, so
// the bigger the ring gets the fainter it is, leaving it ~invisible by the time it reaches full
// extent -- it never pops out or appears to brighten. Its thickness stays FIXED however far it has
// expanded. `flash` is the 1->0 press timer (sim.Player.PushFlash); `body` is the pushing team's
// colour, matching the trap-aura and shoot-charge tints.
func drawPushPulse(c canvas, center geom.Vec, innerRadius, pullRange, flash float64, body color.RGBA) {
	if flash <= 0 {
		return
	}
	// easeOut EXPANSION: the ring bursts outward quickly then decelerates as it dissipates (a real
	// shockwave covers most of its distance early), so radius = surface + (1 - flash^2) * travel.
	radius := innerRadius + (1-flash*flash)*(2*pullRange)
	if radius < 0.5 {
		return
	}
	const thickness = 2.5   // CONSTANT, THIN ring (world units) -- independent of how far it has expanded
	const peakAlpha = 235.0 // a bright pulse while the ring is still small at the player...
	// ...that fades on an easeOut curve (flash^2): brightest when small, dropping FAST as the ring
	// grows so the bigger it gets the FAINTER it is, ~invisible by full extent -- a dissipating
	// shockwave that never pops out or appears to brighten. fade() handles the premultiply (RGB
	// scaled by alpha); glowColor gives it the SAME tint as the trap aura.
	alpha := (peakAlpha / 255.0) * flash * flash // straight alpha, 0..1
	if alpha <= 0.003 {
		return
	}
	c.strokeCircle(center.X, center.Y, radius, thickness, fade(glowColor(body), alpha))
}

// drawTrapAura draws a soft glow ring around a player while it traps. `level` is the trap's
// effective strength (sim.Player.TrapAura): it swells to a max as the trap is held, weakens as the
// trap is over-held (so the circle grows then shrinks), and shrinks to nothing on release. Both
// the reach and the opacity track it -- and the trap MECHANIC uses this same level, so the glow's
// size and intensity match what the trap is actually doing.
func drawTrapAura(c canvas, pos geom.Vec, radius, level float64, body color.RGBA) {
	if level <= 0 {
		return
	}
	// A glow drawn as a stack of thin concentric bands in the cyan/peach glowColor: opacity is
	// FULLEST at the body (most opaque, right at the player) and FADES OUT toward the outer rim, so
	// the halo is strongest at its source and dissolves the farther away it gets. The profile is
	// fixed and SIZE-INDEPENDENT -- only the reach scales with the aura level. fade() makes each band
	// correctly translucent.
	const bands = 28
	const innerA = 0.93                     // straight alpha at the body (inner edge) -- fullest near the player
	const outerA = 0.22                     // straight alpha at the outer rim -- faded but still PRESENT (not gone)
	reach := 4 + 18*level                   // SIZE tracks the aura level (grows then shrinks); opacity does NOT
	width := reach / float64(bands-1) * 1.2 // thin bands with a hair of overlap so they meet seamlessly
	glow := glowColor(body)
	for i := 0; i < bands; i++ {
		t := float64(i) / float64(bands-1) // 0 at the body, 1 at the outer rim
		r := radius + reach*t
		af := innerA + (outerA-innerA)*t // falls: fullest at the body, fading out toward the rim
		if af <= 0 {
			continue
		}
		c.strokeCircle(pos.X, pos.Y, r, width, fade(glow, af))
	}
}

// ShowPossessionBars toggles the on-screen test bars above each player (player possession +
// team possession charge). On by default; flip to false to hide them.
var ShowPossessionBars = true

// drawPossessionBarsAll draws the per-player test bars over an already-drawn match: the
// player's own possession and that player's team possession charge (the touch coefficient).
func drawPossessionBarsAll(screen *ebiten.Image, m *sim.Match) {
	if !ShowPossessionBars {
		return
	}
	c := newCanvas(screen)
	for _, p := range m.Players {
		drawPossessionBars(c, p.Position, p.Radius(),
			p.Possession(), p.TouchCoefficient())
	}
}

// drawPossessionBars draws two small test bars above a player: the TOP bar is the player's own
// possession (0..1, white), the BOTTOM bar is the player's TOUCH COEFFICIENT magnitude (0..1) --
// green while boosted (positive), red while conceding (negative). The coefficient folds in BOTH
// drains, so the bar reflects them live: the per-player contact drain shrinks one boosted player's
// green bar when an opponent marks it, and the team-wide debuff drain (a defender on the contested
// ball) shrinks the conceding team's RED bars toward empty as their debuff is relieved -- then they
// flip green on handover. For tuning/testing visibility of the possession mechanic.
func drawPossessionBars(c canvas, pos geom.Vec, radius, playerPoss, coef float64) {
	const w, h, gap = 26.0, 3.0, 1.5
	clamp01 := func(v float64) float64 {
		if v < 0 {
			return 0
		}
		if v > 1 {
			return 1
		}
		return v
	}
	x := pos.X - w/2
	y := pos.Y - radius - 13
	bg := color.RGBA{0, 0, 0, 130}

	c.fillRect(x, y, w, h, bg)
	c.fillRect(x, y, w*clamp01(playerPoss), h, fade(color.RGBA{240, 240, 240, 255}, 225.0/255))

	y2 := y + h + gap
	c.fillRect(x, y2, w, h, bg)
	fill := color.RGBA{90, 220, 100, 235} // green: this player is boosted
	if coef < 0 {
		fill = color.RGBA{225, 80, 80, 235} // red: this player is on the conceding side
	}
	mag := coef // bar length tracks the per-player coefficient magnitude (folds in the contact drain)
	if mag < 0 {
		mag = -mag
	}
	c.fillRect(x, y2, w*clamp01(mag), h, fill)
}

// ShowTrapBars toggles the trap-energy bar above each player (the right-click "good touch"
// resource). On by default; drawn on both the local and networked render paths, over ALL players.
var ShowTrapBars = true

// trapBarFill is the cyan fill of the trap-energy bar.
var trapBarFill = color.RGBA{80, 210, 230, 235}

// drawTrapEnergyBar draws a small bar above a player showing its 0..1 trap ENERGY: full = ready,
// draining while the trap is held, regenerating otherwise. Sits one slot ABOVE the possession test
// bars so they never overlap.
func drawTrapEnergyBar(c canvas, pos geom.Vec, radius, energy float64) {
	if !ShowTrapBars {
		return
	}
	const w, h, gap = 26.0, 3.0, 1.5
	e := energy
	if e < 0 {
		e = 0
	} else if e > 1 {
		e = 1
	}
	x := pos.X - w/2
	y := pos.Y - radius - 13 - (h + gap) // one slot above the possession bars
	c.fillRect(x, y, w, h, color.RGBA{0, 0, 0, 130})
	c.fillRect(x, y, w*e, h, trapBarFill)
}

// drawTrapBarsAll draws the trap-energy bar over every player of an already-drawn local match.
func drawTrapBarsAll(screen *ebiten.Image, m *sim.Match) {
	if !ShowTrapBars {
		return
	}
	c := newCanvas(screen)
	for _, p := range m.Players {
		drawTrapEnergyBar(c, p.Position, p.Radius(), p.TrapCharge())
	}
}

// drawShootCharge draws the power gauge as a 180deg arc over the FRONT hemisphere the player
// faces (matching where the left-click shot can fire). The fill loads from BOTH edges
// (+-90deg off the facing) inward and meets in the middle (dead front) at full charge,
// brightening toward full.
func drawShootCharge(c canvas, pos, facing geom.Vec, radius, charge float64, body color.RGBA) {
	if charge <= 0 {
		return
	}
	r := radius + 5
	f := -math.Pi / 2 // fallback front = up if facing is unset
	if geom.Norm(facing) > 0 {
		f = math.Atan2(facing.Y, facing.X)
	}
	// Faint outline of the front 180deg hemisphere (centred on the facing direction). additiveGlow
	// reproduces the classic additive team-coloured look EXACTLY (full rgb + low alpha) -- this gauge
	// is meant to read as an additive glow, so it deliberately does NOT use the premultiplied fade.
	strokeArc(c, pos, r, f-math.Pi/2, f+math.Pi/2, 2, additiveGlow(body, 70))
	// Two fill arcs grow inward from the +-90deg edges, meeting at the middle (f) at full charge.
	arc := additiveGlow(body, uint8(150+105*charge))
	sweep := (math.Pi / 2) * charge
	strokeArc(c, pos, r, f-math.Pi/2, f-math.Pi/2+sweep, 3, arc) // from the -90deg edge toward front
	strokeArc(c, pos, r, f+math.Pi/2, f+math.Pi/2-sweep, 3, arc) // from the +90deg edge toward front
}

// strokeArc draws an arc as a run of short line segments (the vector package has no
// arc primitive).
func strokeArc(c canvas, center geom.Vec, r, a0, a1, w float64, clr color.Color) {
	const seg = 28
	prev := geom.NewVec(center.X+r*math.Cos(a0), center.Y+r*math.Sin(a0))
	for i := 1; i <= seg; i++ {
		t := a0 + (a1-a0)*float64(i)/seg
		cur := geom.NewVec(center.X+r*math.Cos(t), center.Y+r*math.Sin(t))
		c.line(prev.X, prev.Y, cur.X, cur.Y, w, clr)
		prev = cur
	}
}

// ---------------------------------------------------------------------------
// In-game HUD model and drawer.
// ---------------------------------------------------------------------------

// formatClock renders seconds as mm:ss.
func formatClock(sec float64) string {
	if sec < 0 {
		sec = 0
	}
	t := int(sec)
	ss := t % 60
	mm := t / 60
	sstr := itoa(ss)
	if ss < 10 {
		sstr = "0" + sstr
	}
	return itoa(mm) + ":" + sstr
}

// regularPolygon returns the vertices of a regular polygon.
func regularPolygon(center geom.Vec, radius float64, sides int, rotation float64) []geom.Vec {
	points := make([]geom.Vec, sides)
	for i := 0; i < sides; i++ {
		a := rotation + 2*math.Pi*float64(i)/float64(sides)
		points[i] = geom.NewVec(center.X+radius*math.Cos(a), center.Y+radius*math.Sin(a))
	}
	return points
}

// itoa renders a small integer without importing fmt.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
