package sim

import (
	"math"
	"testing"

	"phootball/internal/config"
	"phootball/internal/geom"
)

// soloAtBall builds a one-player match with the ball glued to that player's front, deep enough
// inside TouchRange that the held ball never drifts out of reach -- so the shoot charge (which
// only builds while touching the ball) can accumulate over the test. The returned aim point keeps
// the player facing +X, where the ball sits.
func soloAtBall(t *testing.T) (*Match, *Player, geom.Vec) {
	t.Helper()
	m := BuildSolo(NewFieldFromGeometry(config.Default().Geometry))
	p := m.Players[0]
	p.Position = m.Field.CenterSpot
	p.Facing = geom.NewVec(1, 0)
	m.Ball.Position = geom.NewVec(p.Position.X+p.Radius()+m.Ball.Radius()+0.5, p.Position.Y)
	aim := geom.NewVec(p.Position.X+1000, p.Position.Y) // a point due +X, so facing stays toward the ball
	return m, p, aim
}

// TestShootChargeOnlyBuildsWhileTouching pins two rules together:
//
//  1. The wind-up only happens ON THE BALL: holding shoot away from the ball builds NO charge.
//  2. With the ball at the feet, the charge grows by exactly deltaTime per held tick, capped at
//     shootChargeMax -- the AI<=human "no shortcut to full charge" boundary. The only way to charge
//     is to hold ShootHeld over real ticks on the ball, so neither a human nor the AI can reach full
//     power faster than k*dt.
func TestShootChargeOnlyBuildsWhileTouching(t *testing.T) {
	dt := 1.0 / 60.0

	// (1) Away from the ball: no charge ever builds.
	{
		m := BuildSolo(NewFieldFromGeometry(config.Default().Geometry))
		p := m.Players[0]
		p.Position = m.Field.CenterSpot
		m.Ball.Position = geom.NewVec(m.Field.Max.X-40, m.Field.Max.Y-40) // far from the player
		aim := geom.NewVec(p.Position.X+1000, p.Position.Y)
		in := map[int]Intent{p.PlayerID: {ShootHeld: true, Aim: aim}}
		for k := 1; k <= 40; k++ {
			m.Step(in, dt)
		}
		if p.shootCharge != 0 {
			t.Fatalf("holding shoot away from the ball must not build a charge, got %.6f", p.shootCharge)
		}
	}

	// (2) On the ball: charge accumulates by exactly dt per tick, firmly capped.
	{
		m, p, aim := soloAtBall(t)
		in := map[int]Intent{p.PlayerID: {ShootHeld: true, Aim: aim}}
		for k := 1; k <= 80; k++ {
			m.Step(in, dt)
			want := math.Min(float64(k)*dt, shootChargeMax)
			if d := math.Abs(p.shootCharge - want); d > 1e-9 {
				t.Fatalf("on the ball, after %d ticks: shootCharge=%.6f, want min(k*dt, %.2f)=%.6f", k, p.shootCharge, shootChargeMax, want)
			}
		}
		if p.shootCharge != shootChargeMax {
			t.Errorf("held charge = %.6f, want cap %.2f", p.shootCharge, shootChargeMax)
		}
	}
}

// TestCancelChargeEquivalentForHumanAndAI: dropping a charge via CancelCharge (the signal a human
// raises on a right-click and the AI raises to abort/override a charge) clears the charge and
// suppresses the release kick -- identically regardless of who set it. This pins B.3: the AI's use
// of CancelCharge is the same human-reachable action.
func TestCancelChargeEquivalentForHumanAndAI(t *testing.T) {
	dt := 1.0 / 60.0
	m, p, aim := soloAtBall(t)

	// Charge for a while on the ball.
	for i := 0; i < 20; i++ {
		m.Step(map[int]Intent{p.PlayerID: {ShootHeld: true, Aim: aim}}, dt)
	}
	if p.shootCharge <= 0 {
		t.Fatal("precondition: a charge should have built on the ball")
	}
	// Cancel while still holding shoot (a trap/push takeover, or a right-click): charge drops.
	m.Step(map[int]Intent{p.PlayerID: {ShootHeld: true, CancelCharge: true, Aim: aim}}, dt)
	if p.shootCharge != 0 {
		t.Errorf("CancelCharge should drop the charge, got %.4f", p.shootCharge)
	}
	// Releasing now must NOT fire: the cancel latched. The ball is at the feet (so the dribble
	// nudges it gently), so we check it is not LAUNCHED rather than perfectly still -- a fired shot
	// would send it off at shot speed (>= ~200), far above any dribble nudge.
	m.Step(map[int]Intent{p.PlayerID: {ShootHeld: false, Aim: aim}}, dt)
	if v := geom.Norm(m.Ball.Velocity); v > 50 {
		t.Errorf("a cancelled charge must not fire on release; ball launched at speed %.1f", v)
	}
}
