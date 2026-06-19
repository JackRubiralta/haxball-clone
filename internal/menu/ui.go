package menu

import (
	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/inpututil"

	"phootball/internal/geom"
	"phootball/internal/render"
)

// frame carries this frame's input and draw surface for the immediate-mode widgets. The
// same screen function runs twice per displayed frame: once in Update with draw=false to
// read clicks and mutate state, once in Draw with draw=true to render. Layout is
// identical in both passes, so the two agree.
type frame struct {
	ui      render.UI
	screen  *ebiten.Image // the draw surface (draw pass only); needed for the procedural icons
	cursor  geom.Vec
	clicked bool
	draw    bool

	// Clip rectangle for a scroll pane. When clipped is set, draws go through a UI
	// masked to [clipY, clipY+clipH] and hit-tests outside that band are culled, so
	// off-pane rows neither draw nor respond to clicks. The X span is the full row width
	// (panes only overflow vertically), so only the Y band gates the hit-test.
	clipped      bool
	clipY, clipH float64
}

// updateFrame builds the input-only frame for the update pass. It maps the cursor with the
// UI viewport captured by the last draw pass (a.uiViewport), so there is no render global.
func (a *App) updateFrame() frame {
	cx, cy := ebiten.CursorPosition()
	return frame{
		cursor:  a.uiViewport.ScreenToWorld(cx, cy),
		clicked: inpututil.IsMouseButtonJustPressed(ebiten.MouseButtonLeft),
	}
}

// drawFrame begins a UI surface and records its viewport for the next update pass.
func (a *App) drawFrame(screen *ebiten.Image) frame {
	cx, cy := ebiten.CursorPosition()
	ui := render.BeginUI(screen)
	a.uiViewport = ui.Viewport()
	return frame{ui: ui, screen: screen, cursor: a.uiViewport.ScreenToWorld(cx, cy), draw: true}
}

func (f frame) hit(x, y, w, h float64) bool {
	// Cull the hit-test when the widget falls outside the active scroll-pane band, so an
	// off-pane (scrolled-away) button is not clickable even though the cursor is over where
	// it would draw. A widget straddling the edge stays hittable on its visible part.
	if f.clipped && (y+h <= f.clipY || y >= f.clipY+f.clipH) {
		return false
	}
	return f.cursor.X >= x && f.cursor.X <= x+w && f.cursor.Y >= y && f.cursor.Y <= y+h
}

// backdrop fills the screen and draws a titled panel; returns the panel's inner left X,
// top Y, and width for laying out rows.
func (f frame) backdrop(title string) (x, y, w float64) {
	const px, py, pw, ph = 120.0, 70.0, 760.0, 560.0
	if f.draw {
		f.ui.Fill(theme.BG)
		f.ui.Panel(px, py, pw, ph, theme.Panel, theme.Edge)
		f.ui.Title(title, render.UIWidth/2, py+34, theme.H1, theme.Accent)
	}
	return px + theme.PanelPad, py + 80, pw - 2*theme.PanelPad
}

// button draws a button (draw pass) or reports a click on it (update pass).
func (f frame) button(label string, x, y, w, h float64) bool {
	hot := f.hit(x, y, w, h)
	if f.draw {
		bg := theme.BtnBG
		if hot {
			bg = theme.BtnHover
		}
		f.ui.FillRect(x, y, w, h, bg)
		f.ui.StrokeRect(x, y, w, h, 2, theme.BtnEdge)
		f.ui.TextCenteredS(label, x+w/2, y+h/2, theme.Body, theme.Text)
		return false
	}
	return hot && f.clicked
}

// sectionHeader draws an accented label with an underline spanning the row width.
func (f frame) sectionHeader(label string, x, y, w float64) {
	if f.draw {
		f.ui.TextS(label, x, y+theme.Section*0.5, theme.Section, theme.Accent)
		f.ui.Line(x, y+theme.Section, x+w, y+theme.Section, 1, theme.Edge)
	}
}

