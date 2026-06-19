package aifactory_test

import (
	"testing"

	"phootball/internal/aifactory"
	"phootball/internal/config"
	"phootball/internal/control"
	"phootball/internal/control/neural"
	"phootball/internal/netcode"
	"phootball/internal/sim"
)

// Compile-time proof that the factory's product satisfies both interfaces it must drop into.
var (
	_ control.Controller = aifactory.New(0, control.SkillHard)
	_ netcode.Bot        = aifactory.New(0, control.SkillHard)
)

func TestNewBranchesByTier(t *testing.T) {
	if c := aifactory.New(0, control.SkillHard); c == nil {
		t.Fatal("nil AI controller for SkillHard")
	}
	c := aifactory.New(0, control.SkillNeural)
	if c == nil {
		t.Fatal("nil controller for SkillNeural")
	}
	if _, ok := c.(*neural.Controller); !ok {
		t.Fatalf("SkillNeural produced %T, want *neural.Controller", c)
	}
}

// TestNeuralTierRunsInMatch builds an all-neural match through the factory (loading the embedded
// net) and steps it, confirming the SkillNeural wiring works end-to-end and stays human-reachable.
func TestNeuralTierRunsInMatch(t *testing.T) {
	cfg := config.Default()
	field := sim.NewFieldFromGeometry(cfg.Geometry)
	m := sim.BuildMatchFromConfig(field, 3, cfg)
	ctrls := make(map[int]control.Controller, len(m.Players))
	for _, p := range m.Players {
		ctrls[p.PlayerID] = aifactory.New(p.PlayerID, control.SkillNeural)
	}
	for i := 0; i < 120; i++ {
		in := make(map[int]sim.Intent, len(ctrls))
		for id, c := range ctrls {
			it := c.Intent(m.View())
			if it.Trap && it.Push {
				t.Fatalf("tick %d: trap+push", i)
			}
			in[id] = it
		}
		m.Step(in, 1.0/60.0)
	}
}
