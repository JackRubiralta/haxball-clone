// Package render draws the game with Ebiten. It is the only internal package (other
// than the human controller) that imports Ebiten, keeping graphics out of the
// simulation. It exposes small primitives so both the local game (drawing a
// sim.Match) and the network client (drawing a server snapshot) share the same look.
package render

import (
	"image/color"
	"math"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/ebitenutil"
	"github.com/hajimehoshi/ebiten/v2/text"
	"github.com/hajimehoshi/ebiten/v2/vector"
	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gobold"
	"golang.org/x/image/font/opentype"

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
)

// Line widths (world units). The perimeter -- the boundary plus the goal line that
// closes it across each mouth -- shares one width so the pitch edge is uniform;
// interior markings are a touch thinner. Perimeter lines are offset outward by half
// their width so the inner edge lands exactly on the physics wall.
const (
	fieldLineWidth  = 4.0
	fieldLineOffset = fieldLineWidth / 2
	markingWidth    = 3.0
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
	scale := math.Min(w/ScreenWidth, h/ScreenHeight)
	if scale <= 0 {
		scale = 1
	}
	c := canvas{
		dst:   dst,
		scale: scale,
		ox:    (w - ScreenWidth*scale) / 2,
		oy:    (h - ScreenHeight*scale) / 2,
	}
	view = c
	return c
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

// openBox draws a penalty/goal box that opens onto the goal line: only the three
// inner sides, omitting the side on the goal line so it never doubles the boundary.
// originX is the goal-line edge; depth points into the pitch (signed).
func (c canvas) openBox(originX, cy, depth, height, w float64, clr color.Color) {
	top, bot := cy-height/2, cy+height/2
	far := originX + depth
	c.line(originX, top, far, top, w, clr)
	c.line(originX, bot, far, bot, w, clr)
	c.line(far, top, far, bot, w, clr)
	// Fill the two far corners so the round joins are not left notched.
	c.fillCircle(far, top, w/2, clr)
	c.fillCircle(far, bot, w/2, clr)
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

// ScreenToWorld converts a framebuffer (cursor) coordinate back to world space using
// the most recent frame's transform.
func ScreenToWorld(x, y int) geom.Vec {
	return geom.NewVec((float64(x)-view.ox)/view.scale, (float64(y)-view.oy)/view.scale)
}

// Match draws a complete local match.
func Match(screen *ebiten.Image, m *sim.Match) {
	Field(screen, m.Field, m.Teams[0].Color, m.Teams[1].Color)
	BallAt(screen, m.Ball.Position, m.Ball.Radius())
	for _, p := range m.Players {
		PlayerAt(screen, p.Position, p.Facing, p.Radius(), p.Team.Color, p.Number,
			sim.NormShootCharge(p.ShootCharge()), p.TrapCharge())
	}
	Scoreboard(screen, m.Teams[0].Name, m.Teams[0].Score, m.Teams[1].Name, m.Teams[1].Score)
	if m.Celebrating() {
		GoalBanner(screen)
	}
}

// Field draws the pitch: a striped lawn, boundary and markings, the two goals with
// nets, and any obstacles.
func Field(screen *ebiten.Image, f *sim.Field, leftColor, rightColor color.RGBA) {
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

	// Halfway line, centre circle and spot.
	c.line(cx, y, cx, y+h, markingWidth, lineColor)
	c.strokeCircle(cx, cy, 72, markingWidth, lineColor)
	c.fillCircle(cx, cy, 5, lineColor)

	// Penalty boxes and goal areas: drawn open on the goal-line side (three sides) so
	// their goal-line edge does not double up on the boundary near each goal. Heights
	// are fixed (not tied to the goal mouth), so shrinking the goal doesn't shrink the
	// boxes; the depths extend well out from the goal line.
	penaltyH := 330.0 // outer penalty area -- unchanged
	penaltyD := 150.0
	areaH := 150.0 // inner goal area -- kept narrow (clearly less wide than the penalty area)
	areaD := 75.0
	c.openBox(x, cy, penaltyD, penaltyH, markingWidth, lineColor)
	c.openBox(x+w, cy, -penaltyD, penaltyH, markingWidth, lineColor)
	c.openBox(x, cy, areaD, areaH, markingWidth, lineColor)
	c.openBox(x+w, cy, -areaD, areaH, markingWidth, lineColor)
	// Penalty spot: midway between the goal-area edge and the penalty-area edge.
	spotD := (areaD + penaltyD) / 2
	c.fillCircle(x+spotD, cy, 4, lineColor)
	c.fillCircle(x+w-spotD, cy, 4, lineColor)

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

	// Goal line closing the mouth, drawn on the post line itself.
	c.line(top.X, top.Y, bot.X, bot.Y, fieldLineWidth, lineColor)

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
func PlayerAt(screen *ebiten.Image, pos, facing geom.Vec, radius float64, body color.RGBA, number int, shootCharge, trapCharge float64) {
	c := newCanvas(screen)
	drawTrapAura(c, pos, radius, trapCharge, body) // glow under the body

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
	drawShootCharge(c, pos, radius, shootCharge, body) // power gauge over the body
}

// drawTrapAura draws a soft glow ring around a player while it traps, growing with
// the trap charge. The larger body itself comes from the bigger passed radius.
func drawTrapAura(c canvas, pos geom.Vec, radius, trap float64, body color.RGBA) {
	if trap <= 0 {
		return
	}
	// A glow drawn as a stack of thin concentric bands whose opacity runs as a smooth
	// LINEAR gradient from the inner edge out: 75 (of 255) right at the body, falling
	// to 10 at the outer rim. The alpha is read straight from the gradient (not summed
	// across bands), so it stays a clean halo the ball reads through rather than a
	// bright blob. The whole gradient scales with the trap charge, so it fades in as the
	// charge builds instead of popping in, and the reach grows with the charge too.
	const bands = 24
	const innerAlpha = 75.0 // opacity at the body (inner edge), at full charge
	const outerAlpha = 10.0 // opacity at the outer rim, at full charge
	reach := 4 + 16*trap                    // how far the glow reaches past the body, grows with charge
	width := reach / float64(bands-1) * 1.1 // thin bands with a hair of overlap so they meet seamlessly
	for i := 0; i < bands; i++ {
		t := float64(i) / float64(bands-1) // 0 at the body, 1 at the outer rim
		r := radius + reach*t
		a := uint8((innerAlpha + (outerAlpha-innerAlpha)*t) * trap) // linear 75 -> 10, scaled by charge
		if a == 0 {
			continue
		}
		c.strokeCircle(pos.X, pos.Y, r, width, color.RGBA{body.R, body.G, body.B, a})
	}
}

// drawShootCharge draws a radial power gauge around a player that fills from the top
// as the shoot charge grows (0..1), brightening toward full.
func drawShootCharge(c canvas, pos geom.Vec, radius, charge float64, body color.RGBA) {
	if charge <= 0 {
		return
	}
	r := radius + 5
	c.strokeCircle(pos.X, pos.Y, r, 2, color.RGBA{body.R, body.G, body.B, 70})
	arc := color.RGBA{body.R, body.G, body.B, uint8(150 + 105*charge)}
	strokeArc(c, pos, r, -math.Pi/2, -math.Pi/2+2*math.Pi*charge, 3, arc)
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

// Scoreboard draws a HUD bar with the score and the controls hint.
func Scoreboard(screen *ebiten.Image, leftName string, leftScore int, rightName string, rightScore int) {
	c := newCanvas(screen)
	c.fillRect(0, 0, ScreenWidth, 28, hudColor)
	score := leftName + " " + itoa(leftScore) + "   -   " + itoa(rightScore) + " " + rightName
	c.text(score, ScreenWidth/2, 7, -len(score)*3, 0)
	c.text("WASD move  -  mouse aim  -  hold left-click to charge shot  -  right-click to trap", 10, ScreenHeight-22, 0, 0)
}

// GoalBanner draws the "GOAL!" overlay shown after a goal.
func GoalBanner(screen *ebiten.Image) {
	c := newCanvas(screen)
	c.fillRect(0, ScreenHeight/2-26, ScreenWidth, 52, bannerColor)
	c.text("G O A L !", ScreenWidth/2, ScreenHeight/2-4, -24, 0)
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
