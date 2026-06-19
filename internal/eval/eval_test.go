package eval_test

import (
	"testing"

	"phootball/internal/control"
	"phootball/internal/eval"
)

func TestGatherRuns(t *testing.T) {
	m := eval.BuildAIMatch(3, 1, control.SkillHard, nil)
	st := m.Gather(600)
	if st.Passes < 0 || st.Turnovers < 0 {
		t.Fatalf("negative counts: %+v", st)
	}
	if st.Passes+st.Turnovers == 0 {
		t.Fatal("expected some carrier transitions over 600 ticks of Hard AI")
	}
}

func TestBuildTeamMatch(t *testing.T) {
	m := eval.BuildTeamMatch(2, 2, control.SkillImpossible, control.SkillEasy, nil)
	if len(m.Controllers) != len(m.M.Players) {
		t.Fatalf("controllers=%d players=%d", len(m.Controllers), len(m.M.Players))
	}
	m.Run(120, nil)
}