// rowStepper draws a labelled, compact "< value >" cluster in [x, x+w] and returns the dec/inc
// click. cur/lo/hi bound the value: the < arrow greys out (and stops responding) once cur is at
// lo, the > arrow once cur is at hi, so a reached limit is visible. Pass lo >= hi for an
// unbounded / cycling stepper (both arrows always live). The cluster is RIGHT-ALIGNED in the
// control band (so it lines up with toggles/segments in the same column) and tightly grouped:
// the > button hugs the band's right edge, the value is centred just left of it, and the <
// button hugs the value -- each with a small fixed gap. The value width is measured
// (scale-independently, so both passes agree) to size the cluster, and everything is vertically
// centred on the buttons' midline.
func (f frame) rowStepper(label, value string, x, y, w, cur, lo, hi float64) (dec, inc bool) {
	bh := theme.RowH - 10
	const gap = 8.0
	midY := y + bh/2 // centre on the buttons, which start at y and are bh tall
	valW := f.ui.MeasureUI(value, theme.Body)
	incX := x + w - theme.StepBtnW
	valCX := incX - gap - valW/2
	decX := valCX - valW/2 - gap - theme.StepBtnW
	// A cycling / no-bounds stepper signals itself with hi < lo (both arrows always active). When
	// lo == hi there is exactly ONE possible value, so BOTH arrows grey out -- e.g. the human-slot
	// picker at team size 1.
	unbounded := lo > hi
	canDec := unbounded || cur > lo
	canInc := unbounded || cur < hi
	if f.draw {
		f.ui.TextS(label, x, y+theme.RowH/2, theme.Body, theme.Text)
		f.ui.TextCenteredS(value, valCX, midY, theme.Body, theme.Text)
	}
	dec = f.arrowButton("<", canDec, decX, y, theme.StepBtnW, bh)
	inc = f.arrowButton(">", canInc, incX, y, theme.StepBtnW, bh)
	return dec, inc
}

// arrowButton draws a stepper arrow. When !enabled (the value is at its min/max) it is greyed
// out -- a dim fill, dim border, dim glyph -- and ignores clicks, so a reached bound reads as
// "can't go further".
func (f frame) arrowButton(label string, enabled bool, x, y, w, h float64) bool {
	hot := enabled && f.hit(x, y, w, h)
	if f.draw {
		bg, edge, clr := theme.BtnBG, theme.BtnEdge, theme.Text
		switch {
		case !enabled:
			bg, edge, clr = theme.Panel, theme.Edge, theme.TextDim
		case hot:
			bg = theme.BtnHover
		}
		f.ui.FillRect(x, y, w, h, bg)
		f.ui.StrokeRect(x, y, w, h, 2, edge)
		f.ui.TextCenteredS(label, x+w/2, y+h/2, theme.Body, clr)
		return false
	}
	return hot && f.clicked
}

// selectButton draws a button that can show a selected (highlighted) state -- accent border and
// text on a hover-coloured fill, like a chosen tab. Returns a click in the update pass. Used for
// the quick-fill preset buttons so the active preset reads as selected.
func (f frame) selectButton(label string, selected bool, x, y, w, h float64) bool {
	hot := f.hit(x, y, w, h)
	if f.draw {
		bg, edge, clr := theme.BtnBG, theme.BtnEdge, theme.Text
		switch {
		case selected:
			bg, edge, clr = theme.BtnHover, theme.Accent, theme.Accent
		case hot:
			bg = theme.BtnHover
		}
		f.ui.FillRect(x, y, w, h, bg)
		f.ui.StrokeRect(x, y, w, h, 2, edge)
		f.ui.TextCenteredS(label, x+w/2, y+h/2, theme.Body, clr)
		return false
	}
	return hot && f.clicked
}

// rowToggle draws a labelled ON/OFF button in [x, x+w] and returns whether it was clicked.
func (f frame) rowToggle(label string, on bool, x, y, w float64) bool {
	bh := theme.RowH - 10
	if f.draw {
		f.ui.TextS(label, x, y+theme.RowH/2, theme.Body, theme.Text)
	}
	txt := "OFF"
	if on {
		txt = "ON"
	}
	const tw = 78.0
	return f.button(txt, x+w-tw, y, tw, bh)
}

