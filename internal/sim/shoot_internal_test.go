package sim

import (
	"math"
	"testing"

	"phootball/internal/geom"
)

// TestPokeAndFrontHemisphereShot covers the middle-click poke and the redesigned left-click
// shot: the poke is an instant min-power radial push that reaches the whole pull radius and is
// equal in every direction; the shot fires only in the front 180deg and its power degrades
// (much faster) toward the +-90deg edges.
func TestPokeAndFrontHemisphereShot(t *testing.T) {
	s := fieldPlayerStats()
	const ballR = 10.0
	d0 := s.Radius + ballR // distance at zero gap
	pokePower := s.Shoot.Eval(0) * pokePowerFactor

	newP := func() *Player {
		p := NewPlayer(0, geom.NewVec(0, 0), s, nil)
		p.Facing = geom.NewVec(1, 0)
		return p
	}

	// --- POKE fires within the PULL radius even when NOT touching, at the strong poke power. ---
	gap := 3.0 // between TouchRange (2) and PullRange (5)
	if !(gap < s.PullRange && gap >= s.TouchRange) {
		t.Fatalf("test setup: gap %.1f should sit between TouchRange %.1f and PullRange %.1f", gap, s.TouchRange, s.PullRange)
	}
	pf := NewBall(geom.NewVec(d0+gap, 0), ballR)
	if !poke(newP(), pf) {
		t.Fatalf("poke should fire on a ball within the pull radius (gap %.1f)", gap)
	}
	if got := geom.Norm(pf.Velocity); math.Abs(got-pokePower) > 1e-6 {
		t.Errorf("poke power should be the strong poke power %.1f, got %.1f", pokePower, got)
	}
	// The poke is much stronger than a tap, and stronger than a full-charge front shot.
	if !(pokePower > s.Shoot.Eval(0)) {
		t.Errorf("poke should hit harder than a full-charge front shot (%.1f), got %.1f", s.Shoot.Eval(0), pokePower)
	}
	// A held shot must NOT fire there -- it still needs the ball touching.
	if shoot(newP(), NewBall(geom.NewVec(d0+gap, 0), ballR)) {
		t.Errorf("a held shot should not fire on a ball only in the pull radius (gap %.1f >= TouchRange %.1f)", gap, s.TouchRange)
	}
	// Poke power is EQUAL in every direction: a ball directly behind gets the same speed.
	pb := NewBall(geom.NewVec(-(d0+gap), 0), ballR)
	poke(newP(), pb)
	if got := geom.Norm(pb.Velocity); math.Abs(got-pokePower) > 1e-6 {
		t.Errorf("poke power should be equal in every direction (%.1f), behind got %.1f", pokePower, got)
	}
	if pb.Velocity.X >= 0 {
		t.Errorf("a poke on a ball behind should push it backward, vx=%.2f", pb.Velocity.X)
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
