package sim

import "phootball/internal/config"

// advanceRules drives the match state machine each tick: it runs the goal celebration,
// detects fresh goals, and applies the win condition and the draw-resolution chain
// (extra time, golden goal, penalties). It is deterministic and replaces the original
// inline goal handling.
func (m *Match) advanceRules(deltaTime float64) {
	// Goal celebration: a pause-free kickoff countdown. Play keeps simulating during it
	// but no new goal is counted; when it elapses we decide what happens next.
	if m.celebrate > 0 {
		m.celebrate -= deltaTime
		if m.celebrate <= 0 {
			m.celebrate = 0
			m.afterCelebration()
		}
		return
	}

	// A fresh goal starts the celebration; the outcome is resolved when it elapses.
	if side := m.Field.CheckGoal(m.Ball); side != SideNone {
		m.onGoal(side)
		m.celebrate = m.Rules.CelebrationSeconds
		return
	}

	// Time-based progression for the current phase.
	switch m.State.Phase {
	case PhasePlaying:
		if m.Rules.Win == config.WinTimed && m.Rules.RegulationSeconds > 0 && m.Clock >= m.Rules.RegulationSeconds {
			m.endRegulation()
		}
	case PhaseExtraTime:
		if m.Clock-m.State.PhaseStart >= m.Rules.ExtraTimeSeconds {
			m.endStage()
		}
	case PhaseGoldenGoal:
		if m.Rules.GoldenGoalSeconds > 0 && m.Clock-m.State.PhaseStart >= m.Rules.GoldenGoalSeconds {
			m.endStage()
		}
	}
}

// onGoal credits a goal with full attribution (scorer, own goal, deflection, assist).
func (m *Match) onGoal(side Side) {
	m.resolveGoal(side)
	m.emit(SoundGoal, 1, m.Ball.Position)
}

// afterCelebration decides what happens once the kickoff countdown elapses: end a
// sudden-death stage, end a first-to-N match, or kick off again.
func (m *Match) afterCelebration() {
	if m.State.Phase == PhaseGoldenGoal {
		m.finish(m.leader())
		return
	}
	if m.Rules.Win == config.WinFirstToScore && m.Rules.ScoreTarget > 0 {
		if m.Teams[0].Score >= m.Rules.ScoreTarget || m.Teams[1].Score >= m.Rules.ScoreTarget {
			m.finish(m.leader())
			return
		}
	}
	m.resetKickoff()
}

// endRegulation is reached when timed regulation expires: the leader wins, or a level
// score starts the draw-resolution chain.
func (m *Match) endRegulation() {
	if w := m.leader(); w != SideNone {
		m.finish(w)
		return
	}
	m.State.ChainIndex = 0
	m.beginStage()
}

// beginStage starts the continuation at the current chain index.
func (m *Match) beginStage() {
	if m.State.ChainIndex >= len(m.Rules.OnDraw) {
		m.finish(SideNone) // a level match with no (more) deciders is a draw
		return
	}
	switch m.Rules.OnDraw[m.State.ChainIndex] {
	case config.ContinueExtraTime:
		m.State.Phase = PhaseExtraTime
		m.State.PhaseStart = m.Clock
		m.resetKickoff()
	case config.ContinueGoldenGoal:
		m.State.Phase = PhaseGoldenGoal
		m.State.PhaseStart = m.Clock
		m.resetKickoff()
	case config.ContinuePenalties:
		m.beginShootout() // the playable shootout; falls back to a draw until it is built
	}
}

// endStage is reached when a timed continuation (extra time, time-limited golden goal)
// ends still level: the leader wins, otherwise move to the next continuation.
func (m *Match) endStage() {
	if w := m.leader(); w != SideNone {
		m.finish(w)
		return
	}
	m.State.ChainIndex++
	m.beginStage()
}

// leader returns the side currently ahead on the scoreboard, or SideNone if level.
func (m *Match) leader() Side {
	l, r := m.Teams[0].Score, m.Teams[1].Score
	switch {
	case l > r:
		return m.Teams[0].Side
	case r > l:
		return m.Teams[1].Side
	default:
		return SideNone
	}
}

// finish ends the match with the given winner (SideNone is a draw).
func (m *Match) finish(winner Side) {
	m.State.Phase = PhaseFinished
	m.State.Winner = winner
}

// Frozen reports whether the simulation should not advance this tick: the match is
// paused or finished.
func (m *Match) Frozen() bool {
	return m.Paused || m.State.Phase == PhaseFinished
}

// Finished reports whether the match is over.
func (m *Match) Finished() bool { return m.State.Phase == PhaseFinished }

// Winner returns the winning side once finished (SideNone is a draw or still playing).
func (m *Match) Winner() Side { return m.State.Winner }

// Phase returns the current match phase.
func (m *Match) Phase() Phase { return m.State.Phase }

// PhaseLabel returns the scoreboard label for the current phase (empty during ordinary
// play).
func (m *Match) PhaseLabel() string { return m.State.Phase.String() }

// ClockSeconds returns the time to show on the scoreboard: the regulation or extra-time
// countdown when timed, otherwise the elapsed time in the current phase.
func (m *Match) ClockSeconds() float64 {
	switch m.State.Phase {
	case PhasePlaying:
		if m.Rules.Win == config.WinTimed && m.Rules.RegulationSeconds > 0 {
			return clampPos(m.Rules.RegulationSeconds - m.Clock)
		}
		return m.Clock
	case PhaseExtraTime:
		return clampPos(m.Rules.ExtraTimeSeconds - (m.Clock - m.State.PhaseStart))
	default:
		return m.Clock - m.State.PhaseStart
	}
}

func clampPos(x float64) float64 {
	if x < 0 {
		return 0
	}
	return x
}
