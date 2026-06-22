package sim

import (
	"math"
	"testing"

	"phootball/internal/config"
	"phootball/internal/geom"
)

// TestShootAimAssist verifies the shot aim-assist: a ball within the front cone fires nudged
// toward where the player is FACING, the assist then DEGRADES across the front hemisphere
// (a wide shot is only partly nudged), and a shot at/behind +-90deg does not fire at all
// (the left-click shot is front-180 only). All angles are in radians.
func TestShootAimAssist(t *testing.T) {
	stats := config.DefaultPlayerTuning()
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

	// Assist is now UNIFORM (1.0 = 100%) across the whole front hemisphere -- no angular degradation --
	// so a ball anywhere in front fires essentially exactly along the facing (full assist, no residual).
	for _, off := range []float64{0.244, 0.6, 1.0, 1.4} { // ~14, 34, 57, 80 deg
		if got := shotAngleOff(off); got > 0.05 { // ~3deg: strongly assisted at every front angle
			t.Errorf("aim assist should fire ~along the facing across the hemisphere: launched %.3f rad off at ball-offset %.3f rad", got, off)
		}
	}
	// And it does NOT get worse toward the edge: a wide shot is assisted at least as well as a
	// narrow one (the opposite of the old cone-falloff behaviour).
	if shotAngleOff(1.4) > shotAngleOff(0.6)+tol {
		t.Errorf("aim assist must not degrade toward the +-90deg edge (uniform across the hemisphere)")
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
