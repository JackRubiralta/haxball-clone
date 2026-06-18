package sim

import (
	"math"
	"testing"

	"phootball/internal/config"
	"phootball/internal/geom"
)

// TestShootChargeAccumulatesByDeltaTime pins the AI<=human charge limit: shootCharge grows by
// exactly deltaTime per tick the shoot button is held, capped at shootChargeMax. Because the
// only way to charge is to assert Intent.ShootHeld over real ticks, neither a human nor the AI
// can reach full charge faster than k*dt -- there is no shortcut. The boundary is structural:
// this test guards against a regression that would let a charge be set directly.
func TestShootChargeAccumulatesByDeltaTime(t *testing.T) {
	field := NewFieldFromGeometry(config.Default().Geometry)
	m := BuildSolo(field)
	p := m.Players[0]
	// Park the player far from the ball so the dribble/shoot interaction never fires or clears
	// the charge -- we are measuring pure accumulation.
	p.Position = geom.NewVec(field.Min.X+40, field.Min.Y+40)
	m.Ball.Position = geom.NewVec(field.Max.X-40, field.Max.Y-40)

	dt := 1.0 / 60.0
	in := map[int]Intent{p.PlayerID: {ShootHeld: true, Aim: geom.NewVec(1, 0)}}
	for k := 1; k <= 80; k++ {
		m.Step(in, dt)
		want := math.Min(float64(k)*dt, shootChargeMax)
		if d := math.Abs(p.shootCharge - want); d > 1e-9 {
			t.Fatalf("after %d ticks: shootCharge=%.6f, want min(k*dt, %.2f)=%.6f", k, p.shootCharge, shootChargeMax, want)
		}
	}
	// It is firmly capped, never exceeding the max no matter how long it is held.
	if p.shootCharge != shootChargeMax {
		t.Errorf("held charge = %.6f, want cap %.2f", p.shootCharge, shootChargeMax)
	}
}

// TestCancelChargeEquivalentForHumanAndAI: dropping a charge via CancelCharge (the signal a
// human raises on a right-click and the AI raises to abort/override a charge) clears the charge
// and suppresses the release kick -- identically regardless of who set it. This pins B.3: the
// AI's use of CancelCharge is the same human-reachable action.
func TestCancelChargeEquivalentForHumanAndAI(t *testing.T) {
	field := NewFieldFromGeometry(config.Default().Geometry)
	m := BuildSolo(field)
	p := m.Players[0]
	p.Position = geom.NewVec(field.Min.X+40, field.Min.Y+40)
	m.Ball.Position = geom.NewVec(field.Max.X-40, field.Max.Y-40)
	dt := 1.0 / 60.0

	// Charge for a while.
	for i := 0; i < 20; i++ {
		m.Step(map[int]Intent{p.PlayerID: {ShootHeld: true, Aim: geom.NewVec(1, 0)}}, dt)
	}
	if p.shootCharge <= 0 {
		t.Fatal("precondition: a charge should have built")
	}
	// Cancel while still holding shoot (a trap/push takeover, or a right-click): charge drops.
	m.Step(map[int]Intent{p.PlayerID: {ShootHeld: true, CancelCharge: true, Aim: geom.NewVec(1, 0)}}, dt)
	if p.shootCharge != 0 {
		t.Errorf("CancelCharge should drop the charge, got %.4f", p.shootCharge)
	}
	// Releasing now must NOT fire (the cancel latched), so the ball stays put.
	ballBefore := m.Ball.Position
	m.Step(map[int]Intent{p.PlayerID: {ShootHeld: false}}, dt)
	if geom.Dist(m.Ball.Position, ballBefore) > 1e-9 {
		t.Errorf("a cancelled charge must not fire on release; ball moved from %v to %v", ballBefore, m.Ball.Position)
	}
}
