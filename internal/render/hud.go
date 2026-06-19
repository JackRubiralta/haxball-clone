package render

import (
	"image/color"

	"github.com/hajimehoshi/ebiten/v2"

	"phootball/internal/sim"
)

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
	hudCardTop  = 8.0   // margin above the floating card
	hudCardH    = 52.0  // card height
	hudScoreS   = 26.0  // big vector score size (kept -- it reads well)
	hudNameS    = 17.0  // team name (bumped from 14 -- it was too small)
	hudClockS   = 14.0  // clock
	hudPhaseS   = 12.0  // phase label
	hudSwatchW  = 16.0  // team-colour swatch (a dot)
	hudCardPad  = 16.0  // inner horizontal padding
	hudColGap   = 9.0   // gap between a swatch and its team name
	hudSideGap  = 20.0  // gap between each team block and the central score (kept tight)
	hudNameMaxW = 120.0 // max team-name width before truncation
)

// hudPanel/hudEdge mirror the menu's panel + edge so the card reads as the same UI family.
var (
	hudPanel  = color.RGBA{22, 40, 28, 235}   // dark green rounded panel fill (matches the menu Panel)
	hudEdge   = color.RGBA{96, 140, 104, 235} // matches the menu Edge so the card reads as the same UI family
	hudScored = color.RGBA{96, 220, 110, 255} // shootout scored dot
	hudMissed = color.RGBA{210, 78, 78, 255}  // shootout missed dot
	hudEmpty  = color.RGBA{210, 218, 210, 80} // untaken dot
)

// DrawHUD draws the in-game scoreboard as a CONDENSED FLOATING CARD centred at the top: a
// small dark rounded panel (styled like the menu) holding, per side, a team-colour SWATCH
// and the team name, the big vector score "L - R" in the middle, and the clock + phase
// label compactly below the score. It is client-only and fit-to-window (never pans or
// zooms), so it stays put under the camera. The same card serves the local and network
// paths (HUDModel is built identically), with a shootout dot sub-row appended when active.
func DrawHUD(screen *ebiten.Image, h HUDModel) {
	c := newOverlayCanvas(screen)
	w := overlayW

	// Card geometry: size each team block to its actual (swatch + name) content and keep both a
	// fixed small gap from the central score, so the teams sit CLOSE to the score instead of at
	// the card's far edges. Both sides reserve the wider block's width so the score stays
	// perfectly centred even with unequal names.
	score := itoa(h.LeftScore) + " - " + itoa(h.RightScore)
	scoreW := measureUI(score, hudScoreS)
	leftName := fitText(h.LeftName, hudNameMaxW, hudNameS)
	rightName := fitText(h.RightName, hudNameMaxW, hudNameS)
	leftBlockW := hudSwatchW + hudColGap + measureUI(leftName, hudNameS)
	rightBlockW := hudSwatchW + hudColGap + measureUI(rightName, hudNameS)
	sideW := leftBlockW
	if rightBlockW > sideW {
		sideW = rightBlockW
	}
	cardW := scoreW + 2*hudSideGap + 2*sideW + 2*hudCardPad
	cardX := (w - cardW) / 2
	cardY := hudCardTop
	cx := w / 2

	// Panel fill + edge -- 2px stroke to match the menu panels.
	c.fillRect(cardX, cardY, cardW, hudCardH, hudPanel)
	c.strokeRect(cardX, cardY, cardW, hudCardH, 2, hudEdge)

	scoreMidY := cardY + hudCardH*0.38 // score sits in the upper portion; clock/phase below
	c.textSized(score, cx, scoreMidY, hudScoreS, AlignCenter, hudText)

	// Left block [swatch][name], hugging the score's left side.
	leftEnd := cx - scoreW/2 - hudSideGap
	lx := leftEnd - leftBlockW
	drawTeamSwatch(c, lx, scoreMidY, hudSwatchW, h.LeftColor)
	c.textSized(leftName, lx+hudSwatchW+hudColGap, scoreMidY, hudNameS, AlignLeft, hudText)

	// Right block [name][swatch], mirrored, hugging the score's right side.
	rStart := cx + scoreW/2 + hudSideGap
	c.textSized(rightName, rStart, scoreMidY, hudNameS, AlignLeft, hudText)
	drawTeamSwatch(c, rStart+measureUI(rightName, hudNameS)+hudColGap, scoreMidY, hudSwatchW, h.RightColor)

	// Clock + phase below the score, compact and centred.
	subY := cardY + hudCardH*0.76
	clock := formatClock(h.ClockSeconds)
	if h.Phase != "" {
		// Clock on the left of centre, phase on the right, with a thin separator.
		c.textSized(clock, cx-6, subY, hudClockS, AlignRight, hudDim)
		c.textSized(h.Phase, cx+6, subY, hudPhaseS, AlignLeft, hudColorForPhase(h.Phase))
	} else {
		c.textSized(clock, cx, subY, hudClockS, AlignCenter, hudDim)
	}

	if h.InShootout {
		drawShootoutRow(c, h, cardX, cardY+hudCardH, cardW)
	}
}

// drawTeamSwatch draws a small filled rounded square (a colour dot) at (x, midY) for a team,
// with a subtle dark outline so a light team colour still reads against the panel. This is
// the single team-colour mark shared by the HUD and the result header -- not a shield shape
// and not a wide colour bar.
func drawTeamSwatch(c canvas, x, midY, size float64, col color.RGBA) {
	r := size / 2
	c.fillCircle(x+r, midY, r, col)
	c.strokeCircle(x+r, midY, r-0.75, 1.5, withAlpha(outlineColor, 200))
}

// drawShootoutRow draws the penalties sub-card directly under the score card: a "PENALTIES"
// tag and a row of result dots per side (scored = green, missed = red). cardX/rowY/cardW
// place it flush below the floating card so the two read as one stacked panel.
func drawShootoutRow(c canvas, h HUDModel, cardX, rowY, cardW float64) {
	const rowH = 20.0
	cx := cardX + cardW/2
	c.fillRect(cardX, rowY, cardW, rowH, hudPanel)
	c.strokeRect(cardX, rowY, cardW, rowH, 2, hudEdge)
	c.textSized("PENALTIES", cx, rowY+rowH/2, 11, AlignCenter, hudDim)

	dotR := 4.0
	gap := 11.0
	cy := rowY + rowH/2
	// Left dots grow leftward from centre-left; right dots grow rightward from centre-right.
	left := dotsFor(h.LeftDots, h.LeftPenGoals, h.LeftPenTaken)
	right := dotsFor(h.RightDots, h.RightPenGoals, h.RightPenTaken)
	startL := cx - 64
	for i, scored := range left {
		c.fillCircle(startL-float64(i)*gap, cy, dotR, dotColor(scored))
	}
	startR := cx + 64
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
		return color.RGBA{150, 220, 160, 255} // the menu accent (mint), not a clashing red
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
