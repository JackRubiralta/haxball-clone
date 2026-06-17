package menu

import (
	"image/color"

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
	return frame{ui: ui, cursor: render.ScreenToWorld(cx, cy), draw: true}
}

var (
	menuBG    = color.RGBA{14, 30, 20, 255}   // deep pitch-green backdrop
	overlayBG = color.RGBA{8, 14, 12, 210}    // dim over a paused match
	panelBG   = color.RGBA{22, 40, 28, 245}   // menu panel fill
	panelEdge = color.RGBA{96, 140, 104, 255} // panel / divider border
	btnBG     = color.RGBA{40, 64, 48, 255}
	btnHover  = color.RGBA{70, 108, 78, 255}
	btnBorder = color.RGBA{120, 160, 130, 255}
	accent    = color.RGBA{150, 220, 160, 255}
)

func (f frame) hit(x, y, w, h float64) bool {
	return f.cursor.X >= x && f.cursor.X <= x+w && f.cursor.Y >= y && f.cursor.Y <= y+h
}

// backdrop fills the screen and draws a titled panel; returns the panel's inner left X,
// top Y, and width for laying out rows.
func (f frame) backdrop(title string) (x, y, w float64) {
	const px, py, pw, ph = 120.0, 70.0, 760.0, 560.0
	if f.draw {
		f.ui.Fill(menuBG)
		f.ui.Panel(px, py, pw, ph, panelBG, panelEdge)
		f.ui.Title(title, render.UIWidth/2, py+34, 30, accent)
	}
	return px + 30, py + 80, pw - 60
}

// button draws a button (draw pass) or reports a click on it (update pass).
func (f frame) button(label string, x, y, w, h float64) bool {
	hot := f.hit(x, y, w, h)
	if f.draw {
		bg := btnBG
		if hot {
			bg = btnHover
		}
		f.ui.FillRect(x, y, w, h, bg)
		f.ui.StrokeRect(x, y, w, h, 2, btnBorder)
		f.ui.TextCentered(label, x+w/2, y+h/2-6)
		return false
	}
	return hot && f.clicked
}

// sectionHeader draws an accented label with an underline spanning the row width.
func (f frame) sectionHeader(label string, x, y, w float64) {
	if f.draw {
		f.ui.Text(label, x, y)
		f.ui.Line(x, y+15, x+w, y+15, 1, panelEdge)
	}
}

// rowStepper draws a labelled "< value >" row in [x, x+w] and returns the dec/inc click.
func (f frame) rowStepper(label, value string, x, y, w float64) (dec, inc bool) {
	if f.draw {
		f.ui.Text(label, x, y+9)
		f.ui.TextCentered(value, x+w-77, y+9)
	}
	dec = f.button("<", x+w-120, y, 32, 28)
	inc = f.button(">", x+w-34, y, 32, 28)
	return dec, inc
}

// rowToggle draws a labelled ON/OFF button in [x, x+w] and returns whether it was clicked.
func (f frame) rowToggle(label string, on bool, x, y, w float64) bool {
	if f.draw {
		f.ui.Text(label, x, y+9)
	}
	txt := "OFF"
	if on {
		txt = "ON"
	}
	return f.button(txt, x+w-78, y, 78, 28)
}
