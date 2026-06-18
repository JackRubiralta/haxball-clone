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
}

func updateFrame() frame {
	cx, cy := ebiten.CursorPosition()
	return frame{
		cursor:  render.ScreenToWorld(cx, cy),
		clicked: inpututil.IsMouseButtonJustPressed(ebiten.MouseButtonLeft),
	}
}

func drawFrame(screen *ebiten.Image) frame {
	cx, cy := ebiten.CursorPosition()
	ui := render.BeginUI(screen)
	return frame{ui: ui, screen: screen, cursor: render.ScreenToWorld(cx, cy), draw: true}
}

func (f frame) hit(x, y, w, h float64) bool {
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

// rowStepper draws a labelled "< value >" row in [x, x+w] and returns the dec/inc click.
// The value sits between two stepper buttons pinned to the right of the row.
func (f frame) rowStepper(label, value string, x, y, w float64) (dec, inc bool) {
	bh := theme.RowH - 10
	incX := x + w - theme.StepBtnW
	decX := incX - theme.ControlW + theme.StepBtnW
	if f.draw {
		f.ui.TextS(label, x, y+theme.RowH/2, theme.Body, theme.Text)
		valCX := (decX + theme.StepBtnW + incX) / 2
		f.ui.TextCenteredS(value, valCX, y+theme.RowH/2, theme.Body, theme.Text)
	}
	dec = f.button("<", decX, y, theme.StepBtnW, bh)
	inc = f.button(">", incX, y, theme.StepBtnW, bh)
	return dec, inc
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

// beginScroll opens a scroll pane occupying [x, x+w] x [y, y+h] and returns the Y
// at which the first row should be laid out: the pane's top minus the (clamped)
// scroll offset. Lay rows out downward from there as usual; the offset shifts them
// all together. (render.UI has no clip primitive, so the App keeps action bars and
// headers OUTSIDE the pane and sizes the pane to the panel interior; content that
// scrolls past the edges is expected to be short-lived.) Pair every beginScroll
// with an endScroll passing the cursor Y reached, which records the content height
// and draws the scrollbar.
func (f frame) beginScroll(s *scrollState, x, y, w, h float64) float64 {
	s.view = h
	if s.offset < 0 {
		s.offset = 0
	}
	if m := s.MaxOffset(); s.offset > m {
		s.offset = m
	}
	return y - s.offset
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
	bandW := theme.ControlW
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
