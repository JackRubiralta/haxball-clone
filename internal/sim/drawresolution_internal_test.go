package sim

import (
	"testing"

	"phootball/internal/config"
)

// buildFromSetup resolves a MatchSetup to a Config and builds a 2v2 match, so the smoke
// tests exercise the same config->ruleset->engine path the lobby and CLI use.
func buildFromSetup(t *testing.T, s config.MatchSetup) *Match {
	t.Helper()
	cfg, err := s.Build()
	if err != nil {
		t.Fatalf("build config: %v", err)
	}
	return BuildMatchFromConfig(NewStandardField(), 2, cfg)
}

// timedDrawSetup is a 5-second timed match (so regulation can expire in a test) plus the
// caller's draw-resolution choices.
func timedDrawSetup() config.MatchSetup {
	s := config.DefaultMatchSetup()
	s.WinByTime, s.Minutes = true, 5.0/60.0 // 5 seconds of regulation
	s.ExtraMinutes = 5.0 / 60.0             // 5 seconds of extra time / golden-goal cap
	return s
}

// expireRegulation drives the clock to the regulation whistle with the scores level.
func expireRegulation(m *Match) {
	m.Clock = m.Rules.RegulationSeconds
	m.advanceRules(1.0 / 60)
}

// scoreGoldenGoal simulates a goal during sudden death: bump the score and run the
// celebration to completion, which is where afterCelebration decides the golden winner.
func scoreGoldenGoal(m *Match, teamIdx int) {
	m.Teams[teamIdx].Score++
	m.celebrate = 0.0001
	m.advanceRules(1.0)
}

// TestDrawResolutionTerminates is the sim-level smoke test: representative draw-resolution
// chains, each built from config, must TERMINATE in the right phase when regulation ends
// level.
func TestDrawResolutionTerminates(t *testing.T) {
	t.Run("timed draw + draw-stands -> finishes a draw", func(t *testing.T) {
		m := buildFromSetup(t, timedDrawSetup())
		expireRegulation(m)
		if !m.Finished() {
			t.Fatalf("expected the match to finish, phase=%v", m.Phase())
		}
		if m.Winner() != SideNone {
			t.Errorf("winner = %v, want a draw (SideNone)", m.Winner())
		}
	})

	t.Run("timed draw + direct pens -> enters PhasePenalties", func(t *testing.T) {
		s := timedDrawSetup()
		s.Penalties, s.PenaltyBestOf = true, 5
		m := buildFromSetup(t, s)
		expireRegulation(m)
		if m.Phase() != PhasePenalties {
			t.Fatalf("phase = %v, want PhasePenalties", m.Phase())
		}
		if !m.InShootout() {
			t.Error("expected the shootout to be live")
		}
	})

	t.Run("timed draw + ET-golden-capped: a goal in ET wins", func(t *testing.T) {
		s := timedDrawSetup()
		s.ExtraTime, s.GoldenGoal, s.GoldenGoalCapped = true, true, true
		m := buildFromSetup(t, s)
		expireRegulation(m)
		if m.Phase() != PhaseGoldenGoal {
			t.Fatalf("phase = %v, want PhaseGoldenGoal", m.Phase())
		}
		scoreGoldenGoal(m, 0)
		if !m.Finished() {
			t.Fatalf("expected a golden goal to finish the match, phase=%v", m.Phase())
		}
		if m.Winner() != m.Teams[0].Side {
			t.Errorf("winner = %v, want %v", m.Winner(), m.Teams[0].Side)
		}
	})

	t.Run("timed draw + ET-golden-capped: no goal by the cap proceeds to a draw", func(t *testing.T) {
		s := timedDrawSetup()
		s.ExtraTime, s.GoldenGoal, s.GoldenGoalCapped = true, true, true
		m := buildFromSetup(t, s)
		expireRegulation(m)
		if m.Phase() != PhaseGoldenGoal {
			t.Fatalf("phase = %v, want PhaseGoldenGoal", m.Phase())
		}
		// Run past the golden-goal cap with no goal: endStage -> next continuation (none) -> draw.
		m.Clock = m.State.PhaseStart + m.Rules.GoldenGoalSeconds
		m.advanceRules(1.0 / 60)
		if !m.Finished() {
			t.Fatalf("expected the capped golden goal to end, phase=%v", m.Phase())
		}
		if m.Winner() != SideNone {
			t.Errorf("winner = %v, want a draw after an empty golden goal", m.Winner())
		}
	})

	t.Run("timed draw + ET-golden-capped then pens: cap with no goal -> shootout", func(t *testing.T) {
		s := timedDrawSetup()
		s.ExtraTime, s.GoldenGoal, s.GoldenGoalCapped = true, true, true
		s.Penalties, s.PenaltyBestOf = true, 5
		m := buildFromSetup(t, s)
		expireRegulation(m)
		if m.Phase() != PhaseGoldenGoal {
			t.Fatalf("phase = %v, want PhaseGoldenGoal", m.Phase())
		}
		m.Clock = m.State.PhaseStart + m.Rules.GoldenGoalSeconds
		m.advanceRules(1.0 / 60)
		if m.Phase() != PhasePenalties {
			t.Fatalf("phase = %v, want PhasePenalties after an empty capped golden goal", m.Phase())
		}
	})
}
