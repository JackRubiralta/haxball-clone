package control

import (
	"testing"

	"phootball/internal/config"
	"phootball/internal/geom"
	"phootball/internal/sim"
)

// TestTurnCapMeasure reports the largest per-tick facing change any AI makes in a real match, vs the
// HUMAN limit (TurnRate*dt). An AI turning faster than that is "cheating" (turning instantly). Reports
// the worst offenders and how often the human limit is exceeded. Measurement only.
func TestTurnCapMeasure(t *testing.T) {
	cfg := config.Default()
	cfg.Geometry = config.LargeGeometry()
	humanPerTick := cfg.Tuning.Player.TurnRate * diagDt // radians a human may turn in one tick
	m, ais := sweepMatch(6, 7, SkillHard, func(c *config.Config) { c.Geometry = config.LargeGeometry() }, nil)
	prevFace := map[int]geom.Vec{}
	prevPos := map[int]geom.Vec{}
	prevBall := m.Ball.Position
	prevGoals := m.Teams[0].Score + m.Teams[1].Score
	resetWindow := 10 // skip the first few ticks (initial orientation)
	var maxDelta float64
	exceed, total := 0, 0
	worstNonCtrl := 0.0
	for i := 0; i < diagTicks; i++ {
		in := make(map[int]sim.Intent, len(ais))
		for id, ai := range ais {
			in[id] = ai.Intent(m.View())
		}
		m.Step(in, diagDt)
		// Skip kickoff/goal-reset windows: a GOAL teleports the ball to centre and the sim resets every
		// player's facing toward centre (kickoff.go) over the following ticks -- not AI-controlled turns
		// (they flip many players at once). Detect a goal (score change) or a ball teleport and skip a
		// generous settle window after it.
		if g := m.Teams[0].Score + m.Teams[1].Score; g != prevGoals {
			resetWindow = 60
			prevGoals = g
		}
		if geom.Dist(prevBall, m.Ball.Position) > 100 {
			resetWindow = 60
		}
		prevBall = m.Ball.Position
		kickoff := resetWindow > 0
		if resetWindow > 0 {
			resetWindow--
		}
		for _, pl := range m.Players {
			f := pl.Facing
			pf, okF := prevFace[pl.PlayerID]
			pp, okP := prevPos[pl.PlayerID]
			teleported := kickoff || (okP && geom.Dist(pp, pl.Position) > 30)
			if okF && !teleported && pf != (geom.Vec{}) && f != (geom.Vec{}) {
				d := geom.AngleBetween(pf, f)
				total++
				if d > maxDelta {
					maxDelta = d
				}
				if d > humanPerTick+1e-6 {
					exceed++
					if d > worstNonCtrl {
						worstNonCtrl = d
					}
				}
			}
			prevFace[pl.PlayerID] = f
			prevPos[pl.PlayerID] = pl.Position
		}
	}
	t.Logf("human per-tick turn limit = %.4f rad (TurnRate %.0f * dt %.4f)", humanPerTick, cfg.Tuning.Player.TurnRate, diagDt)
	t.Logf("AI maxTurnRad (per decision) = %.4f; worst single-tick AI facing delta = %.4f rad", defaultAITuning().maxTurnRad, maxDelta)
	t.Logf("ticks exceeding the human limit: %d / %d (%.2f%%); worst exceedance = %.4f rad",
		exceed, total, 100*float64(exceed)/float64(total), worstNonCtrl)
	// The AI must never out-turn a human: every per-tick facing change (outside kickoff resets) must be
	// within the AI's own turn cap, and that cap must itself be <= a human's per-tick turn.
	if maxDelta > defaultAITuning().maxTurnRad*1.02 {
		t.Errorf("AI turned %.4f rad in one tick > maxTurnRad %.4f -- an uncapped instant-turn path", maxDelta, defaultAITuning().maxTurnRad)
	}
	if defaultAITuning().maxTurnRad > humanPerTick*1.01 {
		t.Errorf("maxTurnRad %.4f exceeds a human's per-tick turn %.4f -- the AI can out-turn a human", defaultAITuning().maxTurnRad, humanPerTick)
	}
	if exceed > 0 {
		t.Errorf("%d AI facing changes exceeded the human per-tick turn limit (instant-turn cheat)", exceed)
	}
}
