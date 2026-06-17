package menu

import (
	"image/color"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/inpututil"

	"phootball/internal/geom"
	"phootball/internal/render"
)

// frame carries this frame's input and draw surface for the immediate-mode widgets.
// The same screen function runs twice per displayed frame: once in Update with
// draw=false to read clicks and mutate state, once in Draw with draw=true to render.
// Layout is identical in both passes, so the two agree.
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
	menuBG    = color.RGBA{16, 36, 22, 255}
	overlayBG = color.RGBA{8, 14, 12, 205}
	btnBG     = color.RGBA{40, 60, 48, 255}
	btnHover  = color.RGBA{64, 96, 74, 255}
	btnBorder = color.RGBA{120, 150, 130, 255}
)

func (f frame) hit(x, y, w, h float64) bool {
	return f.cursor.X >= x && f.cursor.X <= x+w && f.cursor.Y >= y && f.cursor.Y <= y+h
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

// stepper draws a labelled "< value >" row and returns whether the dec/inc arrow was
// clicked this frame.
func (f frame) stepper(label, value string, y float64) (dec, inc bool) {
	if f.draw {
		f.ui.Text(label, 300, y+10)
	}
	dec = f.button("<", 560, y, 40, 34)
	if f.draw {
		f.ui.TextCentered(value, 650, y+10)
	}
	inc = f.button(">", 700, y, 40, 34)
	return dec, inc
}

// toggle draws a labelled ON/OFF button and returns whether it was clicked.
func (f frame) toggle(label string, on bool, y float64) bool {
	if f.draw {
		f.ui.Text(label, 300, y+10)
	}
	txt := "OFF"
	if on {
		txt = "ON"
	}
	return f.button(txt, 600, y, 90, 34)
}
