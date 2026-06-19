package sim

import (
	"math"
	"testing"

	"phootball/internal/config"
	"phootball/internal/geom"
)

// TestPushAndFrontHemisphereShot covers the middle-click push and the redesigned left-click
// shot: the push is an instant min-power radial push that reaches the whole pull radius and is
// equal in every direction; the shot fires only in the front 180deg arc, at full power within the
// inner +-30deg cone and tapering to 0 toward the +-90deg edges.
func TestPushAndFrontHemisphereShot(t *testing.T) {
	s := config.DefaultPlayerTuning()
	const ballR = 10.0
	d0 := s.Radius + ballR // distance at zero gap
	pushPower := s.Shoot.Front * pushPowerFactor

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
	if math.Abs(pushPower-0.7*s.Shoot.Front) > 1e-6 {
		t.Errorf("push should be ~70%% of a full front shot (%.1f), got %.1f", 0.7*s.Shoot.Front, pushPower)
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

	// --- SHOOT: front 180deg arc -- full power within the +-30deg cone, tapering to 0 by the edge. ---
	d := d0 + 1.0 // touching (gap 1 < TouchRange)
	atDeg := func(deg float64) *Ball {
		r := deg * math.Pi / 180
		return NewBall(geom.NewVec(d*math.Cos(r), d*math.Sin(r)), ballR)
	}

	frontBall := NewBall(geom.NewVec(d, 0), ballR)
	if !shoot(newP(), frontBall) {
		t.Fatalf("a dead-front shot should fire")
	}
	frontPower := geom.Norm(frontBall.Velocity)

	// 25deg off the facing: still INSIDE the +-30deg full-power cone, so full power.
	cb := atDeg(25)
	if !shoot(newP(), cb) {
		t.Fatalf("a 25deg shot is inside the full-power cone and should fire")
	}
	if conePower := geom.Norm(cb.Velocity); math.Abs(conePower-frontPower) > 1e-6 {
		t.Errorf("a shot inside the +-30deg cone should be at full power: %.3f vs front %.3f", conePower, frontPower)
	}

	// 75deg off the facing: out in the taper, much weaker than a front shot.
	sb := atDeg(75)
	if !shoot(newP(), sb) {
		t.Fatalf("a 75deg shot is inside the 180deg arc and should fire")
	}
	if sidePower := geom.Norm(sb.Velocity); !(sidePower < frontPower*0.6) {
		t.Errorf("a 75deg shot should be much weaker than a front shot: side %.1f vs front %.1f", sidePower, frontPower)
	}

	// 135deg (behind the arc): no shot at all, ball untouched.
	bb := atDeg(135)
	if shoot(newP(), bb) {
		t.Errorf("a shot behind the front arc should not fire")
	}
	if bb.Velocity != (geom.Vec{}) {
		t.Errorf("a disallowed back shot must not move the ball, got %v", bb.Velocity)
	}
}

// TestPushLatchRisingEdge verifies the middle-click push fires only on the RISING edge of the
// (held) push signal, reconstructed in the sim -- so re-applying the same intent across ticks (as
// the authoritative server does) jabs exactly once, and a release re-arms it. This is the fix that
// makes the push work in multiplayer, treating it like the other held abilities.
func TestPushLatchRisingEdge(t *testing.T) {
	s := config.DefaultPlayerTuning()
	p := NewPlayer(0, geom.NewVec(0, 0), s, nil)
	p.Facing = geom.NewVec(1, 0)
	m := &Match{Ball: NewBall(geom.NewVec(1e6, 1e6), 10)} // ball far away: no possession penalty / nil deref
	dt := 1.0 / 60.0
	held := Intent{Push: true}

	m.applyIntent(p, held, dt)
	if !p.wantsPush {
		t.Fatal("the first held-push tick should latch the jab (rising edge)")
	}
	p.wantsPush = false // the kick phase consumes wantsPush each tick

	for i := 0; i < 5; i++ {
		m.applyIntent(p, held, dt)
		if p.wantsPush {
			t.Fatalf("a held push must not re-latch on continued-hold tick %d", i)
		}
	}

	m.applyIntent(p, Intent{Push: false}, dt) // release re-arms
	m.applyIntent(p, held, dt)
	if !p.wantsPush {
		t.Fatal("a fresh press after a release should latch the jab again")
	}
}
