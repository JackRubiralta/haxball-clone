package eval_test

import (
	"testing"

	"phootball/internal/control"
	"phootball/internal/eval"
)

func TestGatherRuns(t *testing.T) {
	// Carrier transitions in a single 600-tick AI roll-out are CHAOTIC: whether a given seed yields a
	// pass/turnover swings on tiny trajectory differences (a one-tick-earlier touch sends the ball to a
	// different player), so one seed is a coin-flip and a poor canary. Sum over a band of seeds so the
	// smoke test ("Hard AI play moves the ball between carriers") is robust to that chaos -- the
	// aggregate is stable, while a genuine freeze (the ball never changing hands at all) still trips it.
	total := 0
	for seed := int64(1); seed <= 24; seed++ {
		st := eval.BuildAIMatch(3, seed, control.SkillHard, nil).Gather(600)
		if st.Passes < 0 || st.Turnovers < 0 {
			t.Fatalf("seed %d negative counts: %+v", seed, st)
		}
		total += st.Passes + st.Turnovers
	}
	if total == 0 {
		t.Fatal("expected some carrier transitions over 24 seeds x 600 ticks of Hard AI")
	}
}

func TestBuildTeamMatch(t *testing.T) {
	m := eval.BuildTeamMatch(2, 2, control.SkillImpossible, control.SkillEasy, nil)
	if len(m.Controllers) != len(m.M.Players) {
		t.Fatalf("controllers=%d players=%d", len(m.Controllers), len(m.M.Players))
	}
	m.Run(120, nil)
}
