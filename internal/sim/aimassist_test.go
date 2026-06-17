package sim

import (
	"math"
	"testing"

	"phootball/internal/geom"
)

// TestShootAimAssist verifies the shot aim-assist: a ball sitting within the front cone
// fires nudged toward where the player is FACING (not straight along the radial), so the
// shot goes where the player aims even when the ball isn't perfectly centred; while a ball
// at the side/back fires purely radially (no facing pull). All angles are in radians.
func TestShootAimAssist(t *testing.T) {
	stats := DefaultStats(500)
	touchDist := stats.Radius + 10 // ball just touching (ball radius 10), inside TouchRange
	const tol = 0.018              // ~1deg tolerance

	// shotAngleOff returns the angle (radians) between the launched ball velocity and the
	// player's facing (+X), with the ball placed `off` radians off the facing direction.
	shotAngleOff := func(off float64) float64 {
		p := NewPlayer(0, geom.NewVec(0, 0), stats, nil)
		p.Facing = geom.NewVec(1, 0)
		ballPos := geom.NewVec(math.Cos(off), math.Sin(off)).Scale(touchDist)
		b := NewBall(ballPos, 10)
		if !shoot(p, b) {
			t.Fatalf("shoot did not connect at off=%.3f rad", off)
		}
		return math.Acos(clamp1(geom.Dot(geom.Unit(b.Velocity), p.Facing)))
	}

	// Inside the front cone (0.244 rad ~14deg, still < CaptureCone 0.262): assist is 100%,
	// so the shot fires essentially straight along the facing direction (~0 off facing).
	if got := shotAngleOff(0.244); got > tol {
		t.Errorf("in-cone shot not snapped to facing: launched at %.3f rad (want ~0)", got)
	}

	// Just past the cone (0.349 rad ~20deg): assist is zero (no soft band), fires radially.
	if got := shotAngleOff(0.349); math.Abs(got-0.349) > tol {
		t.Errorf("past-cone shot should fire radially: launched at %.3f rad (want ~0.349)", got)
	}

	// At the side (pi/2 ~90deg): no assist, the shot fires straight out along the radial.
	if got := shotAngleOff(math.Pi / 2); math.Abs(got-math.Pi/2) > tol {
		t.Errorf("side shot should fire radially: launched at %.3f rad (want ~%.3f)", got, math.Pi/2)
	}

	// Disabling the assist restores the raw radial physics even inside the cone.
	off := stats
	off.ShootAimAssist = 0
	p := NewPlayer(0, geom.NewVec(0, 0), off, nil)
	p.Facing = geom.NewVec(1, 0)
	b := NewBall(geom.NewVec(math.Cos(0.1745), math.Sin(0.1745)).Scale(touchDist), 10) // 0.1745 ~10deg
	if !shoot(p, b) {
		t.Fatal("shoot did not connect with assist disabled")
	}
	if ang := math.Acos(clamp1(geom.Dot(geom.Unit(b.Velocity), p.Facing))); math.Abs(ang-0.1745) > tol {
		t.Errorf("assist disabled should be pure radial: launched at %.3f rad (want ~0.1745)", ang)
	}
}

func clamp1(v float64) float64 {
	if v > 1 {
		return 1
	}
	if v < -1 {
		return -1
	}
	return v
}
