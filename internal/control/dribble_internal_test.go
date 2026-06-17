package control

import (
	"testing"

	"phootball/internal/config"
	"phootball/internal/geom"
	"phootball/internal/sim"
)

// TestDribbleTurnRateLimited verifies the anti-fling rule: the dribbler's facing can never
// snap toward a new heading in one tick (the ball can't follow a facing that whips around),
// so a request to turn a half-turn (pi rad) is rate-limited to at most maxTurnRad.
func TestDribbleTurnRateLimited(t *testing.T) {
	field := sim.NewStandardField()
	m := sim.BuildMatchFromConfig(field, 3, config.Default())
	me := m.Players[1] // an outfielder
	me.Facing = geom.NewVec(1, 0)
	m.Ball.Position = me.Position.Add(geom.NewVec(me.Radius()+m.Ball.Radius(), 0)) // ball at the front

	ai := NewAISkill(me.PlayerID, SkillHard)
	p := perceive(m.View(), viewMe(m, me), 1.0/60)
	in := ai.dribble(p, sim.Intent{}, me.Position.Add(geom.NewVec(-100, 0))) // demand a full reversal

	turned := geom.AngleBetween(me.Facing, in.Aim.Sub(me.Position))
	if turned > ai.tune.maxTurnRad+0.009 { // small radian slack (~0.5deg)
		t.Errorf("facing turned %.3f rad in one tick (max %.3f) -- would fling the ball", turned, ai.tune.maxTurnRad)
	}
	if !in.Trap {
		t.Errorf("a hard direction change should engage trap to keep the ball glued")
	}
}

// TestFaceBallRecovery verifies that when the ball has drifted to the player's back, the AI
// turns to FACE the ball (to scoop it to the front) rather than continuing to face forward.
func TestFaceBallRecovery(t *testing.T) {
	field := sim.NewStandardField()
	m := sim.BuildMatchFromConfig(field, 3, config.Default())
	me := m.Players[1]
	me.Facing = geom.NewVec(1, 0)
	// Ball directly behind the player (off the front arc).
	m.Ball.Position = me.Position.Add(geom.NewVec(-(me.Radius() + m.Ball.Radius()), 0))

	ai := NewAISkill(me.PlayerID, SkillHard)
	p := perceive(m.View(), viewMe(m, me), 1.0/60)
	in := ai.dribble(p, sim.Intent{}, me.Position.Add(geom.NewVec(100, 0))) // want to go forward

	// The new facing should have rotated AWAY from straight ahead, toward the ball behind.
	toBall := m.Ball.Position.Sub(me.Position)
	if geom.AngleBetween(in.Aim.Sub(me.Position), toBall) >= geom.AngleBetween(me.Facing, toBall) {
		t.Errorf("with the ball behind, the AI did not turn toward it to recover it")
	}
}
