package sim

import (
	"math"
	"testing"

	"phootball/internal/geom"
)

// TestShootAimAssist verifies the shot aim-assist: a ball within the front cone fires nudged
// toward where the player is FACING, the assist then DEGRADES across the front hemisphere
// (a wide shot is only partly nudged), and a shot at/behind +-90deg does not fire at all
// (the left-click shot is front-180 only). All angles are in radians.
func TestShootAimAssist(t *testing.T) {
	stats := DefaultStats(500)
	touchDist := stats.Radius + 10 // ball just touching (ball radius 10), inside TouchRange
	const tol = 0.02               // ~1deg tolerance

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

	// Inside the front cone (0.244 rad ~14deg): assist is 100%, so the shot fires essentially
	// straight along the facing direction (~0 off facing).
	if got := shotAngleOff(0.244); got > tol {
		t.Errorf("in-cone shot not snapped to facing: launched at %.3f rad (want ~0)", got)
	}

	// The assist spans the whole front hemisphere but DEGRADES toward the +-90deg edges: the
	// residual (launched-off / radial-off; 0 = fully snapped to facing, 1 = pure radial) grows
	// with the angle, and a wide shot is still only PARTIALLY assisted.
	resNarrow := shotAngleOff(0.6) / 0.6 // ~34deg
	resWide := shotAngleOff(1.4) / 1.4   // ~80deg
	if !(resWide > resNarrow) {
		t.Errorf("aim assist should get worse toward the edge: residual %.3f at ~80deg should exceed %.3f at ~34deg", resWide, resNarrow)
	}
	if !(resWide < 0.95) {
		t.Errorf("a ~80deg shot should still be partially assisted, residual %.3f", resWide)
	}

	// A shot at the hemisphere edge (pi/2 ~90deg) does not fire -- the shot is front-180 only.
	{
		p := NewPlayer(0, geom.NewVec(0, 0), stats, nil)
		p.Facing = geom.NewVec(1, 0)
		b := NewBall(geom.NewVec(0, touchDist), 10) // 90deg off facing
		if shoot(p, b) {
			t.Errorf("a 90deg shot should not fire (front hemisphere only)")
		}
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
