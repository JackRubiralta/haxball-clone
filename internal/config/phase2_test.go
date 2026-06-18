package config

import "testing"

// TestGeometryValidateRelational checks the relational box-nesting and pitch-proportion
// constraints on a resolved geometry.
func TestGeometryValidateRelational(t *testing.T) {
	if err := StandardGeometry().Validate(); err != nil {
		t.Fatalf("standard preset must validate: %v", err)
	}

	cases := []struct {
		name    string
		mutate  func(g *Geometry)
		wantErr bool
	}{
		{"standard ok", func(g *Geometry) {}, false},
		{"pitch wider than long", func(g *Geometry) { g.PlayHeight = g.PlayWidth + 1 }, true},
		{"goal mouth wider than pitch", func(g *Geometry) { g.GoalMouthWidth = g.PlayHeight + 1 }, true},
		{"goal area narrower than mouth", func(g *Geometry) { g.GoalAreaWidth = g.GoalMouthWidth - 1 }, true},
		{"goal area shallower than goal depth", func(g *Geometry) { g.GoalAreaDepth = g.GoalPocketDepth - 1 }, true},
		{"penalty narrower than goal area", func(g *Geometry) { g.PenaltyWidth = g.GoalAreaWidth - 1 }, true},
		{"penalty shallower than goal area", func(g *Geometry) { g.PenaltyDepth = g.GoalAreaDepth - 1 }, true},
		{"penalty wider than pitch", func(g *Geometry) { g.PenaltyWidth = g.PlayHeight + 1 }, true},
		{"penalty depth exceeds half-length", func(g *Geometry) { g.PenaltyDepth = g.PlayWidth }, true},
		{"boxes off skips nesting", func(g *Geometry) {
			g.HasPenaltyArea, g.HasGoalArea = false, false
			g.PenaltyWidth, g.GoalAreaWidth = 1, 2 // invalid nesting, but ignored when off
		}, false},
	}
	for _, tc := range cases {
		g := StandardGeometry()
		tc.mutate(&g)
		err := g.Validate()
		if (err != nil) != tc.wantErr {
			t.Errorf("%s: err=%v, wantErr=%v", tc.name, err, tc.wantErr)
		}
	}
}

// TestMatchSetupSizes verifies the per-team size fallback to TeamSize.
func TestMatchSetupSizes(t *testing.T) {
	cases := []struct {
		s          MatchSetup
		home, away int
	}{
		{MatchSetup{TeamSize: 3}, 3, 3},
		{MatchSetup{TeamSize: 3, HomeSize: 5}, 5, 3},
		{MatchSetup{TeamSize: 3, AwaySize: 1}, 3, 1},
		{MatchSetup{TeamSize: 4, HomeSize: 2, AwaySize: 6}, 2, 6},
	}
	for _, tc := range cases {
		h, a := tc.s.sizes()
		if h != tc.home || a != tc.away {
			t.Errorf("%+v sizes = (%d,%d), want (%d,%d)", tc.s, h, a, tc.home, tc.away)
		}
	}
}

// TestMatchSetupValidateSizes: per-team min of 1 is enforced through the resolved sizes.
func TestMatchSetupValidateSizes(t *testing.T) {
	base := DefaultMatchSetup()
	if err := base.Validate(); err != nil {
		t.Fatalf("default must validate: %v", err)
	}
	s := base
	s.TeamSize, s.HomeSize, s.AwaySize = 0, 1, 1 // TeamSize 0 but both teams overridden -> ok
	if err := s.Validate(); err != nil {
		t.Errorf("explicit per-team sizes should validate without TeamSize: %v", err)
	}
	s = base
	s.AwaySize = 0
	s.TeamSize = 0 // away falls back to 0 -> invalid
	if err := s.Validate(); err == nil {
		t.Error("expected an error when a resolved team size is below 1")
	}
}

// TestRulesetWinCondition maps each orthogonal win-condition combination.
func TestRulesetWinCondition(t *testing.T) {
	cases := []struct {
		name       string
		goals      bool
		time       bool
		want       WinCondition
		wantTarget int
		wantSecs   float64
	}{
		{"neither -> friendly", false, false, WinFriendly, 0, 0},
		{"goals only -> first to score", true, false, WinFirstToScore, 3, 0},
		{"time only -> timed", false, true, WinTimed, 0, 300},
		{"both -> first and timed", true, true, WinFirstAndTimed, 3, 300},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := DefaultMatchSetup()
			s.WinByGoals, s.WinByTime = tc.goals, tc.time
			s.WinScore, s.Minutes = 3, 5
			r, err := s.Ruleset()
			if err != nil {
				t.Fatal(err)
			}
			if r.Win != tc.want {
				t.Errorf("Win = %v, want %v", r.Win, tc.want)
			}
			if r.ScoreTarget != tc.wantTarget {
				t.Errorf("ScoreTarget = %d, want %d", r.ScoreTarget, tc.wantTarget)
			}
			if r.RegulationSeconds != tc.wantSecs {
				t.Errorf("RegulationSeconds = %g, want %g", r.RegulationSeconds, tc.wantSecs)
			}
		})
	}
}

