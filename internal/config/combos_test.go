package config

import (
	"reflect"
	"testing"
)

// TestRulesetAllCombos enumerates every win-condition x draw-resolution combination and
// asserts the full Ruleset mapping: the win condition, score target, regulation length,
// the exact OnDraw chain, the extra-time / golden-goal seconds, and the shootout best-of.
// This is the single source of truth that MatchSetup.Ruleset wires every menu/CLI option
// through to the simulation engine correctly.
func TestRulesetAllCombos(t *testing.T) {
	// Win-condition axis: the four orthogonal {goals, time} combinations.
	type winCase struct {
		name       string
		goals      bool
		time       bool
		wantWin    WinCondition
		wantTarget int
		wantReg    float64
	}
	winCases := []winCase{
		{"neither", false, false, WinFriendly, 0, 0},
		{"goals", true, false, WinFirstToScore, 4, 0},
		{"time", false, true, WinTimed, 0, 300},
		{"both", true, true, WinFirstAndTimed, 4, 300},
	}

	// Draw-resolution axis: every chain the orthogonal fields can produce.
	type drawCase struct {
		name      string
		extraTime bool
		golden    bool
		capped    bool
		pens      bool
		bestOf    int
		wantChain []Continuation
		wantET    float64
		wantGG    float64
		wantBestO int
	}
	drawCases := []drawCase{
		{
			name: "draw-stands", wantChain: nil,
		},
		{
			name: "ET-fixed", extraTime: true,
			wantChain: []Continuation{ContinueExtraTime}, wantET: 120,
		},
		{
			name: "ET-golden-uncapped", extraTime: true, golden: true,
			wantChain: []Continuation{ContinueGoldenGoal}, wantGG: 0,
		},
		{
			name: "ET-golden-capped", extraTime: true, golden: true, capped: true,
			wantChain: []Continuation{ContinueGoldenGoal}, wantGG: 120,
		},
		{
			name: "pens-direct", pens: true, bestOf: 5,
			wantChain: []Continuation{ContinuePenalties}, wantBestO: 5,
		},
		{
			name: "ET-then-pens", extraTime: true, pens: true, bestOf: 3,
			wantChain: []Continuation{ContinueExtraTime, ContinuePenalties}, wantET: 120, wantBestO: 3,
		},
		{
			name: "ET-golden-then-pens", extraTime: true, golden: true, pens: true, bestOf: 5,
			wantChain: []Continuation{ContinueGoldenGoal, ContinuePenalties}, wantGG: 0, wantBestO: 5,
		},
		{
			name: "ET-golden-capped-then-pens", extraTime: true, golden: true, capped: true, pens: true, bestOf: 7,
			wantChain: []Continuation{ContinueGoldenGoal, ContinuePenalties}, wantGG: 120, wantBestO: 7,
		},
	}

	for _, wc := range winCases {
		for _, dc := range drawCases {
			t.Run(wc.name+"/"+dc.name, func(t *testing.T) {
				s := DefaultMatchSetup()
				s.WinByGoals, s.WinByTime = wc.goals, wc.time
				s.WinScore, s.Minutes = 4, 5
				s.ExtraTime, s.GoldenGoal, s.GoldenGoalCapped = dc.extraTime, dc.golden, dc.capped
				s.ExtraMinutes = 2
				s.Penalties, s.PenaltyBestOf = dc.pens, dc.bestOf

				// Every combo must validate (the lobby/CLI gate on this).
				if err := s.Validate(); err != nil {
					t.Fatalf("Validate: %v", err)
				}
				r, err := s.Ruleset()
				if err != nil {
					t.Fatalf("Ruleset: %v", err)
				}

				if r.Win != wc.wantWin {
					t.Errorf("Win = %v, want %v", r.Win, wc.wantWin)
				}
				if r.ScoreTarget != wc.wantTarget {
					t.Errorf("ScoreTarget = %d, want %d", r.ScoreTarget, wc.wantTarget)
				}
				if r.RegulationSeconds != wc.wantReg {
					t.Errorf("RegulationSeconds = %g, want %g", r.RegulationSeconds, wc.wantReg)
				}
				if !reflect.DeepEqual(r.OnDraw, dc.wantChain) {
					t.Errorf("OnDraw = %v, want %v", r.OnDraw, dc.wantChain)
				}
				if r.ExtraTimeSeconds != dc.wantET {
					t.Errorf("ExtraTimeSeconds = %g, want %g", r.ExtraTimeSeconds, dc.wantET)
				}
				if r.GoldenGoalSeconds != dc.wantGG {
					t.Errorf("GoldenGoalSeconds = %g, want %g", r.GoldenGoalSeconds, dc.wantGG)
				}
				if dc.pens && r.Penalties.BestOf != dc.wantBestO {
					t.Errorf("Penalties.BestOf = %d, want %d", r.Penalties.BestOf, dc.wantBestO)
				}
			})
		}
	}
}

// TestGoldenGoalCappedValidation checks that a capped golden goal (like fixed extra time)
// requires a positive cap, while an uncapped golden goal does not.
func TestGoldenGoalCappedValidation(t *testing.T) {
	base := func() MatchSetup {
		s := DefaultMatchSetup()
		s.WinByTime, s.Minutes = true, 5
		s.ExtraTime, s.GoldenGoal = true, true
		return s
	}

	t.Run("uncapped golden goal needs no extra minutes", func(t *testing.T) {
		s := base()
		s.ExtraMinutes = 0 // ignored when uncapped
		if err := s.Validate(); err != nil {
			t.Errorf("uncapped golden goal should validate with zero extra minutes: %v", err)
		}
	})

	t.Run("capped golden goal requires positive extra minutes", func(t *testing.T) {
		s := base()
		s.GoldenGoalCapped = true
		s.ExtraMinutes = 0
		if err := s.Validate(); err == nil {
			t.Error("capped golden goal must reject zero extra minutes")
		}
		s.ExtraMinutes = 3
		if err := s.Validate(); err != nil {
			t.Errorf("capped golden goal should validate with positive extra minutes: %v", err)
		}
	})
}
