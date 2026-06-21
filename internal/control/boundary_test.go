package control_test

import (
	"math"
	"testing"

	"phootball/internal/control"
	"phootball/internal/geom"
	"phootball/internal/sim"
)

// TestObservedViewCannotSeeHiddenState pins the type-level half of the AI<=human boundary:
// a handle on ANOTHER player (from Opponents/Teammates/Squad/Carrier) is an ObservedView and
// must NOT be assertable to SelfView, so the AI can never reach an opponent's hidden velocity,
// steering heading, or tuning (the un-rendered state). Only the controller's own Me handle is a
// SelfView. (Trap aura, possession, and the team buff/debuff ARE rendered for every player, so
// they live on ObservedView and are legitimately observable.)
func TestObservedViewCannotSeeHiddenState(t *testing.T) {
	m, _ := aiMatch(4, 1, control.SkillHard, nil)
	v := m.View()
	me, ok := v.Me(0)
	if !ok {
		t.Fatal("player 0 should be in the match")
	}

	// Every non-self handle must be an ObservedView that is NOT a SelfView.
	check := func(label string, o sim.ObservedView) {
		if o == nil {
			return
		}
		if _, ok := o.(sim.SelfView); ok {
			t.Errorf("%s handle satisfies SelfView -- the AI could read its hidden state", label)
		}
	}
	for _, o := range v.Opponents(me) {
		check("opponent", o)
	}
	for _, mate := range v.Teammates(me) {
		check("teammate", mate)
	}
	if c, ok := v.Carrier(); ok {
		check("carrier", c)
	}

	// The controller's OWN handle is a SelfView (it may read its own hidden state).
	if _, ok := interface{}(me).(sim.SelfView); !ok {
		t.Error("Me handle must be a SelfView")
	}
}

// TestNoSeedExposure pins that the raw RNG seed is no longer reachable through the View;
// variety comes from NoiseSalt instead.
func TestNoSeedExposure(t *testing.T) {
	m, _ := aiMatch(2, 7, control.SkillHard, nil)
	if _, ok := interface{}(m.View()).(interface{ Seed() int64 }); ok {
		t.Error("View must not expose Seed() -- it leaks hidden state")
	}
	if m.View().NoiseSalt(0) == m.View().NoiseSalt(1) {
		t.Error("NoiseSalt should differ per player id")
	}
}

// TestAIIntentsAreHumanReachable drives an AI-vs-AI match and asserts every emitted Intent is
// something a human could also produce: finite Move/Aim, Throttle in [0,1], and ability
// exclusivity (never both Trap and Push at once -- a human has one set of mouse buttons).
func TestAIIntentsAreHumanReachable(t *testing.T) {
	m, ais := aiMatch(4, 3, control.SkillHard, nil)
	for tick := 0; tick < 900; tick++ {
		inputs := make(map[int]sim.Intent, len(ais))
		for id, ai := range ais {
			in := ai.Intent(m.View())
			if !finite(in.Throttle) || in.Throttle < 0 || in.Throttle > 1 {
				t.Fatalf("tick %d player %d: Throttle %v out of [0,1]", tick, id, in.Throttle)
			}
			if !finiteVec(in.Move) || !finiteVec(in.Aim) {
				t.Fatalf("tick %d player %d: non-finite Move %v or Aim %v", tick, id, in.Move, in.Aim)
			}
			if in.Trap && in.Push {
				t.Fatalf("tick %d player %d: Trap and Push both set -- not human-reachable", tick, id)
			}
			inputs[id] = in
		}
		m.Step(inputs, 1.0/60.0)
	}
}

func finite(f float64) bool { return !math.IsNaN(f) && !math.IsInf(f, 0) }

func finiteVec(v geom.Vec) bool { return finite(v.X) && finite(v.Y) }
