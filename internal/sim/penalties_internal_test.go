package sim

import (
	"testing"

	"phootball/internal/config"
	"phootball/internal/geom"
)

// TestPenaltyShootoutFreezesTakerAndHoldsKeeper drives one live penalty and checks the new
// set-piece rules: the taker is frozen on the spot through the aim phase (it never moves, it just
// strikes once), the keeper stays planted on its line until the ball is released, and once the
// strike is away the keeper is free to move.
func TestPenaltyShootoutFreezesTakerAndHoldsKeeper(t *testing.T) {
	m := BuildMatchFromConfig(NewStandardField(), 3, config.Default())
	const dt = 1.0 / 60
	m.beginShootout()
	s := m.shootout
	if s == nil || m.PlayerByID(s.takerID) == nil || m.PlayerByID(s.keeperID) == nil {
		t.Fatal("shootout setup failed (need >=2 players per side)")
	}
	taker := m.PlayerByID(s.takerID)
	keeper := m.PlayerByID(s.keeperID)
	takerStart, keeperStart := taker.Position, keeper.Position

	aimFrozenTaker, aimFrozenKeeper := true, true
	released, ballHadVel, keeperFreed := false, false, false
	charge := 0

	for i := 0; i < 1200; i++ {
		in := map[int]Intent{}
		// The keeper is told to charge forward off its line every tick.
		in[keeper.PlayerID] = Intent{Move: m.Ball.Position.Sub(keeper.Position), Throttle: 1}
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
			if geom.Dist(keeper.Position, keeperStart) > 0.5 || geom.Norm(keeper.Velocity) > 0.5 {
				aimFrozenKeeper = false
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
	if !aimFrozenKeeper {
		t.Error("the keeper must stay on its line (frozen) until the ball is released")
	}
	if !keeperFreed {
		t.Error("the keeper must be free to move once the ball is released")
	}
}