// TestRulesetDrawResolution maps each draw-resolution combination: fixed extra time vs
// golden-goal sudden death, direct penalties (pens without extra time), the full chain,
// and a bare draw (empty chain).
func TestRulesetDrawResolution(t *testing.T) {
	base := func() MatchSetup {
		s := DefaultMatchSetup()
		s.WinByTime, s.Minutes = true, 5
		return s
	}

	t.Run("a draw stands when no resolution", func(t *testing.T) {
		r, err := base().Ruleset()
		if err != nil {
			t.Fatal(err)
		}
		if len(r.OnDraw) != 0 {
			t.Errorf("OnDraw = %v, want empty", r.OnDraw)
		}
	})

	t.Run("fixed extra time", func(t *testing.T) {
		s := base()
		s.ExtraTime, s.ExtraMinutes = true, 2
		r, err := s.Ruleset()
		if err != nil {
			t.Fatal(err)
		}
		if len(r.OnDraw) != 1 || r.OnDraw[0] != ContinueExtraTime {
			t.Fatalf("OnDraw = %v, want [extra time]", r.OnDraw)
		}
		if r.ExtraTimeSeconds != 120 {
			t.Errorf("ExtraTimeSeconds = %g, want 120", r.ExtraTimeSeconds)
		}
	})

	t.Run("golden goal makes extra time sudden death", func(t *testing.T) {
		s := base()
		s.ExtraTime, s.GoldenGoal = true, true
		r, err := s.Ruleset()
		if err != nil {
			t.Fatal(err)
		}
		if len(r.OnDraw) != 1 || r.OnDraw[0] != ContinueGoldenGoal {
			t.Fatalf("OnDraw = %v, want [golden goal] (not extra time)", r.OnDraw)
		}
		if r.GoldenGoalSeconds != 0 {
			t.Errorf("GoldenGoalSeconds = %g, want 0 (until a goal)", r.GoldenGoalSeconds)
		}
	})

	t.Run("direct pens: penalties without extra time", func(t *testing.T) {
		s := base()
		s.Penalties, s.PenaltyBestOf = true, 5
		r, err := s.Ruleset()
		if err != nil {
			t.Fatal(err)
		}
		if len(r.OnDraw) != 1 || r.OnDraw[0] != ContinuePenalties {
			t.Errorf("OnDraw = %v, want [penalties]", r.OnDraw)
		}
	})

	t.Run("full chain: extra time then penalties", func(t *testing.T) {
		s := base()
		s.ExtraTime, s.ExtraMinutes = true, 2
		s.Penalties, s.PenaltyBestOf = true, 3
		r, err := s.Ruleset()
		if err != nil {
			t.Fatal(err)
		}
		want := []Continuation{ContinueExtraTime, ContinuePenalties}
		if len(r.OnDraw) != len(want) {
			t.Fatalf("OnDraw = %v, want %v", r.OnDraw, want)
		}
		for i := range want {
			if r.OnDraw[i] != want[i] {
				t.Errorf("OnDraw[%d] = %v, want %v", i, r.OnDraw[i], want[i])
			}
		}
		if r.Penalties.BestOf != 3 {
			t.Errorf("BestOf = %d, want 3", r.Penalties.BestOf)
		}
	})

	t.Run("golden goal then penalties", func(t *testing.T) {
		s := base()
		s.ExtraTime, s.GoldenGoal = true, true
		s.Penalties, s.PenaltyBestOf = true, 5
		r, err := s.Ruleset()
		if err != nil {
			t.Fatal(err)
		}
		want := []Continuation{ContinueGoldenGoal, ContinuePenalties}
		if len(r.OnDraw) != len(want) {
			t.Fatalf("OnDraw = %v, want %v", r.OnDraw, want)
		}
		for i := range want {
			if r.OnDraw[i] != want[i] {
				t.Errorf("OnDraw[%d] = %v, want %v", i, r.OnDraw[i], want[i])
			}
		}
	})
}
