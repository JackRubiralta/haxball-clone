package sim

import (
	"testing"

	"phootball/internal/config"
)

// TestBuildMatchSizedCounts verifies the additive sized builder lays out asymmetric
// rosters (3 home, 2 away) with each team's keeper at index 0 / number 1.
func TestBuildMatchSizedCounts(t *testing.T) {
	m := BuildMatchSized(NewStandardField(), 3, 2)
	home, away := m.Teams[0], m.Teams[1]
	if home.Side != SideLeft || away.Side != SideRight {
		t.Fatalf("team sides: home=%v away=%v", home.Side, away.Side)
	}
	if len(home.Players) != 3 {
		t.Errorf("home roster = %d, want 3", len(home.Players))
	}
	if len(away.Players) != 2 {
		t.Errorf("away roster = %d, want 2", len(away.Players))
	}
	if len(m.Players) != 5 {
		t.Errorf("total players = %d, want 5", len(m.Players))
	}
	for _, tm := range m.Teams {
		if tm.Players[0].Role != RoleKeeper {
			t.Errorf("%v P0 role = %v, want goalkeeper", tm.Side, tm.Players[0].Role)
		}
		if tm.Players[0].Number != 1 {
			t.Errorf("%v keeper number = %d, want 1", tm.Side, tm.Players[0].Number)
		}
	}
	// Player IDs are unique across both teams.
	seen := map[int]bool{}
	for _, p := range m.Players {
		if seen[p.PlayerID] {
			t.Errorf("duplicate player id %d", p.PlayerID)
		}
		seen[p.PlayerID] = true
	}
}

// TestAdvanceRulesHybrid: a hybrid match ends EARLY when a team reaches the target, OR
// when the clock expires -- whichever comes first.
func TestAdvanceRulesHybrid(t *testing.T) {
	// Ends on the target before the clock.
	t.Run("target first", func(t *testing.T) {
		m := BuildMatchFromConfig(NewStandardField(), 2, config.Default())
		m.Rules = config.HybridRuleset(600, 2) // 10 minutes, first-to-2
		m.Teams[0].Score = 2
		m.celebrate = 0.0001
		m.advanceRules(1.0) // celebration elapses -> afterCelebration
		if !m.Finished() {
			t.Fatal("hybrid should finish once a team reaches the target")
		}
		if m.Winner() != m.Teams[0].Side {
			t.Errorf("winner = %v, want %v", m.Winner(), m.Teams[0].Side)
		}
	})

	// Ends on the clock when the target is never met.
	t.Run("clock first", func(t *testing.T) {
		m := BuildMatchFromConfig(NewStandardField(), 2, config.Default())
		m.Rules = config.HybridRuleset(5, 9) // 5 seconds, first-to-9 (unreachable here)
		m.Teams[0].Score = 1
		m.Clock = 5
		m.advanceRules(1.0 / 60)
		if !m.Finished() {
			t.Fatal("hybrid should finish when the clock expires")
		}
		if m.Winner() != m.Teams[0].Side {
			t.Errorf("winner = %v, want the leader %v", m.Winner(), m.Teams[0].Side)
		}
	})

	// ClockSeconds counts down for the hybrid like a timed match.
	t.Run("clock counts down", func(t *testing.T) {
		m := BuildMatchFromConfig(NewStandardField(), 2, config.Default())
		m.Rules = config.HybridRuleset(60, 3)
		m.Clock = 20
		if got := m.ClockSeconds(); got != 40 {
			t.Errorf("ClockSeconds = %g, want 40 (counting down)", got)
		}
	})
}
