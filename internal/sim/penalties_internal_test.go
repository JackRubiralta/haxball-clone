package sim

import (
	"testing"

	"phootball/internal/config"
	"phootball/internal/geom"
)

// keeperLineX returns the keeper's goal-line X (the line it is held near) for the defending side,
// mirroring setupKick / clampKeeperToLine geometry.
func keeperLineX(m *Match, defender Side, keeper *Player) float64 {
	spot := m.Field.PenaltySpot(defender)
	goalX := m.Field.Min.X
	if defender == SideRight {
		goalX = m.Field.Max.X
	}
	into := 1.0
	if goalX < spot.X {
		into = -1.0
	}
	return goalX - into*(keeper.Radius()+1)
}

// TestPenaltyShootoutFreezesTakerAndClampsKeeper drives one live penalty and checks the set-piece
// rules: the taker is frozen on the spot through the aim phase (it never moves, it just strikes
// once); the keeper, before release, MAY shuffle laterally (Y) but its X stays within
// keeperLineBand of its goal line (it never charges the spot); once the strike is away the keeper
// is free to move off the line.
func TestPenaltyShootoutFreezesTakerAndClampsKeeper(t *testing.T) {
	m := BuildMatchFromConfig(NewStandardField(), 3, config.Default())
	const dt = 1.0 / 60
	m.beginShootout()
	s := m.shootout
	if s == nil || m.PlayerByID(s.takerID) == nil || m.PlayerByID(s.keeperID) == nil {
		t.Fatal("shootout setup failed (need >=2 players per side)")
	}
	taker := m.PlayerByID(s.takerID)
	keeper := m.PlayerByID(s.keeperID)
	takerStart := taker.Position
	defender := s.taker.Opponent()

	aimFrozenTaker := true
	keeperOffLineX := false // keeper X ever charged beyond the band
	keeperMovedLaterally := false
	released, ballHadVel, keeperFreed := false, false, false
	charge := 0

	for i := 0; i < 1200; i++ {
		in := map[int]Intent{}
		// The keeper is told to charge diagonally toward the spot AND up the line (a +Y, toward-spot
		// move): it should be free to shuffle laterally (Y) but its X must stay clamped to the band.
		toBall := m.Ball.Position.Sub(keeper.Position)
		in[keeper.PlayerID] = Intent{Move: geom.NewVec(toBall.X, toBall.Y+40), Throttle: 1}
		// The taker charges briefly then releases (only while the kick is live and not yet struck);
		// no Aim means it keeps its setup facing toward the goal.
		if s.kickState == kickLive && !s.released {
			charge++
			in[taker.PlayerID] = Intent{ShootHeld: charge < 12}
		}

		m.Step(in, dt)
		if m.shootout == nil {
			break
		}
		s = m.shootout
		taker = m.PlayerByID(s.takerID)
		keeper = m.PlayerByID(s.keeperID)

		if s.kickState == kickLive && !s.released {
			if geom.Dist(taker.Position, takerStart) > 0.5 {
				aimFrozenTaker = false
			}
			lineX := keeperLineX(m, defender, keeper)
			// X must stay within the band of the goal line (allow a small tolerance for the
			// half-tick the clamp acts at the end of a step).
			if absF(keeper.Position.X-lineX) > keeperLineBand+1.5 {
				keeperOffLineX = true
			}
			if absF(keeper.Velocity.Y) > 0.5 {
				keeperMovedLaterally = true
			}
		}
		if s.released && !released {
			released = true
			ballHadVel = geom.Norm(m.Ball.Velocity) > 1
		}
		if s.kickState == kickLive && s.released && geom.Norm(keeper.Velocity) > 0.5 {
			keeperFreed = true
		}
		if s.taken[0]+s.taken[1] > 0 { // the kick resolved
			break
		}
	}

	if !released {
		t.Fatal("the taker never released its single strike")
	}
	if !ballHadVel {
		t.Error("the ball should be moving once the strike is released")
	}
	if !aimFrozenTaker {
		t.Error("the taker must stay frozen on the spot during the aim phase")
	}
	if keeperOffLineX {
		t.Error("the keeper must stay within keeperLineBand of its goal line before release (it must not charge the spot)")
	}
	if !keeperMovedLaterally {
		t.Error("the keeper should be able to move laterally along its line before release")
	}
	if !keeperFreed {
		t.Error("the keeper must be free to move once the ball is released")
	}
}

// TestPenaltyScoredWaitLongerThanMiss checks the post-result timing split: after a SCORED penalty
// the next kick does not begin for at least penaltyGoalSeconds (the 3s savour pause).
func TestPenaltyScoredWaitLongerThanMiss(t *testing.T) {
	m := BuildMatchFromConfig(NewStandardField(), 3, config.Default())
	const dt = 1.0 / 60
	m.beginShootout()
	s := m.shootout
	if s == nil {
		t.Fatal("shootout setup failed")
	}
	// Force a SCORED first kick by recording it directly. State goes to kickDone with timer 0.
	m.recordKick(true)
	if !s.lastScored {
		t.Fatal("expected the recorded kick to be scored")
	}
	if s.kickState != kickDone {
		t.Fatalf("expected kickDone after recordKick, got %v", s.kickState)
	}

	// Advance ticks; the next kick (leaving kickDone) must not begin before penaltyGoalSeconds.
	beganAt := -1.0
	waited := 0.0
	for i := 0; i < int(penaltyGoalSeconds*60)+240; i++ {
		m.stepShootout(map[int]Intent{}, dt)
		waited += dt
		if m.shootout == nil {
			break
		}
		s = m.shootout
		if s.kickState != kickDone { // left the result pause -> the next kick has begun
			beganAt = waited
			break
		}
	}
	if beganAt < 0 {
		t.Fatal("the next kick never began")
	}
	if beganAt+1e-9 < penaltyGoalSeconds {
		t.Errorf("after a scored pen the next kick began at %.3fs, want >= penaltyGoalSeconds=%.1f", beganAt, penaltyGoalSeconds)
	}
	if penaltyGoalSeconds <= penaltyResultSeconds {
		t.Errorf("penaltyGoalSeconds (%.1f) must exceed penaltyResultSeconds (%.1f)", penaltyGoalSeconds, penaltyResultSeconds)
	}
}

// absF is a small float helper local to the test.
func absF(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}