// tabRail draws a vertical stack of tab buttons at (x, y), each w wide and rowH
// tall, with the selected one highlighted. It returns the index hovered-and-clicked
// this frame, or sel if none -- so callers write `sel = f.tabRail(items, sel, ...)`.
func (f frame) tabRail(items []string, sel int, x, y, w, rowH float64) int {
	out := sel
	for i, label := range items {
		ty := y + float64(i)*(rowH+theme.RowGap)
		hot := f.hit(x, ty, w, rowH)
		if f.draw {
			bg := theme.BtnBG
			switch {
			case i == sel:
				bg = theme.BtnHover
			case hot:
				bg = theme.BtnBG
			}
			f.ui.FillRect(x, ty, w, rowH, bg)
			edge := theme.Edge
			if i == sel {
				edge = theme.Accent
			}
			f.ui.StrokeRect(x, ty, w, rowH, 2, edge)
			clr := theme.Text
			if i == sel {
				clr = theme.Accent
			}
			f.ui.TextS(label, x+12, ty+rowH/2, theme.Body, clr)
			continue
		}
		if hot && f.clicked {
			out = i
		}
	}
	return out
}

// rowTextField draws a labelled single-line text box editing *val in [x, x+w] (the box is
// right-aligned in the control band, like the steppers). When focused it consumes typed characters
// (filtered by accept) and Backspace in the UPDATE pass only -- mirroring the wheel-read discipline
// -- and shows a caret (blinking via caretOn). Input is charset-sanitized and length-capped so the
// field can never hold an un-dialable or unbounded string. Returns whether the box was clicked, so
// the caller can move focus to this field.
func (f frame) rowTextField(label string, val *string, x, y, w float64, focused, caretOn bool, accept func(rune) bool, maxLen int) (clicked bool) {
	bh := theme.RowH - 10
	boxW := theme.ControlW + 60 // a touch wider than a stepper so an address fits
	if boxW > w {
		boxW = w
	}
	bx := x + w - boxW
	if !f.draw {
		if focused {
			for _, r := range ebiten.AppendInputChars(nil) {
				if accept(r) && len([]rune(*val)) < maxLen {
					*val += string(r)
				}
			}
			if inpututil.IsKeyJustPressed(ebiten.KeyBackspace) {
				if rs := []rune(*val); len(rs) > 0 {
					*val = string(rs[:len(rs)-1])
				}
			}
		}
		return f.hit(bx, y, boxW, bh) && f.clicked
	}
	f.ui.TextS(label, x, y+theme.RowH/2, theme.Body, theme.Text)
	edge := theme.BtnEdge
	if focused {
		edge = theme.Accent
	}
	f.ui.FillRect(bx, y, boxW, bh, theme.BtnBG)
	f.ui.StrokeRect(bx, y, boxW, bh, 2, edge)
	shown := fitTail(f, *val, boxW-16, theme.Body) // anchor the tail (the caret end) when it overflows
	f.ui.TextS(shown, bx+8, y+bh/2, theme.Body, theme.Text)
	if focused && caretOn {
		cw := f.ui.MeasureUI(shown, theme.Body)
		f.ui.Line(bx+8+cw+1, y+7, bx+8+cw+1, y+bh-7, 1, theme.Text)
	}
	return false
}

// fitTail truncates s from the FRONT with a leading ellipsis so its tail fits within maxW (used by
// the text field, where the caret sits at the end).
func fitTail(f frame, s string, maxW, sizeUI float64) string {
	if f.ui.MeasureUI(s, sizeUI) <= maxW {
		return s
	}
	for len(s) > 1 {
		s = s[1:]
		if f.ui.MeasureUI("…"+s, sizeUI) <= maxW {
			return "…" + s
		}
	}
	return s
}

// scrollState is the persistent state of one scroll pane. The App owns it (one per
// scrollable page) so the offset survives across frames; the wheel is read in the
// update pass only (like the Settings zoom wheel) and fed to Scroll. beginScroll
// clamps it against the content measured on the previous frame.
type scrollState struct {
	offset  float64 // current scroll distance (>= 0)
	content float64 // content height measured by the last endScroll
	view    float64 // viewport height set by the last beginScroll
}

// Scroll adds wheel delta to the offset (call from the update pass). Positive dy
// (wheel up) scrolls toward the top. The result is clamped on the next beginScroll.
func (s *scrollState) Scroll(dy float64) { s.offset -= dy * 40 }

// MaxOffset is how far the pane can scroll given the last measured content/view.
func (s *scrollState) MaxOffset() float64 {
	m := s.content - s.view
	if m < 0 {
		return 0
	}
	return m
}

