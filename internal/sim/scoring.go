package sim

// Scoring-attribution tuning.
const (
	assistWindow     = 6.0 // seconds: how far back a same-team pass still counts as an assist
	deflectionWindow = 0.6 // seconds: a defender clipping a shot this recently keeps the shooter's credit
	touchHistoryCap  = 8   // distinct recent touches retained
)

// TouchKind distinguishes a dribble/contact touch from a deliberate kick.
type TouchKind int

const (
	TouchDribble TouchKind = iota
	TouchKick
)

// Touch records one player's contact with the ball.
type Touch struct {
	Player int
	Side   Side
	Kind   TouchKind
	Time   float64 // match clock when it occurred
}

// ScoreEvent is the resolved attribution of a goal.
type ScoreEvent struct {
	Team        Side // the team credited the goal
	GoalEntered Side // which goal the ball crossed
	Scorer      int  // player credited (the author, even for an own goal)
	HasScorer   bool
	OwnGoal     bool
	Assist      int
	HasAssist   bool
	Deflected   bool // a defender clipped a shot that was already going in
	Tick        uint64
	Time        float64
}

// recordTouch logs that a player touched the ball. Repeated contact by the same player
// collapses into one history entry (its timestamp refreshed, and upgraded to a kick),
// so the history holds distinct touchers for assist/deflection look-ups.
func (m *Match) recordTouch(p *Player, kind TouchKind) {
	isNew := false
	if n := len(m.touchHistory); n > 0 && m.touchHistory[n-1].Player == p.PlayerID {
		m.touchHistory[n-1].Time = m.Clock
		if kind == TouchKick {
			m.touchHistory[n-1].Kind = TouchKick
		}
	} else {
		m.touchHistory = append(m.touchHistory, Touch{Player: p.PlayerID, Side: p.Team.Side, Kind: kind, Time: m.Clock})
		if len(m.touchHistory) > touchHistoryCap {
			m.touchHistory = m.touchHistory[len(m.touchHistory)-touchHistoryCap:]
		}
		isNew = true
	}
	last := m.touchHistory[len(m.touchHistory)-1]
	m.LastTouch = &last
	m.rec.onTouch(m, p, kind, isNew)
}

// resolveGoal credits a goal from the touch history, detecting own goals and deflected
// shots (football-style: a shot already heading in keeps the shooter's credit even off
// a defender; only a genuine defender redirect is an own goal). It increments the score
// and records the event.
func (m *Match) resolveGoal(goalEntered Side) ScoreEvent {
	scoringTeam := goalEntered.Opponent()
	ev := ScoreEvent{Team: scoringTeam, GoalEntered: goalEntered, Tick: m.Tick, Time: m.Clock}

	if last := m.LastTouch; last != nil {
		switch {
		case last.Side == scoringTeam:
			// Normal goal: the last toucher scores; look back for an assist.
			ev.Scorer, ev.HasScorer = last.Player, true
			if a, ok := m.findAssist(scoringTeam, last.Player); ok {
				ev.Assist, ev.HasAssist = a, true
			}
		default:
			// Last touch by the conceding team. If a scoring-team shot was on its way in
			// and a defender merely clipped it, the shooter keeps the goal; otherwise it
			// is a genuine own goal.
			if shooter, ok := m.deflectionShooter(scoringTeam, last.Time); ok {
				ev.Scorer, ev.HasScorer, ev.Deflected = shooter, true, true
			} else {
				ev.Scorer, ev.HasScorer, ev.OwnGoal = last.Player, true, true
			}
		}
	}

	m.addScore(goalEntered)
	m.Goals = append(m.Goals, ev)
	m.LastGoal = &m.Goals[len(m.Goals)-1]
	m.rec.onGoal(m, ev)
	return ev
}

// findAssist returns the most recent distinct same-team toucher before the scorer,
// within the assist window, aborting if an opponent touched in between.
func (m *Match) findAssist(team Side, scorer int) (int, bool) {
	for i := len(m.touchHistory) - 2; i >= 0; i-- {
		t := m.touchHistory[i]
		if m.Clock-t.Time > assistWindow {
			return 0, false
		}
		if t.Side != team {
			return 0, false // an opponent intervened
		}
		if t.Player != scorer {
			return t.Player, true
		}
	}
	return 0, false
}

// deflectionShooter returns the scoring-team player whose recent kick was deflected in
// by the defender's touch, if the touch right before the defender's was such a shot.
func (m *Match) deflectionShooter(scoringTeam Side, defenderTouchTime float64) (int, bool) {
	if n := len(m.touchHistory); n >= 2 {
		prev := m.touchHistory[n-2]
		if prev.Side == scoringTeam && prev.Kind == TouchKick && defenderTouchTime-prev.Time <= deflectionWindow {
			return prev.Player, true
		}
	}
	return 0, false
}
