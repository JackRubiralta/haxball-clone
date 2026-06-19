package menu

import (
	"image/color"
	"strconv"

	"github.com/hajimehoshi/ebiten/v2"

	"phootball/internal/render"
)

// resultScrollState persists the goal-timeline scroll offset across frames.
var resultScroll scrollState

// resultOverlay is the near-opaque backdrop behind the result panel: dark enough that the
// in-match HUD card behind it does not bleed through above the panel (the bug where the
// bright scoreboard showed over the top edge).
var resultOverlay = color.RGBA{6, 12, 10, 245}

func (a *App) screenResult(f frame) {
	r := buildResult(a.match)

	// Full-time panel over the dimmed final frame. The overlay is drawn nearly opaque so the
	// in-match HUD card behind it cannot bleed through above the panel, and the panel itself
	// starts near the very top so the result reads cleanly over where the HUD sat.
	const px, py, pw, ph = 110.0, 24.0, 780.0, 616.0
	const barH = 52.0
	pad := theme.PanelPad
	if f.draw {
		f.ui.DimScreen(resultOverlay) // cover the WHOLE screen, not just the letterboxed UI box
		f.ui.Panel(px, py, pw, ph, theme.Panel, theme.Edge)
	}
	innerX := px + pad
	innerW := pw - 2*pad

	// --- Header: team chips flanking the big scoreline, then the winner line + tag. ---
	headTop := py + 24
	a.drawResultHeader(f, r, innerX, headTop, innerW)

	// Action bar (pinned bottom).
	barY := py + ph - pad - barH
	bw := 220.0
	gap := 24.0
	bx := innerX + (innerW-(2*bw+gap))/2
	if !a.duo && f.button("Rematch", bx, barY, bw, barH) {
		a.startMatch(a.practice, a.human)
	}
	if f.button("Quit to Menu", bx+bw+gap, barY, bw, barH) {
		a.quitToMenu()
	}

	// --- Body: a scroll pane holding the goal timeline, shootout block, and stats. ---
	bodyTop := headTop + 132
	bodyBot := barY - 16
	paneH := bodyBot - bodyTop

	if !f.draw {
		if _, wy := ebiten.Wheel(); wy != 0 {
			resultScroll.Scroll(wy)
		}
	}
	top, cf := f.beginScroll(&resultScroll, innerX, bodyTop, innerW, paneH)
	col := newCol(innerX, top, innerW-12) // -12 leaves room for the scrollbar
	a.drawResultTimeline(cf, r, &col)
	if r.HasShootout {
		col.gapRow(0.4)
		a.drawResultShootout(cf, r, &col)
	}
	col.gapRow(0.4)
	a.drawResultStats(cf, &col)
	f.endScroll(&resultScroll, innerX, bodyTop, innerW, paneH, col.cursorY())
}

// drawResultHeader renders the two team chips, the big final scoreline between them, the
// winner line with a trophy, and the context tag.
func (a *App) drawResultHeader(f frame, r ResultModel, x, y, w float64) {
	if !f.draw {
		return
	}
	cx := x + w/2
	chipW := 220.0
	const swatch = 20.0
	sr := swatch / 2

	// Big centred scoreline.
	score := strconv.Itoa(r.Teams[0].Score) + " - " + strconv.Itoa(r.Teams[1].Score)
	f.ui.TextCenteredS(score, cx, y+30, 52, theme.Text)

	// Left (home) chip: a team-colour swatch then the name (consistent with the HUD card).
	render.TeamSwatch(f.screen, x+sr, y+24, swatch, r.Teams[0].Color)
	f.ui.TextS(fitMenu(f, r.Teams[0].Name, chipW-swatch-12, theme.Body), x+swatch+10, y+24, theme.Body, r.Teams[0].Color)

	// Right (away) chip: the name then a swatch, mirrored.
	rxEnd := x + w
	render.TeamSwatch(f.screen, rxEnd-sr, y+24, swatch, r.Teams[1].Color)
	f.ui.TextRightS(fitMenu(f, r.Teams[1].Name, chipW-swatch-12, theme.Body), rxEnd-swatch-10, y+24, theme.Body, r.Teams[1].Color)

	// Winner line with a trophy (a trophy only for a decided result), then the context tag.
	winY := y + 76
	line := r.WinnerLine
	tw := f.ui.MeasureUI(line, theme.Section)
	if r.WinnerIdx >= 0 {
		render.IconTrophy(f.screen, cx-tw/2-18, winY, 24, theme.Accent)
	}
	f.ui.TextCenteredS(line, cx, winY, theme.Section, r.WinnerTint)
	if r.ContextTag != "" {
		f.ui.TextCenteredS(r.ContextTag, cx, winY+24, theme.Small, theme.TextDim)
	}
}

