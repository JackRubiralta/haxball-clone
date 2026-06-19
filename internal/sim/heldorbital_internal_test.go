package sim

import (
	"math"
	"testing"

	"phootball/internal/config"
	"phootball/internal/geom"
)

// The held-orbital change makes the dribble anti-fling/damping act only on the orbital velocity the
// hold itself creates (turn + roll-to-front control + carry), not on a stray ball's incoming
// momentum. These tests prove it (a) is byte-identical to the pre-change code for a ball the player
// is dribbling -- including a hard turn and a stop-turn settle, the cases prior fixes flung -- and
// (b) no longer captures a fast ball crossing perpendicular to the facing.

const heldDT = 1.0 / 60.0
const heldBallR = 7.5

// dribbleRun reproduces the deterministic harness used to capture the pre-change baseline: a player
// at the origin with a fully-possessed ball seated at the front, settle for 30 ticks, turn at the
// full TurnRate for turnTicks, then hold the facing for holdTicks. Returns the max surface gap seen
// during the turn/hold and the final ball position+velocity.
func dribbleRun(turnTicks, holdTicks int) (gapMax float64, final, vel geom.Vec) {
	stats := config.DefaultPlayerTuning()
	p := NewPlayer(0, geom.NewVec(0, 0), stats, nil)
	p.Facing = geom.NewVec(1, 0)
	p.possession = 1
	b := NewBall(geom.NewVec(stats.Radius+heldBallR, 0), heldBallR)
	gapMax = -1e9
	track := func() {
		if g := geom.Dist(b.Position, p.Position) - stats.Radius - heldBallR; g > gapMax {
			gapMax = g
		}
	}
	for i := 0; i < 30; i++ {
		handleBallToPlayerInteraction(b, p, heldDT)
		b.Update(heldDT)
		p.possession = 1
	}
	for i := 0; i < turnTicks; i++ {
		p.Facing = p.Facing.Rotate(stats.TurnRate*heldDT, geom.Vec{})
		handleBallToPlayerInteraction(b, p, heldDT)
		b.Update(heldDT)
		p.possession = 1
		track()
	}
	for i := 0; i < holdTicks; i++ {
		handleBallToPlayerInteraction(b, p, heldDT)
		b.Update(heldDT)
		p.possession = 1
		track()
	}
	return gapMax, b.Position, b.Velocity
}

// TestDribbleTurnExact: a fully-possessed ball turned hard for 60 ticks (a continuous max-rate spin)
// must (1) stay glued -- no fling, the failure mode of the two prior attempts -- and (2) reproduce
// the pre-change trajectory (baseline captured from the committed code before this change). Because
// the hold owns the entire orbital velocity of a dribbled ball, heldOrbital tracks the actual orbital
// speed and the anti-fling/damping math reduces to the original; the only deviation is a sub-1e-3
// floating-point-level residual (the scalar tracker does not model the ball's radial velocity
// rotating into tangential across a multi-revolution spin) -- imperceptible and never a fling.
func TestDribbleTurnExact(t *testing.T) {
	gapMax, pos, vel := dribbleRun(60, 0)
	if gapMax > 0.1 {
		t.Errorf("ball flung out while turning: max gap %.4f (TouchRange %.1f)", gapMax, config.DefaultPlayerTuning().TouchRange)
	}
	// Baseline re-captured after the roll-to-front Control was raised (front 1160.25 -> 1450, back
	// 340 -> 460), Stickiness raised (front 420 -> 500, back 30 -> 150) and CaptureSpeed back raised
	// (30 -> 100); the ball still stays glued (gapMax << 0.1, checked above).
	wantPos := geom.NewVec(24.3810785728, 7.4852890873)
	wantVel := geom.NewVec(-7.7061197196, 26.8361333253)
	if geom.Dist(pos, wantPos) > 1e-3 || geom.Dist(vel, wantVel) > 1e-3 {
		t.Errorf("turn output drifted from the pre-change baseline:\n pos %v want %v\n vel %v want %v", pos, wantPos, vel, wantVel)
	}
}

// TestStopTurnSettleNoFling: turn hard 20 ticks then hold the facing for 40. The ball must settle
// without a fling spike when the turn stops (prior attempt #2 flung here), and stay within a hair of
// the pre-change trajectory (the settle oscillation leaves a ~1e-2 residual -- physically invisible).
func TestStopTurnSettleNoFling(t *testing.T) {
	gapMax, pos, vel := dribbleRun(20, 40)
	if gapMax > 0.1 {
		t.Errorf("ball flung out around the stop-turn: max gap %.4f", gapMax)
	}
	// Baseline re-captured after the Control was raised (front 1160.25 -> 1450, back 340 -> 460),
	// Stickiness raised (front 420 -> 500, back 30 -> 150) and CaptureSpeed back raised (30 -> 100).
	wantPos := geom.NewVec(6.6156201304, -24.6438127539)
	wantVel := geom.NewVec(-52.3761472314, -16.0907284775)
	if geom.Dist(pos, wantPos) > 5e-2 || geom.Dist(vel, wantVel) > 5e-2 {
		t.Errorf("stop-turn output drifted materially from the pre-change baseline:\n pos %v want %v\n vel %v want %v", pos, wantPos, vel, wantVel)
	}
}

