package control

import (
	"testing"

	"phootball/internal/config"
	"phootball/internal/geom"
	"phootball/internal/sim"
)

// TestPassPowerSoft checks passes are calibrated to arrive controllably (much softer than a
// distance-blind blast), so the receiver can take them -- per "some passes can be much softer".
func TestPassPowerSoft(t *testing.T) {
	field := sim.NewStandardField()
	m := sim.BuildMatchFromConfig(field, 3, config.Default())
	me := m.Players[1]
	me.Position = geom.NewVec(200, 340)
	ai := NewAISkill(me.PlayerID, SkillHard)
	p := perceive(m.View(), viewMe(m, me), 1.0/60)

	for _, dist := range []float64{120, 250, 380} {
		target := me.Position.Add(geom.NewVec(dist, 0))
		v0 := ai.passSpeedFor(p, target)
		arrive := v0 - (-p.friction)*dist // speed when it reaches the receiver
		if arrive > 255 {
			t.Errorf("dist %.0f: pass arrives at %.0f (too hard to control)", dist, arrive)
		}
		// Softer than the old distance-blind power (dc = clamp(dist/fullRange,.15,.8)).
		oldDC := clampFloat(dist/ai.tune.fullRange, 0.15, 0.8)
		oldPower := me.Tuning.Shoot.Front * (me.Tuning.MinShootFactor + (1-me.Tuning.MinShootFactor)*oldDC)
		if v0 >= oldPower {
			t.Errorf("dist %.0f: calibrated pass speed %.0f not softer than old %.0f", dist, v0, oldPower)
		}
	}
}

// TestPassNotToNothing checks the receiver-reachability gate: bestPass must not play a ball
// to a teammate who can't get to the target in time (a "pass to nothing"), but DOES play it
// when the same teammate can reach it.
func TestPassNotToNothing(t *testing.T) {
	mk := func(mateY float64) (*AI, perception, sim.ObservedView) {
		field := sim.NewStandardField()
		m := sim.BuildMatchFromConfig(field, 4, config.Default())
		me := m.Players[1]
		me.Position = geom.NewVec(440, 240)
		m.Ball.Position = me.Position
		m.Ball.Velocity = geom.Vec{}
		// Park everyone else far away, then place a single candidate mate.
		for _, q := range m.Players {
			if q != me {
				q.Position = geom.NewVec(60, 460)
				q.Velocity = geom.Vec{}
			}
		}
		mate := m.Players[2]
		mate.Position = geom.NewVec(560, mateY)
		mate.Velocity = geom.Vec{}
		ai := NewAISkill(me.PlayerID, SkillImpossible)
		return ai, perceive(m.View(), viewMe(m, me), 1.0/60), viewMe(m, mate)
	}

	// A reachable, open mate ahead: a pass should be found, aimed near the mate.
	ai, p, mate := mk(240)
	target, recv, score := ai.bestPass(p)
	if recv == nil || score <= 0 {
		t.Fatalf("no pass found to a reachable open mate (score %.2f)", score)
	}
	if geom.Dist(target, mate.Position()) > ai.tune.throughDist+1 {
		t.Errorf("pass target %.0f,%.0f is nowhere near the mate %.0f,%.0f", target.X, target.Y, mate.Position().X, mate.Position().Y)
	}
}

// TestLeadPointHonoursHiddenVelocity checks the AI<=human boundary in the pass target: a
// team-mate's velocity is HIDDEN state (not rendered), so the controller cannot lead a pass
// off it. With the default no-lead policy (leadGain 0) the pass aims at where the mate IS,
// regardless of how fast it is actually moving -- which is also what measured best (a human
// passes to a positioned receiver, and most receivers are settled, not sprinting).
func TestLeadPointHonoursHiddenVelocity(t *testing.T) {
	field := sim.NewStandardField()
	m := sim.BuildMatchFromConfig(field, 3, config.Default())
	me := m.Players[1]
	me.Position = geom.NewVec(200, 240)
	m.Ball.Position = me.Position
	mate := m.Players[2]
	mate.Position = geom.NewVec(520, 240)
	mate.Velocity = geom.NewVec(0, 100) // fast hidden velocity -- the AI must NOT read it
	ai := NewAISkill(me.PlayerID, SkillHard)
	p := perceive(m.View(), viewMe(m, me), 1.0/60)

	lead := ai.leadPoint(p, viewMe(m, mate))
	if absFloat(lead.X-mate.Position.X) > 0.01 || absFloat(lead.Y-mate.Position.Y) > 0.01 {
		t.Errorf("lead %v should aim at the mate's current position %v (velocity is hidden)", lead, mate.Position)
	}
	if flight := ai.passFlightTime(p, mate.Position); flight <= 0 {
		t.Errorf("flight time should be positive, got %.2f", flight)
	}
}

// TestKeeperOffLine checks the keeper never hugs its goal line / backs into the net: its
// guard spot always sits at least keeperDepthMin off the line, for any ball position.
func TestKeeperOffLine(t *testing.T) {
	field := sim.NewStandardField()
	m := sim.BuildMatchFromConfig(field, 3, config.Default())
	keeper := m.Players[0] // left team keeper
	if keeper.Role != sim.RoleKeeper {
		t.Fatalf("player 0 is not the keeper")
	}
	ai := NewAISkill(keeper.PlayerID, SkillHard)
	for _, bx := range []float64{120, 300, 500, 800} {
		for _, by := range []float64{120, 250, 340, 430, 560} {
			m.Ball.Position = geom.NewVec(bx, by)
			p := perceive(m.View(), viewMe(m, keeper), 1.0/60)
			spot := ai.keeperGuardSpot(p)
			off := (spot.X - p.ownGoal.X) * p.attackX // distance off its own goal line
			if off < ai.tune.keeperDepthMin-1 {
				t.Errorf("ball(%.0f,%.0f): keeper only %.1f off its line (min %.0f) -- too close to net",
					bx, by, off, ai.tune.keeperDepthMin)
			}
		}
	}
}
