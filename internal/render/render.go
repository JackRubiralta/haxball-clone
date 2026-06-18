// Package render draws the game with Ebiten. It is the only internal package (other
// than the human controller) that imports Ebiten, keeping graphics out of the
// simulation. It exposes small primitives so both the local game (drawing a
// sim.Match) and the network client (drawing a server snapshot) share the same look.
package render

import (
	"image/color"
	"math"
	"time"

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
	stadiumColor = color.RGBA{18, 46, 26, 255}    // outside the pitch
	stripeA      = color.RGBA{46, 138, 54, 255}   // mowed grass band
	stripeB      = color.RGBA{52, 150, 60, 255}   // alternate band
	lineColor    = color.RGBA{235, 240, 235, 255} // pitch markings
	netFill      = color.RGBA{14, 18, 24, 150}    // dark goal interior
	netLine      = color.RGBA{120, 132, 140, 130} // off-colour (cool grey) net mesh, not white
	ballWhite    = color.RGBA{248, 248, 248, 255}
	ballOutline  = color.RGBA{60, 60, 66, 255}
	seamColor    = color.RGBA{44, 44, 52, 255}
	outlineColor = color.RGBA{28, 28, 36, 255}
	coneOuter    = color.RGBA{235, 120, 30, 255}
	coneInner    = color.RGBA{252, 206, 130, 255}
	hudColor     = color.RGBA{0, 0, 0, 110}
	bannerColor  = color.RGBA{0, 0, 0, 165}
	hudText      = color.RGBA{240, 244, 240, 255} // legible HUD text
	hudDim       = color.RGBA{210, 218, 210, 200} // secondary HUD text (controls hint)
	offsideLine  = color.RGBA{235, 240, 235, 105} // translucent white anti-camp line
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

// view is the most recent world->framebuffer transform, kept so ScreenToWorld can
// invert a cursor position. It is refreshed every time a frame is drawn.
var view = canvas{scale: 1}

// worldW and worldH are the logical world size the canvas fits to the window. They
// default to the standard surface and are set from the field's geometry whenever a
// field is drawn, so a pitch of any size is letterboxed correctly.
var worldW, worldH float64 = ScreenWidth, ScreenHeight

// Camera state. When camActive is set (during Frame's world pass), newCanvas applies a
// pan/zoom transform centred on camCenter; otherwise it fits the world to the window
// (the original look, used for the HUD and the network client). Either way it refreshes
// view, so ScreenToWorld inverts whichever transform is current and aim stays correct.
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

func newCanvas(dst *ebiten.Image) canvas {
	w := float64(dst.Bounds().Dx())
	h := float64(dst.Bounds().Dy())
	base := math.Min(w/worldW, h/worldH)
	if base <= 0 {
		base = 1
	}
	var c canvas
	if camActive {
		// Pan/zoom: scale by zoom and centre the view on camCenter. At zoom 1 with
		// camCenter at the world centre this is identical to the fit transform below.
		scale := base * camZoom
		c = canvas{dst: dst, scale: scale, ox: w/2 - camCenter.X*scale, oy: h/2 - camCenter.Y*scale}
	} else {
		c = canvas{dst: dst, scale: base, ox: (w - worldW*base) / 2, oy: (h - worldH*base) / 2}
	}
	view = c
	return c
}

// newHUDCanvas builds a fit-to-window canvas for the HUD. It never pans or zooms and,
// crucially, does NOT touch view -- so a HUD drawn after the world leaves view holding
// the world (camera) transform that ScreenToWorld inverts for aim.
func newHUDCanvas(dst *ebiten.Image) canvas {
	w := float64(dst.Bounds().Dx())
	h := float64(dst.Bounds().Dy())
	base := math.Min(w/worldW, h/worldH)
	if base <= 0 {
		base = 1
	}
	return canvas{dst: dst, scale: base, ox: (w - worldW*base) / 2, oy: (h - worldH*base) / 2}
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
// align picks the horizontal anchor at x: AlignLeft/AlignCenter/AlignRight. The
// baseline is vertically centred on y. Centring uses text.BoundString -- no
// len(s)*k fudging -- so it is correct for any glyphs.
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
	oy := float64(c.py(y)) - float64(b.Min.Y) - float64(b.Dy())/2
	text.Draw(c.dst, s, face, int(px), int(oy), clr)
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

// ScreenToWorld converts a framebuffer (cursor) coordinate back to world space using
// the most recent frame's transform.
func ScreenToWorld(x, y int) geom.Vec {
	return geom.NewVec((float64(x)-view.ox)/view.scale, (float64(y)-view.oy)/view.scale)
}

// UIWidth and UIHeight are the fixed logical size menus lay out in, independent of the
// pitch and scaled to the window exactly like the pitch is.
const (
	UIWidth  = 1000
	UIHeight = 680
)

// UI is an immediate-mode drawing surface for menus, laid out in fixed UI coordinates
// and scaled to the window. Menus draw through it so they never touch Ebiten or the
// vector package directly, keeping the transform single-sourced.
type UI struct{ c canvas }

// BeginUI prepares a UI surface for this frame. It sets the transform that
// ScreenToWorld inverts, so a menu can map the cursor into UI coordinates.
func BeginUI(screen *ebiten.Image) UI {
	worldW, worldH = UIWidth, UIHeight
	return UI{c: newCanvas(screen)}
}

// Fill clears the whole surface to a colour.
func (u UI) Fill(clr color.Color) { u.c.dst.Fill(clr) }

// FillRect draws a filled rectangle in UI coordinates.
func (u UI) FillRect(x, y, w, h float64, clr color.Color) { u.c.fillRect(x, y, w, h, clr) }

// StrokeRect draws a rectangle outline in UI coordinates.
func (u UI) StrokeRect(x, y, w, h, sw float64, clr color.Color) { u.c.strokeRect(x, y, w, h, sw, clr) }

// Text draws left-aligned text at a UI position.
func (u UI) Text(s string, x, y float64) { u.c.text(s, x, y, 0, 0) }

// TextCentered draws text centred horizontally on cx.
func (u UI) TextCentered(s string, cx, y float64) { u.c.text(s, cx, y, -len(s)*3, 0) }

// TextRight draws text ending near x (rough right alignment for the debug font).
func (u UI) TextRight(s string, x, y float64) { u.c.text(s, x, y, -len(s)*6, 0) }

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

// Match draws a complete local match.
func Match(screen *ebiten.Image, m *sim.Match) {
	Field(screen, m.Field, m.Teams[0].Color, m.Teams[1].Color)
	drawPokePulses(screen, m) // under the ball and players, so the burst wells up from beneath the player instead of covering it
	BallAt(screen, m.Ball.Position, m.Ball.Radius())
	for _, p := range m.Players {
		PlayerAt(screen, p.Position, p.Facing, p.Radius(), p.Team.Color, p.Number,
			sim.NormShootCharge(p.ShootCharge()), p.TrapAura())
	}
	drawPossessionBarsAll(screen, m)
	DrawHUD(screen, hudFromMatch(m))
	drawMatchOverlays(screen, m)
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
		CenterBanner(screen, "P A U S E D")
	case m.Finished():
		CenterBanner(screen, winnerMessage(m))
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
func Frame(screen *ebiten.Image, m *sim.Match, cam *Camera, dt float64) {
	worldW, worldH = m.Field.Geo.ScreenWidth, m.Field.Geo.ScreenHeight
	cam.prepare(worldW, worldH, m, dt)

	camActive, camCenter, camZoom = true, cam.center, cam.Zoom
	Field(screen, m.Field, m.Teams[0].Color, m.Teams[1].Color)
	drawPokePulses(screen, m) // under the ball and players, so the burst wells up from beneath the player instead of covering it
	BallAt(screen, m.Ball.Position, m.Ball.Radius())
	for _, p := range m.Players {
		PlayerAt(screen, p.Position, p.Facing, p.Radius(), p.Team.Color, p.Number,
			sim.NormShootCharge(p.ShootCharge()), p.TrapAura())
	}
	drawPossessionBarsAll(screen, m)
	drawZoneIndicators(newCanvas(screen), m.Field, m.Rules)
	camActive = false

	DrawHUD(screen, hudFromMatch(m))
	drawMatchOverlays(screen, m)
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

// Field draws the pitch: a striped lawn, boundary and markings, the two goals with
// nets, and any obstacles.
func Field(screen *ebiten.Image, f *sim.Field, leftColor, rightColor color.RGBA) {
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
	// collision surface) -- it no longer pokes past it and overlaps a wall.
	const outlineWidth = 3.0
	c.strokeCircle(pos.X, pos.Y, radius-outlineWidth/2, outlineWidth, outlineColor)

	if geom.Norm(facing) > 0 {
		nose := pos.Add(facing.Scale(radius * 0.66))
		c.fillCircle(nose.X, nose.Y, radius*0.22, ballWhite)
	}

	c.number(itoa(number), pos.X, pos.Y, radius, ballWhite)
	drawShootCharge(c, pos, facing, radius, shootCharge, body) // power gauge over the body
}

// drawPokePulses draws the middle-click poke effect. For each player whose poke just fired
// (PokeFlash > 0, set on every attempt -- even a whiff with no ball in reach), it paints a soft
// burst centred on the PLAYER that expands outward in all directions as the press fades -- the
// original player-anchored poke ping (rather than the ball-anchored variant), so it reads as a
// shockwave radiating from the jabbing player. Drawn BEFORE the ball and players (see Match/Frame)
// so the burst renders UNDER them -- it wells up from beneath the player instead of covering it.
func drawPokePulses(screen *ebiten.Image, m *sim.Match) {
	c := newCanvas(screen)
	for _, p := range m.Players {
		flash := p.PokeFlash()
		if flash <= 0 {
			continue
		}
		drawPokePulse(c, p.Position, p.Radius(), p.PokeRange(), flash, p.Team.Color)
	}
}

// drawPokePulse draws one middle-click poke as a soft, semi-transparent burst centred on the
// PLAYER: it starts at the player's surface and swells outward to ~2x the pull range in all
// directions as the press fades -- brightest near the player and fading to nothing at its leading
// edge, so it reads as a shockwave radiating out from the jabbing player. `flash` is the 1->0 press
// timer (sim.Player.PokeFlash); `body` is the poking team's colour (the blue team -> blue), matching
// the trap-aura and shoot-charge tints. This is the original player-anchored expanding ring, kept
// blue, distance-faded, and bounded to ~2x the pull reach.
func drawPokePulse(c canvas, center geom.Vec, innerRadius, pullRange, flash float64, body color.RGBA) {
	if flash <= 0 {
		return
	}
	prog := 1 - flash                         // 0 at the press, 1 when the pulse ends
	reach := innerRadius + prog*(2*pullRange) // outer radius: from the player surface out to ~2x pull range
	const bands = 18
	const peakAlpha = 130.0 // "a bit opaque": semi-transparent even at its brightest
	width := reach / float64(bands-1) * 1.25
	for i := 0; i < bands; i++ {
		t := float64(i) / float64(bands-1) // 0 at the player centre, 1 at the leading edge
		r := reach * t
		if r < 0.5 {
			continue
		}
		// Opacity is highest near the player and FADES the farther out the band sits (1-t); the whole
		// burst also fades over the press timer (flash) -- so it dies away as it expands outward.
		a := uint8(peakAlpha * (1 - t) * flash)
		if a == 0 {
			continue
		}
		c.strokeCircle(center.X, center.Y, r, width, color.RGBA{body.R, body.G, body.B, a})
	}
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
	// A glow drawn as a stack of thin concentric bands whose opacity RISES along the disc: faint
	// at the body (inner edge) and brightest at the outer rim. The opacity gradient is fixed and
	// SIZE-INDEPENDENT -- only the reach scales with the aura level, so a small disc and a big one
	// look identical in opacity (one is just bigger), and the bright rim reads as an expanding ring.
	const bands = 24
	const innerAlpha = 8.0                  // opacity at the body (inner edge)
	const outerAlpha = 70.0                 // opacity at the outer rim (brightest -- opacity rises outward)
	reach := 4 + 16*level                   // SIZE tracks the aura level (grows then shrinks); opacity does NOT
	width := reach / float64(bands-1) * 1.1 // thin bands with a hair of overlap so they meet seamlessly
	for i := 0; i < bands; i++ {
		t := float64(i) / float64(bands-1)                 // 0 at the body, 1 at the outer rim
		r := radius + reach*t                              //
		a := uint8(innerAlpha + (outerAlpha-innerAlpha)*t) // rises 8 -> 70 outward; independent of disc size
		if a == 0 {
			continue
		}
		c.strokeCircle(pos.X, pos.Y, r, width, color.RGBA{body.R, body.G, body.B, a})
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
	c.fillRect(x, y, w*clamp01(playerPoss), h, color.RGBA{240, 240, 240, 225})

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
	// Faint outline of the front 180deg hemisphere (centred on the facing direction).
	strokeArc(c, pos, r, f-math.Pi/2, f+math.Pi/2, 2, color.RGBA{body.R, body.G, body.B, 70})
	// Two fill arcs grow inward from the +-90deg edges, meeting at the middle (f) at full charge.
	arc := color.RGBA{body.R, body.G, body.B, uint8(150 + 105*charge)}
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

// HUDModel is the plain, render-agnostic data the HUD needs. It is built identically
// from a local *sim.Match (hudFromMatch) or a network snapshot (HUDFromSnapshot via the
// client), so a single DrawHUD covers every draw path with no new snapshot fields.
type HUDModel struct {
	LeftName, RightName   string
	LeftColor, RightColor color.RGBA
	LeftScore, RightScore int
	ClockSeconds          float64
	Phase                 string // scoreboard phase label ("" during ordinary play)

	InShootout bool
	// Shootout result dots, oldest first, per side. true = scored, false = missed.
	LeftDots, RightDots []bool
	// Fallback tallies when no per-kick detail is available (network path).
	LeftPenGoals, LeftPenTaken   int
	RightPenGoals, RightPenTaken int
}

// hudFromMatch builds the HUD model from a local match.
func hudFromMatch(m *sim.Match) HUDModel {
	h := HUDModel{
		LeftName:     m.Teams[0].Name,
		RightName:    m.Teams[1].Name,
		LeftColor:    m.Teams[0].Color,
		RightColor:   m.Teams[1].Color,
		LeftScore:    m.Teams[0].Score,
		RightScore:   m.Teams[1].Score,
		ClockSeconds: m.ClockSeconds(),
		Phase:        m.PhaseLabel(),
		InShootout:   m.InShootout(),
	}
	if h.InShootout {
		lg, rg := m.ShootoutScore()
		lt, rt := m.ShootoutTaken()
		h.LeftPenGoals, h.LeftPenTaken = lg, lt
		h.RightPenGoals, h.RightPenTaken = rg, rt
		for _, k := range m.ShootoutKicks() {
			if k.Side == m.Teams[0].Side {
				h.LeftDots = append(h.LeftDots, k.Scored)
			} else {
				h.RightDots = append(h.RightDots, k.Scored)
			}
		}
	}
	return h
}

// HUDFromSnapshot builds the HUD model from a network snapshot's already-present fields
// (no new snapshot data). The client passes the scoreboard strings/colours/scores it
// receives; shootout detail falls back to the goal/taken tallies.
func HUDFromSnapshot(leftName, rightName string, leftColor, rightColor color.RGBA,
	leftScore, rightScore int, clockSeconds float64, phase string,
	inShootout bool, leftPenGoals, leftPenTaken, rightPenGoals, rightPenTaken int) HUDModel {
	return HUDModel{
		LeftName: leftName, RightName: rightName,
		LeftColor: leftColor, RightColor: rightColor,
		LeftScore: leftScore, RightScore: rightScore,
		ClockSeconds: clockSeconds, Phase: phase,
		InShootout:   inShootout,
		LeftPenGoals: leftPenGoals, LeftPenTaken: leftPenTaken,
		RightPenGoals: rightPenGoals, RightPenTaken: rightPenTaken,
	}
}

// HUD layout constants (UI/world units, scaled by the canvas).
const (
	hudBarH   = 44.0
	hudChipW  = 188.0
	hudGap    = 14.0
	hudScoreS = 30.0 // big vector score size
	hudNameS  = 16.0
	hudClockS = 17.0
	hudPhaseS = 13.0
)

var (
	hudPanel  = color.RGBA{16, 22, 28, 180}
	hudScored = color.RGBA{96, 220, 110, 255} // shootout scored dot
	hudMissed = color.RGBA{210, 78, 78, 255}  // shootout missed dot
	hudEmpty  = color.RGBA{210, 218, 210, 80} // untaken dot
)

// DrawHUD draws the in-game top tally: two team chips (colour block + name + procedural
// badge), the big vector score between them, the clock on the left and the phase on the
// right, plus a shootout sub-row of scored/missed dots. It is fit-to-window (never pans
// or zooms) so it stays put under the camera.
func DrawHUD(screen *ebiten.Image, h HUDModel) {
	c := newHUDCanvas(screen)
	w := worldW

	// Top bar background.
	c.fillRect(0, 0, w, hudBarH, hudPanel)

	cx := w / 2
	// Big central score "L - R".
	score := itoa(h.LeftScore) + "  -  " + itoa(h.RightScore)
	c.textSized(score, cx, hudBarH/2, hudScoreS, AlignCenter, hudText)
	scoreHalf := measureUI(score, hudScoreS) / 2

	// Left team chip, right-anchored against the score.
	chipPad := 12.0
	leftChipR := cx - scoreHalf - hudGap
	drawTeamChip(c, h.LeftName, h.LeftColor, leftChipR-hudChipW, 4, hudChipW, hudBarH-8, false)
	// Right team chip, left-anchored against the score.
	rightChipL := cx + scoreHalf + hudGap
	drawTeamChip(c, h.RightName, h.RightColor, rightChipL, 4, hudChipW, hudBarH-8, true)

	// Clock (left) with a small clock icon, and the phase label (right).
	c.iconClock(chipPad+9, hudBarH/2, 18, hudDim)
	c.textSized(formatClock(h.ClockSeconds), chipPad+24, hudBarH/2, hudClockS, AlignLeft, hudText)
	if h.Phase != "" {
		c.textSized(h.Phase, w-chipPad, hudBarH/2, hudPhaseS, AlignRight, hudColorForPhase(h.Phase))
	}

	if h.InShootout {
		drawShootoutRow(c, h, w)
	}

	// Controls hint along the bottom.
	c.textSized("WASD move  -  mouse aim  -  hold left-click to charge shot  -  right-click to trap  -  Esc pause",
		10, worldH-14, 12, AlignLeft, hudDim)
}

// drawTeamChip draws a colour block, the team name, and a procedural shield badge. When
// mirror is set the badge sits on the right (for the right-hand team) so the two chips
// face inward toward the score.
func drawTeamChip(c canvas, name string, col color.RGBA, x, y, w, h float64, mirror bool) {
	c.fillRect(x, y, w, h, withAlpha(col, 60))
	c.strokeRect(x, y, w, h, 1.5, withAlpha(col, 200))
	badgeR := h * 0.8
	pad := 8.0
	badgeX := x + pad + badgeR/2
	nameX := x + pad*2 + badgeR
	nameAlign := AlignLeft
	if mirror {
		badgeX = x + w - pad - badgeR/2
		nameX = x + w - pad*2 - badgeR
		nameAlign = AlignRight
	}
	c.iconShield(badgeX, y+h/2, badgeR, col, outlineColor)
	// Clip the name to the chip with a simple width budget.
	c.textSized(fitText(name, w-badgeR-pad*3, hudNameS), nameX, y+h/2, hudNameS, nameAlign, hudText)
}

// drawShootoutRow draws the penalties sub-bar: a "PENALTIES" tag and a row of result dots
// per side (scored = green, missed = red, untaken slots = faint).
func drawShootoutRow(c canvas, h HUDModel, w float64) {
	const rowY = hudBarH
	const rowH = 22.0
	c.fillRect(0, rowY, w, rowH, hudPanel)
	c.textSized("PENALTIES", w/2, rowY+rowH/2, 12, AlignCenter, hudDim)

	dotR := 4.0
	gap := 11.0
	cy := rowY + rowH/2
	// Left dots grow leftward from centre-left; right dots grow rightward from centre-right.
	left := dotsFor(h.LeftDots, h.LeftPenGoals, h.LeftPenTaken)
	right := dotsFor(h.RightDots, h.RightPenGoals, h.RightPenTaken)
	startL := w/2 - 70
	for i, scored := range left {
		c.fillCircle(startL-float64(i)*gap, cy, dotR, dotColor(scored))
	}
	startR := w/2 + 70
	for i, scored := range right {
		c.fillCircle(startR+float64(i)*gap, cy, dotR, dotColor(scored))
	}
}

// dotsFor returns the per-kick scored flags, falling back to reconstructing them from the
// goal/taken tallies when no per-kick detail is available (the network path): the first
// `goals` are scored, the rest missed.
func dotsFor(detail []bool, goals, taken int) []bool {
	if len(detail) > 0 {
		return detail
	}
	out := make([]bool, 0, taken)
	for i := 0; i < taken; i++ {
		out = append(out, i < goals)
	}
	return out
}

func dotColor(scored bool) color.RGBA {
	if scored {
		return hudScored
	}
	return hudMissed
}

// hudColorForPhase tints the phase label so a sudden-death stage reads as urgent.
func hudColorForPhase(phase string) color.RGBA {
	switch phase {
	case "GOLDEN GOAL", "PENALTIES":
		return color.RGBA{255, 196, 90, 255}
	case "FULL TIME":
		return color.RGBA{255, 120, 120, 255}
	default:
		return hudText
	}
}

// fitText trims s with an ellipsis so its rendered width stays within maxW (UI units).
func fitText(s string, maxW, sizeUI float64) string {
	if measureUI(s, sizeUI) <= maxW {
		return s
	}
	for len(s) > 1 {
		s = s[:len(s)-1]
		if measureUI(s+"…", sizeUI) <= maxW {
			return s + "…"
		}
	}
	return s
}

// ---------------------------------------------------------------------------
// Client-side, cosmetic FX: animated goal overlay and stage-card transitions.
// ---------------------------------------------------------------------------

// fxState holds the client-only celebration/stage animators. The timers advance by wall
// time (render is client-only and never linked into the headless server, so this does
// not affect simulation determinism). They are edge-triggered off the match's
// Celebrating()/Phase() so a rising edge restarts the animation.
type fxState struct {
	lastTick    time.Time
	wasCeleb    bool
	celebT      float64 // seconds since the goal celebration began (0 = none)
	lastPhase   sim.Phase
	phaseLabel0 string  // last observed snapshot phase label (client edge-detect)
	phaseT      float64 // seconds since the last stage change (counts up; card shown while < stageCardSeconds)
	phaseLabel  string  // the label to show on the stage card
	initialised bool
}

const (
	celebDuration    = 2.4 // goal overlay lifetime (seconds)
	stageCardSeconds = 2.0 // stage-card fade-in/out lifetime (seconds)
)

var fx fxState

// advanceFX ticks the cosmetic FX clock by the real elapsed time since the last draw.
func advanceFX() {
	now := time.Now()
	if fx.lastTick.IsZero() {
		fx.lastTick = now
		return
	}
	dt := now.Sub(fx.lastTick).Seconds()
	fx.lastTick = now
	if dt < 0 {
		dt = 0
	}
	if dt > 0.1 {
		dt = 0.1 // clamp a long stall so the animation does not jump
	}
	if fx.celebT > 0 {
		fx.celebT += dt
		if fx.celebT > celebDuration {
			fx.celebT = 0
		}
	}
	if fx.phaseT >= 0 && fx.phaseT < stageCardSeconds {
		fx.phaseT += dt
	}
}

// observe edge-detects the celebration and the phase so the FX timers restart on a rising
// edge. The phase that ENTERS a sudden-death/finished stage shows a card.
func (s *fxState) observe(celebrating bool, phase sim.Phase) {
	if celebrating && !s.wasCeleb {
		s.celebT = 0.0001 // arm
	}
	s.wasCeleb = celebrating
	if !s.initialised {
		s.lastPhase = phase
		s.initialised = true
		return
	}
	if phase != s.lastPhase {
		s.lastPhase = phase
		if label := stageCardLabel(phase); label != "" {
			s.phaseLabel = label
			s.phaseT = 0
		}
	}
}

// stageCardLabel returns the full-screen card text for a phase transition, or "" if that
// phase does not warrant a card.
func stageCardLabel(p sim.Phase) string {
	switch p {
	case sim.PhaseExtraTime:
		return "EXTRA TIME"
	case sim.PhaseGoldenGoal:
		return "GOLDEN GOAL — next goal wins"
	case sim.PhasePenalties:
		return "PENALTIES"
	case sim.PhaseFinished:
		return "FULL TIME"
	default:
		return ""
	}
}

// drawGoalOverlay draws the animated goal celebration: an expanding fading ring at the
// goal spot plus a scale-in/fade banner with the scorer/assist message, tinted to the
// scoring team. It is keyed off the client-side celebT timer.
func drawGoalOverlay(screen *ebiten.Image, message string, tint color.RGBA, ballPos geom.Vec, sw, sh float64) {
	if fx.celebT <= 0 {
		return
	}
	prog := fx.celebT / celebDuration
	if prog > 1 {
		prog = 1
	}
	// Expanding ring at the ball/goal spot (world space, under the camera-fit HUD canvas).
	worldW, worldH = sw, sh
	wc := newHUDCanvas(screen)
	ringR := 18 + prog*120
	alpha := uint8(200 * (1 - prog))
	const bands = 5
	for i := 0; i < bands; i++ {
		t := float64(i) / float64(bands)
		r := ringR * (0.5 + 0.5*t)
		a := uint8(float64(alpha) * (1 - t))
		if a == 0 {
			continue
		}
		wc.strokeCircle(ballPos.X, ballPos.Y, r, 3, color.RGBA{tint.R, tint.G, tint.B, a})
	}

	// Scale-in / fade banner, centred. Fades out over the back third of the lifetime.
	worldW, worldH = sw, sh
	c := newHUDCanvas(screen)
	bannerAlpha := 1.0
	if prog > 0.7 {
		bannerAlpha = 1 - (prog-0.7)/0.3
	}
	scaleIn := 1.0
	if prog < 0.18 {
		scaleIn = prog / 0.18
	}
	size := 22.0 * (0.6 + 0.4*scaleIn)
	cx, cy := worldW/2, worldH*0.34
	bw := measureUI(message, size) + 48
	bh := size + 22
	c.fillRect(cx-bw/2, cy-bh/2, bw, bh, color.RGBA{0, 0, 0, uint8(165 * bannerAlpha)})
	c.fillRect(cx-bw/2, cy-bh/2, 6, bh, color.RGBA{tint.R, tint.G, tint.B, uint8(255 * bannerAlpha)})
	c.fillRect(cx+bw/2-6, cy-bh/2, 6, bh, color.RGBA{tint.R, tint.G, tint.B, uint8(255 * bannerAlpha)})
	c.textSized(message, cx, cy, size, AlignCenter, color.RGBA{240, 244, 240, uint8(255 * bannerAlpha)})
}

// drawStageCard draws the brief full-screen transition card ("EXTRA TIME", etc.) keyed
// off the client-side phaseT timer, fading in then out over stageCardSeconds.
func drawStageCard(screen *ebiten.Image, _ sim.Phase) {
	if fx.phaseLabel == "" || fx.phaseT >= stageCardSeconds {
		return
	}
	t := fx.phaseT / stageCardSeconds // 0..1 across the card's life
	// Fade in over the first 25%, hold, fade out over the last 35%.
	alpha := 1.0
	switch {
	case t < 0.25:
		alpha = t / 0.25
	case t > 0.65:
		alpha = 1 - (t-0.65)/0.35
	}
	if alpha < 0 {
		alpha = 0
	}
	c := newHUDCanvas(screen)
	c.fillRect(0, 0, worldW, worldH, color.RGBA{8, 12, 16, uint8(150 * alpha)})
	bandH := 96.0
	cy := worldH / 2
	c.fillRect(0, cy-bandH/2, worldW, bandH, color.RGBA{12, 18, 24, uint8(210 * alpha)})
	c.fillRect(0, cy-bandH/2, worldW, 3, color.RGBA{255, 196, 90, uint8(220 * alpha)})
	c.fillRect(0, cy+bandH/2-3, worldW, 3, color.RGBA{255, 196, 90, uint8(220 * alpha)})
	c.textSized(fx.phaseLabel, worldW/2, cy, 30, AlignCenter, color.RGBA{245, 240, 230, uint8(255 * alpha)})
}

// DrawClientOverlays drives the goal overlay and stage-card transitions for the network
// client, which has no *sim.Match. It derives the same edge-triggered FX from the
// snapshot's existing fields: celebrating, the phase label, the goal text, the scoring
// team's tint (resolved by the caller), and the ball position. finished/finalText draw
// the result banner. Mirrors drawMatchOverlays for the local path.
func DrawClientOverlays(screen *ebiten.Image, celebrating bool, phaseLabel, goalText string,
	tint color.RGBA, ballPos geom.Vec, sw, sh float64, finished bool, finalText string) {
	advanceFX()
	fx.observeLabels(celebrating, phaseLabel)
	drawStageCard(screen, sim.PhasePlaying)
	if celebrating {
		msg := goalText
		if msg == "" {
			msg = "G O A L !"
		}
		drawGoalOverlay(screen, msg, tint, ballPos, sw, sh)
	}
	if finished {
		worldW, worldH = sw, sh
		CenterBanner(screen, finalText)
	}
}

// observeLabels is the snapshot-driven counterpart to observe: it edge-detects the
// celebration and the phase by its label string (the client has no sim.Phase).
func (s *fxState) observeLabels(celebrating bool, phaseLabel string) {
	if celebrating && !s.wasCeleb {
		s.celebT = 0.0001
	}
	s.wasCeleb = celebrating
	if !s.initialised {
		s.phaseLabel0 = phaseLabel
		s.initialised = true
		return
	}
	if phaseLabel != s.phaseLabel0 {
		s.phaseLabel0 = phaseLabel
		if label := stageCardLabelFor(phaseLabel); label != "" {
			s.phaseLabel = label
			s.phaseT = 0
		}
	}
}

// stageCardLabelFor maps a snapshot phase label to a stage-card message.
func stageCardLabelFor(phaseLabel string) string {
	switch phaseLabel {
	case "EXTRA TIME":
		return "EXTRA TIME"
	case "GOLDEN GOAL":
		return "GOLDEN GOAL — next goal wins"
	case "PENALTIES":
		return "PENALTIES"
	case "FULL TIME":
		return "FULL TIME"
	default:
		return ""
	}
}

// goalTint returns the scoring team's colour for the goal overlay, defaulting to white.
func goalTint(m *sim.Match) color.RGBA {
	if g := m.LastGoal; g != nil {
		if m.Teams[0].Side == g.Team {
			return m.Teams[0].Color
		}
		return m.Teams[1].Color
	}
	return color.RGBA{240, 244, 240, 255}
}

// Scoreboard draws a HUD bar with the score and the controls hint.
func Scoreboard(screen *ebiten.Image, leftName string, leftScore int, rightName string, rightScore int) {
	c := newHUDCanvas(screen)
	c.fillRect(0, 0, worldW, 28, hudColor)
	score := leftName + " " + itoa(leftScore) + "   -   " + itoa(rightScore) + " " + rightName
	c.text(score, worldW/2, 7, -len(score)*3, 0)
	c.text("WASD move  -  mouse aim  -  hold left-click to charge shot  -  right-click to trap", 10, worldH-22, 0, 0)
}

// ScoreboardWithClock draws the HUD bar with the score centred, the match clock on the
// left, and the current phase label on the right.
func ScoreboardWithClock(screen *ebiten.Image, leftName string, leftScore int, rightName string, rightScore int, clockSeconds float64, phase string) {
	c := newHUDCanvas(screen)
	c.fillRect(0, 0, worldW, 28, hudColor)
	score := leftName + " " + itoa(leftScore) + "   -   " + itoa(rightScore) + " " + rightName
	c.textSized(score, worldW/2, 14, 16, AlignCenter, hudText)
	c.textSized(formatClock(clockSeconds), 12, 14, 16, AlignLeft, hudText)
	if phase != "" {
		c.textSized(phase, worldW-12, 14, 14, AlignRight, hudText)
	}
	c.textSized("WASD move  -  mouse aim  -  hold left-click to charge shot  -  right-click to trap  -  Esc pause",
		10, worldH-14, 12, AlignLeft, hudDim)
}

// ShootoutPanel draws a sub-bar with the penalty tally: goals/kicks taken per side.
func ShootoutPanel(screen *ebiten.Image, leftName string, lg, lt int, rightName string, rg, rt int) {
	c := newHUDCanvas(screen)
	c.fillRect(0, 28, worldW, 22, hudColor)
	s := "PENALTIES   " + leftName + " " + itoa(lg) + "/" + itoa(lt) + "   -   " + itoa(rg) + "/" + itoa(rt) + " " + rightName
	c.textSized(s, worldW/2, 39, 13, AlignCenter, hudText)
}

// GoalBanner draws the "GOAL!" overlay shown after a goal.
func GoalBanner(screen *ebiten.Image) { CenterBanner(screen, "G O A L !") }

// CenterBanner draws a centred overlay message (goal, pause, result).
func CenterBanner(screen *ebiten.Image, message string) {
	c := newHUDCanvas(screen)
	c.fillRect(0, worldH/2-26, worldW, 52, bannerColor)
	c.textSized(message, worldW/2, worldH/2, 24, AlignCenter, hudText)
}

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