// beginScroll opens a scroll pane occupying [x, x+w] x [y, y+h]. It returns the Y at
// which the first row should be laid out (the pane's top minus the clamped scroll offset)
// AND a clipped frame the caller draws the pane CONTENT through: that frame's UI is masked
// to the pane rectangle (rows scrolled past the edges are not painted) and its hit-tests
// are culled to the same band (off-pane buttons are not clickable). Lay rows out downward
// from the returned top as usual; the offset shifts them all together. Draw the pane CHROME
// (headers, action bar, scrollbar) through the ORIGINAL frame so it is never clipped. Pair
// every beginScroll with an endScroll (on the original frame) passing the cursor Y reached,
// which records the content height and draws the scrollbar.
func (f frame) beginScroll(s *scrollState, x, y, w, h float64) (float64, frame) {
	s.view = h
	if s.offset < 0 {
		s.offset = 0
	}
	if m := s.MaxOffset(); s.offset > m {
		s.offset = m
	}
	cf := f
	cf.clipped = true
	cf.clipY, cf.clipH = y, h
	if f.draw {
		cf.ui = f.ui.PushClip(x, y, w, h)
	}
	return y - s.offset, cf
}

// endScroll closes a scroll pane. contentBottom is the Y the layout cursor reached
// (in unscrolled coordinates -- i.e. the value beginScroll returned plus the rows'
// total height, before adding back the offset is unnecessary: pass the cursor's
// current Y). It records the content height for next frame's clamp and draws a slim
// scrollbar on the pane's right edge when the content overflows.
func (f frame) endScroll(s *scrollState, x, y, w, h, contentBottom float64) {
	s.content = (contentBottom + s.offset) - y
	if !f.draw {
		return
	}
	if s.content <= h {
		return
	}
	const bw = 5.0
	trackX := x + w - bw
	f.ui.FillRect(trackX, y, bw, h, theme.Edge)
	thumbH := h * h / s.content
	if thumbH < 18 {
		thumbH = 18
	}
	max := s.MaxOffset()
	t := 0.0
	if max > 0 {
		t = s.offset / max
	}
	thumbY := y + t*(h-thumbH)
	f.ui.FillRect(trackX, thumbY, bw, thumbH, theme.Accent)
}

// segmented draws a label and a row of adjacent option buttons (a segmented
// control) in [x, x+w]; the selected option is highlighted. It returns the option
// index clicked this frame, or sel if none -- so callers write
// `sel = f.segmented(label, opts, sel, ...)`. The segments share the right-hand
// control band so it lines up with steppers/toggles in the same column.
func (f frame) segmented(label string, opts []string, sel int, x, y, w float64) int {
	out := sel
	bh := theme.RowH - 10
	if f.draw {
		f.ui.TextS(label, x, y+theme.RowH/2, theme.Body, theme.Text)
	}
	if len(opts) == 0 {
		return out
	}
	// Size the band so the WIDEST option gets comfortable padding (a 3-way "easy/normal/hard"
	// needs more room than the stepper control band, or the long word crowds its neighbours and
	// the short ones read as off-centre). Never narrower than the control band; right-aligned
	// like the steppers/toggles. MeasureUI is scale-independent, so both passes agree.
	widest := 0.0
	for _, o := range opts {
		if ow := f.ui.MeasureUI(o, theme.Small); ow > widest {
			widest = ow
		}
	}
	bandW := (widest + 20) * float64(len(opts))
	if bandW < theme.ControlW {
		bandW = theme.ControlW
	}
	if bandW > w {
		bandW = w
	}
	bx := x + w - bandW
	segW := bandW / float64(len(opts))
	for i, label := range opts {
		sx := bx + float64(i)*segW
		hot := f.hit(sx, y, segW, bh)
		if f.draw {
			bg := theme.BtnBG
			if i == sel {
				bg = theme.BtnHover
			} else if hot {
				bg = theme.BtnBG
			}
			f.ui.FillRect(sx, y, segW, bh, bg)
			edge := theme.Edge
			if i == sel {
				edge = theme.Accent
			}
			f.ui.StrokeRect(sx, y, segW, bh, 2, edge)
			clr := theme.Text
			if i == sel {
				clr = theme.Accent
			}
			f.ui.TextCenteredS(label, sx+segW/2, y+bh/2, theme.Small, clr)
			continue
		}
		if hot && f.clicked {
			out = i
		}
	}
	return out
}
