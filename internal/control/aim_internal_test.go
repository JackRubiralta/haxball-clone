package control

import (
	"math"
	"testing"

	"phootball/internal/config"
	"phootball/internal/geom"
	"phootball/internal/sim"
)

// TestShotAlignedUsesBallToTarget pins the radial-aim fix: alignment must be judged from the
// BALL to the target (the ball launches from its own position), not from the player centre.
// A ball offset to the side toward a close target is NOT aligned even though the old
// player->target check would have (falsely) released.
func TestShotAlignedUsesBallToTarget(t *testing.T) {
	// A committed shot faces the target (aimKeepingBall drives the facing there), so set the
	// facing toward the target -- the aim assist then blends the launch toward it.
	mk := func(ball, target geom.Vec) (*AI, perception) {
		field := sim.NewStandardField()
		m := sim.BuildMatchFromConfig(field, 3, config.Default())
		me := m.Players[1]
		me.Position = geom.NewVec(0, 0)
		me.Facing = geom.Unit(target.Sub(me.Position))
		m.Ball.Position = ball
		m.Ball.Velocity = geom.NewVec(0, 0) // still ball: launch dir == radial
		ai := NewAISkill(me.PlayerID, SkillHard)
		return ai, perceive(m.View(), viewMe(m, me), 1.0/60)
	}

	// Ball mostly in front but offset; a CLOSE target up and to the side. The real shot would
	// fly off the target (~36deg from the ball) -- even with the facing-blended assist the
	// launch still misses by more than the tolerance -> not aligned.
	ai, p := mk(geom.NewVec(26, 5), geom.NewVec(60, 30))
	if ai.launchAligned(p, geom.NewVec(60, 30), 0.3, ai.tune.shootAlignRad) {
		t.Errorf("shot reported aligned, but the radial direction misses the close target")
	}

	// Properly behind the ball, ball on the me->target line (and facing the target): aligned.
	tgt := geom.NewVec(300, 100)
	onLine := tgt.Scale(26.5 / geom.Norm(tgt))
	ai2, p2 := mk(onLine, tgt)
	if !ai2.launchAligned(p2, tgt, 0.3, ai2.tune.shootAlignRad) {
		t.Errorf("shot reported not aligned when the ball is exactly on the player->target line")
	}
}

// TestAimAccuracy drives the real charge/aim pipeline and checks the ball is actually
// launched toward the target (within a tight angle), validating end-to-end shot/pass aim.
func TestAimAccuracy(t *testing.T) {
	worst := 0.0
	for _, target := range []geom.Vec{
		geom.NewVec(700, 360), // long, straight
		geom.NewVec(520, 200), // medium, angled
		geom.NewVec(430, 460), // short, sharply angled (the case passes get wrong)
	} {
		field := sim.NewStandardField()
		m := sim.BuildMatchFromConfig(field, 3, config.Default())
		me := m.Players[1]
		for _, q := range m.Players {
			if q != me {
				q.Position = geom.NewVec(q.Position.X, 40) // clear the area
			}
		}
		me.Position = geom.NewVec(400, 340)
		me.Facing = geom.NewVec(1, 0)
		m.Ball.Position = me.Position.Add(geom.NewVec(me.Radius()+m.Ball.Radius(), 0))
		ai := NewAISkill(me.PlayerID, SkillImpossible)

		launched := false
		for i := 0; i < 200 && !launched; i++ {
			prevVel := m.Ball.Velocity
			in := ai.shootAt(*newPerception(m, me), sim.Intent{}, target, 0.6, ai.tune.shootAlignRad)
			m.Step(map[int]sim.Intent{me.PlayerID: in}, 1.0/60)
			// A launch shows up as a big jump in ball speed.
			if geom.Norm(m.Ball.Velocity)-geom.Norm(prevVel) > 150 {
				launchDir := m.Ball.Velocity
				wantDir := target.Sub(m.Ball.Position)
				errRad := geom.AngleBetween(launchDir, wantDir)
				if errRad > worst {
					worst = errRad
				}
				launched = true
				if errRad > 0.244 { // ~14deg
					t.Errorf("target %v: ball launched %.3f rad off target", target, errRad)
				}
			}
		}
		if !launched {
			t.Errorf("target %v: no shot was launched", target)
		}
	}
	t.Logf("worst launch error: %.3f rad", worst)
}

// newPerception builds a perception for the internal test (mirrors perceive's dt handling).
func newPerception(m *sim.Match, me *sim.Player) *perception {
	p := perceive(m.View(), viewMe(m, me), 1.0/60)
	return &p
}

// viewMe returns the read-only PlayerView for a concrete player, for tests that set up
// the match with concrete types but feed perceive through the game-provided View.
func viewMe(m *sim.Match, me *sim.Player) sim.PlayerView {
	v, _ := m.View().Me(me.PlayerID)
	return v
}

var _ = math.Pi
