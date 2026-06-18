package physics

import (
	"math"
	"testing"

	"phootball/internal/geom"
)

func approx(a, b float64) bool { return math.Abs(a-b) < 1e-6 }

// TestUpdateFrictionDecaysVelocity checks the integration order (accel -> cap -> friction ->
// position): with no acceleration a body's velocity decays by v*friction*dt each step, then
// position advances by the post-friction velocity.
func TestUpdateFrictionDecaysVelocity(t *testing.T) {
	b := NewCircleBody(geom.NewVec(0, 0), 5, -0.3, 1.5)
	b.Velocity = geom.NewVec(100, 0)
	dt := 1.0 / 60.0
	b.Update(dt)
	wantV := 100 + 100*(-0.3)*dt
	if !approx(b.Velocity.X, wantV) {
		t.Errorf("velocity after friction = %.6f, want %.6f", b.Velocity.X, wantV)
	}
	if !approx(b.Position.X, wantV*dt) {
		t.Errorf("position after step = %.6f, want %.6f", b.Position.X, wantV*dt)
	}
}

// TestSoftSpeedCapDoesNotSnapKnock checks the soft cap: acceleration may not push a body past
// MaxSpeed, but a knock that already exceeds MaxSpeed is NOT snapped down -- it is left for
// friction to bleed off.
func TestSoftSpeedCapDoesNotSnapKnock(t *testing.T) {
	b := NewCircleBody(geom.NewVec(0, 0), 5, 0, 1) // no friction, so we isolate the cap
	b.MaxSpeed = 100
	b.Velocity = geom.NewVec(200, 0) // a knock above the cap
	b.Update(1.0 / 60.0)
	if !approx(geom.Norm(b.Velocity), 200) {
		t.Errorf("a knock above MaxSpeed was snapped to %.2f, want kept at 200", geom.Norm(b.Velocity))
	}
}

// TestSoftCapLimitsAcceleration: acceleration cannot drive the body past MaxSpeed.
func TestSoftCapLimitsAcceleration(t *testing.T) {
	b := NewCircleBody(geom.NewVec(0, 0), 5, 0, 1)
	b.MaxSpeed = 100
	b.Acceleration = geom.NewVec(100000, 0) // huge accel for one step
	b.Update(1.0 / 60.0)
	if geom.Norm(b.Velocity) > 100+1e-6 {
		t.Errorf("acceleration pushed speed to %.2f past MaxSpeed 100", geom.Norm(b.Velocity))
	}
}

func TestStaticImmovability(t *testing.T) {
	if !NewStaticCircle(geom.NewVec(0, 0), 5).Static() {
		t.Error("NewStaticCircle should be Static")
	}
	wall := NewStaticSegment(geom.NewVec(0, -50), geom.NewVec(0, 50))
	ball := NewCircleBody(geom.NewVec(-3, 0), 5, 0, 1) // overlapping the wall (left of it), moving INTO it
	ball.Velocity = geom.NewVec(100, 0)                // toward the wall (+x)
	before := wall.Position
	if !Collide(ball, wall, 0.9) {
		t.Fatal("expected a contact with the wall")
	}
	if wall.Position != before {
		t.Errorf("static wall moved to %v", wall.Position)
	}
	if ball.Velocity.X >= 0 {
		t.Errorf("ball should have bounced back off the wall (vx now %.2f, want negative)", ball.Velocity.X)
	}
}

func TestCollideReportsContact(t *testing.T) {
	a := NewCircleBody(geom.NewVec(0, 0), 5, 0, 1)
	b := NewCircleBody(geom.NewVec(8, 0), 5, 0, 1) // overlapping (gap 8 < 10)
	if !Collide(a, b, 0) {
		t.Error("overlapping circles should report contact")
	}
	far := NewCircleBody(geom.NewVec(100, 0), 5, 0, 1)
	if Collide(a, far, 0) {
		t.Error("distant circles should NOT report contact")
	}
	// Coincident centres have no sensible normal -> no contact (use fresh bodies: a/b above
	// were displaced by their own Collide).
	c1 := NewCircleBody(geom.NewVec(0, 0), 5, 0, 1)
	c2 := NewCircleBody(geom.NewVec(0, 0), 5, 0, 1)
	if Collide(c1, c2, 0) {
		t.Error("coincident circles should report no contact (no normal)")
	}
}

func TestCollideEqualMassSplitsOverlap(t *testing.T) {
	a := NewCircleBody(geom.NewVec(0, 0), 5, 0, 1)
	b := NewCircleBody(geom.NewVec(8, 0), 5, 0, 1) // overlap 2
	Collide(a, b, 0)
	// Equal masses split the 2-unit overlap evenly: each moves 1 unit apart.
	if !approx(a.Position.X, -1) || !approx(b.Position.X, 9) {
		t.Errorf("equal-mass separation = %v / %v, want -1 / 9", a.Position, b.Position)
	}
}

func TestReflectInsideAndClampInside(t *testing.T) {
	// ReflectInside flips the into-wall velocity; ClampInside zeroes it. Both clamp position.
	b := NewCircleBody(geom.NewVec(-2, 0), 5, 0, 1)
	b.Velocity = geom.NewVec(-30, 0)
	ReflectInside(b, 0, -100, 100, 100)
	if !approx(b.Position.X, 5) || !approx(b.Velocity.X, 30) {
		t.Errorf("ReflectInside = pos %.1f vel %.1f, want pos 5 vel +30", b.Position.X, b.Velocity.X)
	}
	c := NewCircleBody(geom.NewVec(-2, 0), 5, 0, 1)
	c.Velocity = geom.NewVec(-30, 0)
	ClampInside(c, 0, -100, 100, 100)
	if !approx(c.Position.X, 5) || c.Velocity.X != 0 {
		t.Errorf("ClampInside = pos %.1f vel %.1f, want pos 5 vel 0", c.Position.X, c.Velocity.X)
	}
}

func TestMassReciprocal(t *testing.T) {
	b := NewCircleBody(geom.NewVec(0, 0), 5, 0, 2)
	if !approx(b.Mass(), 2) {
		t.Errorf("Mass = %.3f, want 2", b.Mass())
	}
	if NewStaticCircle(geom.NewVec(0, 0), 5).Mass() != math.MaxFloat64 {
		t.Error("static body should report ~infinite mass")
	}
}
