package render

import (
	"image/color"

	"github.com/hajimehoshi/ebiten/v2"

	"phootball/internal/sim"
)

// StatTeamRow is one team's display-ready totals for the live stats panel.
type StatTeamRow struct {
	Name          string
	Color         color.RGBA
	Goals         int
	Shots         int
	OnTarget      int
	Passes        int
	PassesDone    int
	Interceptions int
	Saves         int
	PossessionPct float64
}

// StatsModel is the presentational view of a match's team stats. It is built identically from
// a local match or a network snapshot, so the HUD renders the same numbers either way.
type StatsModel struct {
	Left, Right StatTeamRow
}

// StatsModelFromStats builds the model from a flattened stats snapshot plus the names/colours
// the caller already knows (the network client path).
func StatsModelFromStats(stats sim.StatsSnapshot, leftName, rightName string, leftColor, rightColor color.RGBA) StatsModel {
	total := 0.0
	for _, t := range stats.Teams {
		total += t.PossessionSeconds
	}
	m := StatsModel{
		Left:  StatTeamRow{Name: leftName, Color: leftColor},
		Right: StatTeamRow{Name: rightName, Color: rightColor},
	}
	for _, t := range stats.Teams {
		row := &m.Left
		if t.Side == sim.SideRight {
			row = &m.Right
		}
		row.Goals, row.Shots, row.OnTarget = t.Goals, t.Shots, t.ShotsOnTarget
		row.Passes, row.PassesDone = t.Passes, t.PassesCompleted
		row.Interceptions, row.Saves = t.Interceptions, t.Saves
		row.PossessionPct = t.PossessionPct(total)
	}
	return m
}

// StatsModelFromMatch builds the model from a live local match (reads its recorder).
func StatsModelFromMatch(m *sim.Match) StatsModel {
	return StatsModelFromStats(
		sim.StatsSnapshot{Teams: m.Stats().Teams},
		m.Teams[0].Name, m.Teams[1].Name, m.Teams[0].Color, m.Teams[1].Color,
	)
}

// stats-panel layout constants (overlay coordinates).
const (
	statsPanelW   = 380.0
	statsTitleS   = 20.0
	statsHeaderS  = 16.0
	statsRowS     = 15.0
	statsRowH     = 26.0
	statsPanelPad = 16.0
)

// StatsPanel draws the live team-stats panel centred over the pitch. It uses the same overlay
// canvas and palette as the scoreboard so it reads as one UI family.
func StatsPanel(screen *ebiten.Image, m StatsModel) {
	c := newOverlayCanvas(screen)

	rows := []struct {
		label       string
		left, right string
	}{
		{"Possession", pct(m.Left.PossessionPct), pct(m.Right.PossessionPct)},
		{"Goals", itoa(m.Left.Goals), itoa(m.Right.Goals)},
		{"Shots (on tgt)", itoa(m.Left.Shots) + " (" + itoa(m.Left.OnTarget) + ")", itoa(m.Right.Shots) + " (" + itoa(m.Right.OnTarget) + ")"},
		{"Passes", passText(m.Left), passText(m.Right)},
		{"Interceptions", itoa(m.Left.Interceptions), itoa(m.Right.Interceptions)},
		{"Saves", itoa(m.Left.Saves), itoa(m.Right.Saves)},
	}

	panelH := statsPanelPad*2 + statsTitleS + 8 + statsHeaderS + 6 + float64(len(rows))*statsRowH
	x := (overlayW - statsPanelW) / 2
	y := (overlayH - panelH) / 2
	c.fillRect(x, y, statsPanelW, panelH, hudPanel)
	c.strokeRect(x, y, statsPanelW, panelH, 2, hudEdge)

	cx := x + statsPanelW/2
	leftX := x + statsPanelPad
	rightX := x + statsPanelW - statsPanelPad
	ty := y + statsPanelPad + statsTitleS

	c.textSized("MATCH STATS", cx, ty, statsTitleS, AlignCenter, hudText)
	ty += 8 + statsHeaderS
	// Team headers with colour swatches.
	c.fillRect(leftX, ty-statsHeaderS*0.8, 10, 10, m.Left.Color)
	c.textSized(fitText(m.Left.Name, statsPanelW/2-40, statsHeaderS), leftX+16, ty, statsHeaderS, AlignLeft, hudText)
	c.textSized(fitText(m.Right.Name, statsPanelW/2-40, statsHeaderS), rightX-16, ty, statsHeaderS, AlignRight, hudText)
	c.fillRect(rightX-10, ty-statsHeaderS*0.8, 10, 10, m.Right.Color)

	ty += 6
	for _, r := range rows {
		ty += statsRowH
		c.textSized(r.left, leftX, ty, statsRowS, AlignLeft, hudText)
		c.textSized(r.label, cx, ty, statsRowS, AlignCenter, hudDim)
		c.textSized(r.right, rightX, ty, statsRowS, AlignRight, hudText)
	}
}

func passText(r StatTeamRow) string {
	s := itoa(r.PassesDone) + "/" + itoa(r.Passes)
	if r.Passes > 0 {
		s += " (" + itoa(int(100*float64(r.PassesDone)/float64(r.Passes)+0.5)) + "%)"
	}
	return s
}

func pct(v float64) string { return itoa(int(v+0.5)) + "%" }
