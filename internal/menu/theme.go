package menu

import "image/color"

// Theme is the single source of truth for the menu's colour palette, type scale,
// and spacing. Widgets read the package-level `theme` value so the look is tuned
// in one place. The individual colour names are kept as package vars too (below)
// so existing call sites (app.go/settings.go) keep compiling unchanged.
type Theme struct {
	// Palette.
	BG       color.RGBA // screen backdrop
	Overlay  color.RGBA // dim over a paused match
	Panel    color.RGBA // panel fill
	Edge     color.RGBA // panel / divider border
	BtnBG    color.RGBA // button fill
	BtnHover color.RGBA // button fill, hovered
	BtnEdge  color.RGBA // button border
	Accent   color.RGBA // titles / highlights
	Text     color.RGBA // body text
	TextDim  color.RGBA // secondary text
	Bad      color.RGBA // validation/error highlight

	// Type scale (UI units; the menu lays out in render.UIWidth x render.UIHeight).
	Title   float64 // page title
	H1      float64 // big heading
	Section float64 // section header
	Body    float64 // body / row text
	Small   float64 // captions / hints

	// Spacing (UI units).
	RowH     float64 // height of one settings row
	RowGap   float64 // vertical gap between rows
	BtnH     float64 // standard button height
	StepBtnW float64 // width of a stepper's < / > button
	ControlW float64 // width of a row's value/control area
	PanelPad float64 // padding inside a panel
}

// theme is the active menu theme. The colours mirror the original green palette.
var theme = Theme{
	BG:       color.RGBA{14, 30, 20, 255},
	Overlay:  color.RGBA{8, 14, 12, 210},
	Panel:    color.RGBA{22, 40, 28, 245},
	Edge:     color.RGBA{96, 140, 104, 255},
	BtnBG:    color.RGBA{40, 64, 48, 255},
	BtnHover: color.RGBA{70, 108, 78, 255},
	BtnEdge:  color.RGBA{120, 160, 130, 255},
	Accent:   color.RGBA{150, 220, 160, 255},
	Text:     color.RGBA{226, 234, 226, 255},
	TextDim:  color.RGBA{170, 188, 172, 255},
	Bad:      color.RGBA{226, 110, 96, 255},

	Title:   56,
	H1:      30,
	Section: 18,
	Body:    16,
	Small:   13,

	RowH:     38,
	RowGap:   6,
	BtnH:     44,
	StepBtnW: 34,
	ControlW: 150,
	PanelPad: 28,
}

// Legacy palette names. app.go and settings.go reference these directly; they are
// kept as package vars (sourced from the theme) so those files compile unchanged
// while the canonical palette now lives on Theme.
var (
	menuBG    = theme.BG
	overlayBG = theme.Overlay
	panelBG   = theme.Panel
	panelEdge = theme.Edge
	btnBG     = theme.BtnBG
	btnHover  = theme.BtnHover
	btnBorder = theme.BtnEdge
	accent    = theme.Accent
)

// colLayout is a simple top-to-bottom layout cursor for a column of rows. It turns
// the hand-incremented `ly += rh` pattern into named steps, so a column's vertical
// math lives in one place and stays identical between the update and draw passes.
type colLayout struct {
	x, w float64 // left edge and width of the column
	y    float64 // current cursor Y (advances as rows are emitted)
}

// newCol starts a column at (x, y) with the given width.
func newCol(x, y, w float64) colLayout { return colLayout{x: x, y: y, w: w} }

// row returns the current row's Y and advances the cursor by one row + gap.
func (l *colLayout) row() float64 {
	y := l.y
	l.y += theme.RowH + theme.RowGap
	return y
}

// header returns the current Y and advances by n section-header heights (a header
// plus its underline reads as a slightly taller row).
func (l *colLayout) header(n float64) float64 {
	y := l.y
	l.y += theme.Section*1.6*n + theme.RowGap
	return y
}

// gapRow advances the cursor by n blank rows without emitting anything.
func (l *colLayout) gapRow(n float64) { l.y += (theme.RowH + theme.RowGap) * n }

// cursorY reports the current cursor position (e.g. to size a scroll pane's content).
func (l *colLayout) cursorY() float64 { return l.y }