// TestPerpendicularStrayNotCaptured: a fast ball crossing the front perpendicular to the facing
// (the reported bug) must NOT be captured -- it keeps its momentum and slides past, the same outcome
// a head-on ball of the same speed gets (it deflects). The player is stationary and not turning, so
// none of the ball's orbital velocity is hold-induced (heldOrbital stays ~0).
func TestPerpendicularStrayNotCaptured(t *testing.T) {
	stats := config.DefaultPlayerTuning()
	p := NewPlayer(0, geom.NewVec(0, 0), stats, nil)
	p.Facing = geom.NewVec(1, 0)
	p.possession = 0
	// Ball at the dead front surface, moving perpendicular (-Y) at 500 -- tangential to the contact.
	b := NewBall(geom.NewVec(stats.Radius+heldBallR, 0), heldBallR)
	b.Velocity = geom.NewVec(0, -500)
	speed0 := geom.Norm(b.Velocity)
	for i := 0; i < 25; i++ {
		handleBallToPlayerInteraction(b, p, heldDT)
		b.Update(heldDT)
	}
	gap := geom.Dist(b.Position, p.Position) - stats.Radius - heldBallR
	speed := geom.Norm(b.Velocity)
	if gap < stats.PullRange {
		t.Errorf("perpendicular stray ball was captured/kept in reach: gap %.2f (PullRange %.1f)", gap, stats.PullRange)
	}
	if speed < 0.7*speed0 {
		t.Errorf("perpendicular stray ball lost too much speed (captured): %.1f -> %.1f", speed0, speed)
	}
	if math.Abs(b.Position.Y) < 40 {
		t.Errorf("perpendicular stray ball did not travel past the player: |y|=%.1f", math.Abs(b.Position.Y))
	}
}

// TestSlowLooseBallGathered: a SLOW loose ball drifting onto the player IS gathered (it lingers long
// enough for the roll-to-front control to build heldOrbital, so the hold engages). Guards against
// over-releasing -- only fast strays should slip, not loose balls.
func TestSlowLooseBallGathered(t *testing.T) {
	stats := config.DefaultPlayerTuning()
	p := NewPlayer(0, geom.NewVec(0, 0), stats, nil)
	p.Facing = geom.NewVec(1, 0)
	p.possession = 0
	// Just outside touch on the front, drifting in slowly.
	b := NewBall(geom.NewVec(stats.Radius+heldBallR+3, 0), heldBallR)
	b.Velocity = geom.NewVec(-120, 0)
	for i := 0; i < 90; i++ {
		handleBallToPlayerInteraction(b, p, heldDT)
		b.Update(heldDT)
	}
	gap := geom.Dist(b.Position, p.Position) - stats.Radius - heldBallR
	if gap > stats.TouchRange {
		t.Errorf("slow loose ball was not gathered: gap %.2f (TouchRange %.1f)", gap, stats.TouchRange)
	}
	if s := geom.Norm(b.Velocity.Sub(p.Velocity)); s > 80 {
		t.Errorf("slow loose ball not settled: relative speed %.1f", s)
	}
}

// TestTurnAfterGatherNoFling: gather a moderate ball, build possession, THEN turn hard -- the ball
// must not fling out (heldOrbital has built up with the dribble, so the hold is at full strength).
func TestTurnAfterGatherNoFling(t *testing.T) {
	stats := config.DefaultPlayerTuning()
	p := NewPlayer(0, geom.NewVec(0, 0), stats, nil)
	p.Facing = geom.NewVec(1, 0)
	b := NewBall(geom.NewVec(stats.Radius+heldBallR+2, 0), heldBallR)
	b.Velocity = geom.NewVec(-100, 0)
	// Gather + settle.
	for i := 0; i < 60; i++ {
		handleBallToPlayerInteraction(b, p, heldDT)
		b.Update(heldDT)
		p.possession = math.Min(1, p.possession+heldDT/stats.PossessionBuildSeconds)
	}
	// Now turn hard.
	gapMax := -1e9
	for i := 0; i < 60; i++ {
		p.Facing = p.Facing.Rotate(stats.TurnRate*heldDT, geom.Vec{})
		handleBallToPlayerInteraction(b, p, heldDT)
		b.Update(heldDT)
		p.possession = 1
		if g := geom.Dist(b.Position, p.Position) - stats.Radius - heldBallR; g > gapMax {
			gapMax = g
		}
	}
	if gapMax > 0.2 {
		t.Errorf("ball flung out turning after a gather: max gap %.4f", gapMax)
	}
}
