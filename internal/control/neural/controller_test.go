package neural_test

import (
	"math"
	"testing"

	"phootball/internal/config"
	"phootball/internal/control/neural"
	"phootball/internal/geom"
	"phootball/internal/policy"
	"phootball/internal/sim"
)

func buildNeuralMatch(t *testing.T, teamSize int, seed int64) (*sim.Match, map[int]*neural.Controller) {
	t.Helper()
	net, err := policy.LoadDefault()
	if err != nil {
		t.Fatalf("LoadDefault: %v", err)
	}
	if err := neural.ValidateNet(net); err != nil {
		t.Fatalf("ValidateNet: %v", err)
	}
	cfg := config.Default()
	cfg.Seed = seed
	field := sim.NewFieldFromGeometry(cfg.Geometry)
	m := sim.BuildMatchFromConfig(field, teamSize, cfg)
	ctrls := make(map[int]*neural.Controller, len(m.Players))
	for _, p := range m.Players {
		ctrls[p.PlayerID] = neural.New(p.PlayerID, net)
	}
	return m, ctrls
}

func stepNeural(m *sim.Match, ctrls map[int]*neural.Controller) {
	in := make(map[int]sim.Intent, len(ctrls))
	for id, c := range ctrls {
		in[id] = c.Intent(m.View())
	}
	m.Step(in, 1.0/60.0)
}

func finiteF(f float64) bool    { return !math.IsNaN(f) && !math.IsInf(f, 0) }
func finiteVec(v geom.Vec) bool { return finiteF(v.X) && finiteF(v.Y) }

// TestNeuralIntentsAreHumanReachable mirrors control/boundary_test.go:65 for the neural
// controller: over 900 ticks every emitted Intent has finite Move/Aim, Throttle in [0,1], and
// never both Trap and Push. Holds with random (M0) weights because the boundary is enforced by
// decode/masking/clamp, not the weights.
func TestNeuralIntentsAreHumanReachable(t *testing.T) {
	m, ctrls := buildNeuralMatch(t, 4, 3)
	for tick := 0; tick < 900; tick++ {
		in := make(map[int]sim.Intent, len(ctrls))
		for id, c := range ctrls {
			it := c.Intent(m.View())
			if !finiteF(it.Throttle) || it.Throttle < 0 || it.Throttle > 1 {
				t.Fatalf("tick %d player %d: bad throttle %v", tick, id, it.Throttle)
			}
			if !finiteVec(it.Move) || !finiteVec(it.Aim) {
				t.Fatalf("tick %d player %d: non-finite Move=%v Aim=%v", tick, id, it.Move, it.Aim)
			}
			if it.Trap && it.Push {
				t.Fatalf("tick %d player %d: Trap and Push together", tick, id)
			}
			in[id] = it
		}
		m.Step(in, 1.0/60.0)
	}
}

// TestNeuralNoSnapTurn proves the relative-aim head structurally bounds the per-tick facing
// change to AimArcMax even with random weights: a human/AI cannot snap-turn. A small allowance
// covers kickoff resets (which reposition and re-face players).
func TestNeuralNoSnapTurn(t *testing.T) {
	m, ctrls := buildNeuralMatch(t, 3, 5)
	before := make(map[int]geom.Vec)
	violations, total := 0, 0
	for tick := 0; tick < 600; tick++ {
		for _, p := range m.Players {
			before[p.PlayerID] = p.Facing
		}
		stepNeural(m, ctrls)
		for _, p := range m.Players {
			old := before[p.PlayerID]
			nw := p.Facing
			if old == (geom.Vec{}) || nw == (geom.Vec{}) {
				continue
			}
			total++
			if geom.AngleBetween(old, nw) > neural.AimArcMax+0.02 {
				violations++
			}
		}
	}
	if total == 0 {
		t.Fatal("no facing samples")
	}
	if violations > total/20 {
		t.Fatalf("too many snap-turns: %d / %d (>5%%)", violations, total)
	}
}

// TestNeuralDeterminism: same seed + weights => identical ball trajectory. Guards against
// map-iteration / wall-clock / scratch nondeterminism in the controller and velocity memory.
func TestNeuralDeterminism(t *testing.T) {
	run := func() (geom.Vec, geom.Vec) {
		m, ctrls := buildNeuralMatch(t, 4, 9)
		for i := 0; i < 300; i++ {
			stepNeural(m, ctrls)
		}
		return m.Ball.Position, m.Ball.Velocity
	}
	p1, v1 := run()
	p2, v2 := run()
	if geom.Dist(p1, p2) > 1e-9 || geom.Dist(v1, v2) > 1e-9 {
		t.Fatalf("nondeterministic: pos %v vs %v, vel %v vs %v", p1, p2, v1, v2)
	}
}

// TestNeuralVariousRosters exercises rosters from 1v1 up to 11v11 (the Deep-Sets variable-count
// path) for a few ticks, asserting intents stay human-reachable across sizes.
func TestNeuralVariousRosters(t *testing.T) {
	for _, size := range []int{1, 2, 5, 11} {
		m, ctrls := buildNeuralMatch(t, size, int64(size))
		for tick := 0; tick < 120; tick++ {
			in := make(map[int]sim.Intent, len(ctrls))
			for id, c := range ctrls {
				it := c.Intent(m.View())
				if it.Trap && it.Push {
					t.Fatalf("size %d: trap+push", size)
				}
				if !finiteVec(it.Aim) || !finiteVec(it.Move) {
					t.Fatalf("size %d: non-finite intent", size)
				}
				in[id] = it
			}
			m.Step(in, 1.0/60.0)
		}
	}
}