// drawResultTimeline renders the chronological goal list, team-coloured per row.
func (a *App) drawResultTimeline(f frame, r ResultModel, col *colLayout) {
	f.sectionHeader("GOALS", col.x, col.header(1), col.w)
	if len(r.Goals) == 0 {
		y := col.row()
		if f.draw {
			f.ui.TextS("No goals.", col.x, y+theme.RowH/2, theme.Body, theme.TextDim)
		}
		return
	}
	for _, g := range r.Goals {
		y := col.row()
		if !f.draw {
			continue
		}
		midY := y + theme.RowH/2
		// Time, then a team-colour dot, scorer, and detail.
		f.ui.TextS(g.Time, col.x, midY, theme.Body, theme.TextDim)
		dotX := col.x + 64
		f.ui.FillRect(dotX, midY-5, 10, 10, g.Color)
		textX := dotX + 22
		label := g.Scorer
		if label == "" {
			label = "goal"
		}
		f.ui.TextS(label, textX, midY, theme.Body, theme.Text)
		if g.Detail != "" {
			f.ui.TextS(g.Detail, textX+90, midY, theme.Small, theme.TextDim)
		}
	}
}

// drawResultShootout renders, per side, a row of scored/missed dots plus the tally.
func (a *App) drawResultShootout(f frame, r ResultModel, col *colLayout) {
	f.sectionHeader("PENALTY SHOOTOUT", col.x, col.header(1), col.w)
	for i := 0; i < 2; i++ {
		y := col.row()
		if !f.draw {
			continue
		}
		midY := y + theme.RowH/2
		st := r.Shootout[i]
		f.ui.TextS(fitMenu(f, r.Teams[i].Name, 150, theme.Body), col.x, midY, theme.Body, r.Teams[i].Color)
		dx := col.x + 168
		const dr, dgap = 7.0, 22.0
		for _, scored := range st.Scored {
			if scored {
				f.ui.FillRect(dx-dr, midY-dr, dr*2, dr*2, theme.Accent)
			} else {
				f.ui.StrokeRect(dx-dr, midY-dr, dr*2, dr*2, 2, theme.Bad)
			}
			dx += dgap
		}
		f.ui.TextRightS(strconv.Itoa(st.Goals), col.x+col.w, midY, theme.Section, r.Teams[i].Color)
	}
}

// drawResultStats renders the final team stat sheet from the match recorder (home = Blue/left,
// away = Red/right).
func (a *App) drawResultStats(f frame, col *colLayout) {
	f.sectionHeader("STATS", col.x, col.header(1), col.w)
	homeX := col.x + col.w*0.62
	awayX := col.x + col.w
	if a.match == nil {
		return
	}
	sm := render.StatsModelFromMatch(a.match)
	rows := []struct{ label, home, away string }{
		{"Possession", resPct(sm.Left.PossessionPct), resPct(sm.Right.PossessionPct)},
		{"Shots (on tgt)", resShots(sm.Left.Shots, sm.Left.OnTarget), resShots(sm.Right.Shots, sm.Right.OnTarget)},
		{"Passes", resPasses(sm.Left.PassesDone, sm.Left.Passes), resPasses(sm.Right.PassesDone, sm.Right.Passes)},
		{"Interceptions", strconv.Itoa(sm.Left.Interceptions), strconv.Itoa(sm.Right.Interceptions)},
		{"Saves", strconv.Itoa(sm.Left.Saves), strconv.Itoa(sm.Right.Saves)},
	}
	for _, r := range rows {
		y := col.row()
		if !f.draw {
			continue
		}
		midY := y + theme.RowH/2
		f.ui.TextS(r.label, col.x, midY, theme.Body, theme.Text)
		f.ui.TextCenteredS(r.home, homeX, midY, theme.Body, theme.Text)
		f.ui.TextRightS(r.away, awayX, midY, theme.Body, theme.Text)
	}
}

func resPct(v float64) string { return strconv.Itoa(int(v+0.5)) + "%" }
func resShots(shots, onTarget int) string {
	return strconv.Itoa(shots) + " (" + strconv.Itoa(onTarget) + ")"
}
func resPasses(done, total int) string {
	s := strconv.Itoa(done) + "/" + strconv.Itoa(total)
	if total > 0 {
		s += " (" + strconv.Itoa(int(100*float64(done)/float64(total)+0.5)) + "%)"
	}
	return s
}
