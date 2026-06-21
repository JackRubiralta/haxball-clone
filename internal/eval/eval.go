// Package eval is the reusable, controller-agnostic match roll-out harness. It is the
// exported, non-test counterpart of the helpers that previously lived only in
// control/ai_test.go (aiMatch/stepAll/run/gather), so cmd/datagen, cmd/env, the evaluation
// suite, and the neural tests can all drive matches of arbitrary control.Controllers the same
// way. It imports only sim, control, and config (no neural), so it stays lightweight; callers
// that want neural controllers pass a factory to BuildWith.
package eval

import (
	"phootball/internal/config"
	"phootball/internal/control"
	"phootball/internal/sim"
)

// DT is the fixed simulation timestep used by the harness (60 Hz), matching the rest of the game.
const DT = 1.0 / 60.0

// Match bundles a sim.Match with the per-player controllers driving it.
type Match struct {
	M           *sim.Match
	Controllers map[int]control.Controller
}

func buildMatch(teamSize int, seed int64, mutate func(*config.Config)) (*sim.Match, config.Config) {
	cfg := config.Default()
	cfg.Seed = seed
	if mutate != nil {
		mutate(&cfg)
	}
	field := sim.NewFieldFromGeometry(cfg.Geometry)
	return sim.BuildMatchFromConfig(field, teamSize, cfg), cfg
}

// BuildWith builds a team-size match whose controllers come from factory(playerID, side). This
// is the general builder: datagen passes a featurizer/teacher factory, env passes a neural
// factory, tests pass whatever they need.
func BuildWith(teamSize int, seed int64, mutate func(*config.Config), factory func(id int, side sim.Side) control.Controller) Match {
	m, _ := buildMatch(teamSize, seed, mutate)
	ctrls := make(map[int]control.Controller, len(m.Players))
	for _, p := range m.Players {
		ctrls[p.PlayerID] = factory(p.PlayerID, p.Team.Side)
	}
	return Match{M: m, Controllers: ctrls}
}

// BuildSizedWith is BuildWith with independent per-side roster sizes, for asymmetric drill
// scenarios (e.g. a 3v1 rondo). The controllers come from factory(playerID, side); positions are
// the standard formation until the caller repositions them for a scenario.
func BuildSizedWith(homeSize, awaySize int, seed int64, mutate func(*config.Config), factory func(id int, side sim.Side) control.Controller) Match {
	cfg := config.Default()
	cfg.Seed = seed
	if mutate != nil {
		mutate(&cfg)
	}
	field := sim.NewFieldFromGeometry(cfg.Geometry)
	m := sim.BuildMatchFromConfigSized(field, homeSize, awaySize, cfg)
	ctrls := make(map[int]control.Controller, len(m.Players))
	for _, p := range m.Players {
		ctrls[p.PlayerID] = factory(p.PlayerID, p.Team.Side)
	}
	return Match{M: m, Controllers: ctrls}
}

// BuildAIMatch builds a match with every player an AI at the given skill.
func BuildAIMatch(teamSize int, seed int64, skill control.Skill, mutate func(*config.Config)) Match {
	return BuildWith(teamSize, seed, mutate, func(id int, _ sim.Side) control.Controller {
		return control.NewAISkill(id, skill)
	})
}

// BuildTeamMatch builds a match where the two sides run different AI skill tiers (head-to-head).
func BuildTeamMatch(teamSize int, seed int64, left, right control.Skill, mutate func(*config.Config)) Match {
	return BuildWith(teamSize, seed, mutate, func(id int, side sim.Side) control.Controller {
		if side == sim.SideRight {
			return control.NewAISkill(id, right)
		}
		return control.NewAISkill(id, left)
	})
}

// Step advances the match one tick using every controller's Intent.
func (e Match) Step() {
	in := make(map[int]sim.Intent, len(e.Controllers))
	for id, c := range e.Controllers {
		in[id] = c.Intent(e.M.View())
	}
	e.M.Step(in, DT)
}

// Run advances ticks ticks, invoking onTick(tick) after each step (onTick may be nil).
func (e Match) Run(ticks int, onTick func(tick int)) {
	for i := 0; i < ticks; i++ {
		e.Step()
		if onTick != nil {
			onTick(i)
		}
	}
}

// Stats holds emergent, recorder-free metrics derived from the carrier sequence and score.
type Stats struct {
	GoalsL, GoalsR int
	Passes         int // completed same-team carrier-to-carrier handovers
	Turnovers      int // possession changes between teams
}

// Gather runs the match for ticks ticks and measures passing/turnovers/goals from the
// BallCarrier() sequence (the same derivation control/ai_test.go's gather used). It needs no
// recorder, so it never perturbs determinism.
func (e Match) Gather(ticks int) Stats {
	var st Stats
	lastCarrier, lastSide := -1, sim.SideNone
	e.Run(ticks, func(int) {
		c := e.M.BallCarrier()
		if c == nil {
			return
		}
		if c.PlayerID != lastCarrier {
			if lastSide != sim.SideNone {
				if c.Team.Side == lastSide {
					st.Passes++
				} else {
					st.Turnovers++
				}
			}
			lastCarrier, lastSide = c.PlayerID, c.Team.Side
		}
	})
	st.GoalsL, st.GoalsR = e.M.Teams[0].Score, e.M.Teams[1].Score
	return st
}
