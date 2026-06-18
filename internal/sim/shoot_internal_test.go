package sim

import (
	"math"
	"testing"

	"phootball/internal/geom"
)

// TestPushAndFrontHemisphereShot covers the middle-click push and the redesigned left-click
// shot: the push is an instant min-power radial push that reaches the whole pull radius and is
// equal in every direction; the shot fires only in the front 180deg and its power degrades
// (much faster) toward the +-90deg edges.
func TestPushAndFrontHemisphereShot(t *testing.T) {
	s := fieldPlayerTuning()
	const ballR = 10.0
	d0 := s.Radius + ballR // distance at zero gap
	pushPower := s.Shoot.Eval(0) * pushPowerFactor

	newP := func() *Player {
		p := NewPlayer(0, geom.NewVec(0, 0), s, nil)
		p.Facing = geom.NewVec(1, 0)
		return p
	}

	// --- PUSH fires within the PULL radius even when NOT touching, at the push power. ---
	gap := 3.0 // between TouchRange (2) and PullRange (5)
	if !(gap < s.PullRange && gap >= s.TouchRange) {
		t.Fatalf("test setup: gap %.1f should sit between TouchRange %.1f and PullRange %.1f", gap, s.TouchRange, s.PullRange)
	}
	pf := NewBall(geom.NewVec(d0+gap, 0), ballR)
	if !push(newP(), pf) {
		t.Fatalf("push should fire on a ball within the pull radius (gap %.1f)", gap)
	}
	if got := geom.Norm(pf.Velocity); math.Abs(got-pushPower) > 1e-6 {
		t.Errorf("push power should be the push power %.1f, got %.1f", pushPower, got)
	}
	// The push fires at 70% of a full-charge front shot.
	if math.Abs(pushPower-0.7*s.Shoot.Eval(0)) > 1e-6 {
		t.Errorf("push should be ~70%% of a full front shot (%.1f), got %.1f", 0.7*s.Shoot.Eval(0), pushPower)
	}
	// A held shot must NOT fire there -- it still needs the ball touching.
	if shoot(newP(), NewBall(geom.NewVec(d0+gap, 0), ballR)) {
		t.Errorf("a held shot should not fire on a ball only in the pull radius (gap %.1f >= TouchRange %.1f)", gap, s.TouchRange)
	}
	// Push power is EQUAL in every direction: a ball directly behind gets the same speed.
	pb := NewBall(geom.NewVec(-(d0+gap), 0), ballR)
	push(newP(), pb)
	if got := geom.Norm(pb.Velocity); math.Abs(got-pushPower) > 1e-6 {
		t.Errorf("push power should be equal in every direction (%.1f), behind got %.1f", pushPower, got)
	}
	if pb.Velocity.X >= 0 {
		t.Errorf("a push on a ball behind should push it backward, vx=%.2f", pb.Velocity.X)
	}

	// --- SHOOT: front 180deg only, power degrading toward the edges. ---
	d := d0 + 1.0 // touching (gap 1 < TouchRange)

	frontBall := NewBall(geom.NewVec(d, 0), ballR)
	if !shoot(newP(), frontBall) {
		t.Fatalf("a dead-front shot should fire")
	}
	frontPower := geom.Norm(frontBall.Velocity)

	// 60deg off the facing: still in the front hemisphere, but much weaker.
	sb := NewBall(geom.NewVec(d*math.Cos(math.Pi/3), d*math.Sin(math.Pi/3)), ballR)
	if !shoot(newP(), sb) {
		t.Fatalf("a 60deg shot is in the front hemisphere and should fire")
	}
	if sidePower := geom.Norm(sb.Velocity); !(sidePower < frontPower*0.8) {
		t.Errorf("a 60deg shot should be much weaker than a front shot: side %.1f vs front %.1f", sidePower, frontPower)
	}

	// 135deg (behind the hemisphere): no shot at all, ball untouched.
	bb := NewBall(geom.NewVec(d*math.Cos(3*math.Pi/4), d*math.Sin(3*math.Pi/4)), ballR)
	if shoot(newP(), bb) {
		t.Errorf("a shot behind the front hemisphere should not fire")
	}
	if bb.Velocity != (geom.Vec{}) {
		t.Errorf("a disallowed back shot must not move the ball, got %v", bb.Velocity)
	}
}
