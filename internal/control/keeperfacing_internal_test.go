package control

import (
	"testing"

	"phootball/internal/config"
	"phootball/internal/geom"
	"phootball/internal/sim"
)

// TestKeeperFacingRateLimited verifies the keeper, like every other AI player, cannot snap-turn
// while it is away from the ball: capAim now rate-limits its re-orientation (it used to be exempt
// and whipped round instantly). A ball placed directly behind the keeper is a 180-degree reversal
// it must NOT complete in one decision -- the emitted aim is at most maxTurnRad off its facing.
func TestKeeperFacingRateLimited(t *testing.T) {
	m := sim.BuildMatchFromConfig(sim.NewStandardField(), 3, config.Default())

	var gk *sim.Player
	for _, p := range m.Players {
		if p.Role == sim.RoleGoalkeeper {
			gk = p
			break
		}
	}
	if gk == nil {
		t.Fatal("no goalkeeper in the match")
	}
	gk.Facing = geom.NewVec(1, 0)
	// Ball far behind the keeper's facing, so the keeper wants to spin around to face it.
	m.Ball.Position = gk.Position.Add(geom.NewVec(-300, 0))

	ai := NewAISkill(gk.PlayerID, SkillImpossible) // perfect tier: re-decides every tick, no aim noise
	in := ai.Intent(m.View())
	if in.Aim == (geom.Vec{}) {
		t.Fatal("keeper produced no aim")
	}

	turned := geom.AngleBetween(gk.Facing, in.Aim.Sub(gk.Position))
	if turned > ai.tune.maxTurnRad+1e-6 {
		t.Errorf("keeper re-oriented %.4f rad in one decision (cap %.4f) -- it must not snap-turn", turned, ai.tune.maxTurnRad)
	}
	if turned <= 1e-6 {
		t.Errorf("keeper did not turn toward the ball behind it (turned %.4f) -- test no longer exercises the cap", turned)
	}
}
