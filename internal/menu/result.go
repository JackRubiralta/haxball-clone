package menu

import (
	"image/color"
	"strconv"

	"phootball/internal/sim"
)

// ResultModel is the plain-data summary of a finished match, assembled from the live
// *sim.Match once so screenResult's draw code stays declarative (and the layout math is
// identical in both immediate-mode passes). It carries no Ebiten/sim references.
type ResultModel struct {
	Teams [2]ResultTeam // [0] = home/left/Blue, [1] = away/right/Red

	WinnerLine string     // "BLUE WINS" / "RED WINS" / "DRAW"
	WinnerIdx  int        // index into Teams of the winner, or -1 for a draw
	WinnerTint color.RGBA // colour for the winner banner (winner's colour, or neutral on a draw)
	ContextTag string     // "(on penalties)" / "(a.e.t.)" / "" (regulation)

	Goals []ResultGoal // every goal in chronological order

	HasShootout bool
	Shootout    [2]ResultShootout // per-side penalty results, in taking order
}

// ResultTeam is one side's header chip data.
type ResultTeam struct {
	Name  string
	Color color.RGBA
	Score int
}

// ResultGoal is one row of the goal timeline.
type ResultGoal struct {
	TeamIdx int        // 0 home, 1 away (the credited team)
	Color   color.RGBA // the credited team's colour
	Time    string     // mm:ss
	Scorer  string     // "#7" or "" when unattributed
	Detail  string     // "(assist #9)", "(OG)", "(deflected)" combined, or ""
}

// ResultShootout is one side's row of penalty dots plus its tally.
type ResultShootout struct {
	Scored []bool // one entry per kick this side took, in order
	Goals  int    // tally (goals scored in the shootout)
}

// buildResult assembles the ResultModel from the finished match. It maps player IDs to
// jersey numbers, formats goal times, and tags the result (penalties / a.e.t.).
func buildResult(m *sim.Match) ResultModel {
	var r ResultModel
	if m == nil {
		r.WinnerIdx = -1
		return r
	}

	for i, t := range m.Teams {
		if t == nil {
			continue
		}
		r.Teams[i] = ResultTeam{Name: t.Name, Color: t.Color, Score: t.Score}
	}

	// Winner line + tint. Map the winning Side onto the team index.
	r.WinnerIdx = -1
	switch w := m.Winner(); w {
	case sim.SideNone:
		r.WinnerLine = "DRAW"
		r.WinnerTint = theme.Accent
	default:
		for i, t := range m.Teams {
			if t != nil && t.Side == w {
				r.WinnerIdx = i
				r.WinnerLine = teamUpper(t.Name) + " WINS"
				r.WinnerTint = t.Color
			}
		}
		if r.WinnerIdx < 0 { // winning side with no matching team (defensive)
			r.WinnerLine = "DRAW"
			r.WinnerTint = theme.Accent
		}
	}

	// Context tag: penalties beat a.e.t.; a.e.t. fires if any goal fell outside regulation.
	if l, rr := m.ShootoutScore(); m.InShootout() || l > 0 || rr > 0 || len(m.ShootoutKicks()) > 0 {
		r.ContextTag = "(on penalties)"
	} else if reg := m.Rules.RegulationSeconds; reg > 0 {
		for _, ev := range m.Goals {
			if ev.Time > reg {
				r.ContextTag = "(a.e.t.)"
				break
			}
		}
	}

	// Goal timeline.
	for _, ev := range m.Goals {
		g := ResultGoal{Time: formatMMSS(ev.Time)}
		g.TeamIdx, g.Color = teamIndexFor(m, ev.Team)
		if ev.HasScorer {
			g.Scorer = "#" + strconv.Itoa(jersey(m, ev.Scorer))
		}
		g.Detail = goalDetail(m, ev)
		r.Goals = append(r.Goals, g)
	}

	// Shootout block.
	if kicks := m.ShootoutKicks(); len(kicks) > 0 {
		r.HasShootout = true
		for _, k := range kicks {
			idx, _ := teamIndexFor(m, k.Side)
			if idx < 0 || idx > 1 {
				continue
			}
			r.Shootout[idx].Scored = append(r.Shootout[idx].Scored, k.Scored)
		}
		l, rr := m.ShootoutScore()
		r.Shootout[0].Goals, r.Shootout[1].Goals = l, rr
	}

	return r
}

// teamIndexFor maps a Side to the Teams slot index and that team's colour.
func teamIndexFor(m *sim.Match, side sim.Side) (int, color.RGBA) {
	for i, t := range m.Teams {
		if t != nil && t.Side == side {
			return i, t.Color
		}
	}
	return -1, theme.Text
}

// goalDetail builds the combined "(assist #9)" / "(OG)" / "(deflected)" suffix for a goal.
func goalDetail(m *sim.Match, ev sim.ScoreEvent) string {
	var s string
	if ev.HasAssist {
		s = "(assist #" + strconv.Itoa(jersey(m, ev.Assist)) + ")"
	}
	switch {
	case ev.OwnGoal:
		s = appendTag(s, "(OG)")
	case ev.Deflected:
		s = appendTag(s, "(deflected)")
	}
	return s
}

func appendTag(s, tag string) string {
	if s == "" {
		return tag
	}
	return s + " " + tag
}

// jersey resolves a player ID to its jersey number (0 if unknown).
func jersey(m *sim.Match, id int) int {
	if p := m.PlayerByID(id); p != nil {
		return p.Number
	}
	return 0
}

// formatMMSS renders a match-clock time (seconds) as mm:ss.
func formatMMSS(sec float64) string {
	if sec < 0 {
		sec = 0
	}
	t := int(sec)
	ss := t % 60
	mm := t / 60
	out := strconv.Itoa(mm) + ":"
	if ss < 10 {
		out += "0"
	}
	return out + strconv.Itoa(ss)
}

// teamUpper upper-cases a team name for the winner banner without pulling in strings just
// for one call (names are short ASCII).
func teamUpper(s string) string {
	b := []byte(s)
	for i := range b {
		if b[i] >= 'a' && b[i] <= 'z' {
			b[i] -= 'a' - 'A'
		}
	}
	return string(b)
}
