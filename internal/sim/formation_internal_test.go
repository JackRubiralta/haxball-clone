package sim

import (
	"testing"

	"phootball/internal/geom"
)

// countRoles tallies a roster by role.
func countRoles(players []*Player) (gk, mid, fwd int) {
	for _, p := range players {
		switch p.Role {
		case RoleKeeper:
			gk++
		case RoleAttacker:
			fwd++
		default:
			mid++
		}
	}
	return
}

func TestBuildFormationCountsAndRoles(t *testing.T) {
	f := NewStandardField()
	cases := []struct {
		n          int
		wantGK     int
		wantOutfld int // n minus keepers
	}{
		{1, 0, 1}, // lone outfielder, no keeper
		{2, 1, 1},
		{3, 1, 2},
		{4, 1, 3},
		{5, 1, 4},
		{6, 1, 5},
		{7, 1, 6},
	}
	for _, tc := range cases {
		id := 0
		team := &Team{Side: SideLeft, Name: "Blue"}
		players := buildFormation(f, team, tc.n, &id)
		if len(players) != tc.n {
			t.Fatalf("n=%d: got %d players, want %d", tc.n, len(players), tc.n)
		}
		gk, mid, fwd := countRoles(players)
		if gk != tc.wantGK {
			t.Errorf("n=%d: got %d keepers, want %d", tc.n, gk, tc.wantGK)
		}
		if mid+fwd != tc.wantOutfld {
			t.Errorf("n=%d: got %d outfielders, want %d", tc.n, mid+fwd, tc.wantOutfld)
		}
		// Keeper convention: when there is a keeper it is index 0 / number 1.
		if tc.wantGK == 1 {
			if players[0].Role != RoleKeeper {
				t.Errorf("n=%d: index 0 is %v, want keeper", tc.n, players[0].Role)
			}
			if players[0].Number != 1 {
				t.Errorf("n=%d: keeper number is %d, want 1", tc.n, players[0].Number)
			}
		}
		// Everyone on the left team faces the opponent goal (+X) and sits in the own (left) half.
		cx := f.CenterSpot.X
		for _, p := range players {
			if p.Facing.X <= 0 {
				t.Errorf("n=%d: player %d facing %v not toward opponent goal", tc.n, p.PlayerID, p.Facing)
			}
			if p.Position.X > cx {
				t.Errorf("n=%d: player %d at X=%.1f past halfway (cx=%.1f)", tc.n, p.PlayerID, p.Position.X, cx)
			}
			if p.Position.Y <= f.Min.Y || p.Position.Y >= f.Max.Y {
				t.Errorf("n=%d: player %d Y=%.1f on/over a touchline", tc.n, p.PlayerID, p.Position.Y)
			}
		}
	}
}

func TestBuildFormationStablePlayerIDs(t *testing.T) {
	f := NewStandardField()
	id := 0
	left := &Team{Side: SideLeft, Name: "Blue"}
	right := &Team{Side: SideRight, Name: "Red"}
	lp := buildFormation(f, left, 5, &id)
	rp := buildFormation(f, right, 5, &id)
	// PlayerIDs must be a contiguous, increasing sequence preserving the *id order.
	want := 0
	for _, p := range append(append([]*Player{}, lp...), rp...) {
		if p.PlayerID != want {
			t.Fatalf("PlayerID order broken: got %d, want %d", p.PlayerID, want)
		}
		want++
	}
	if id != 10 {
		t.Errorf("id counter = %d, want 10", id)
	}
	// Right team faces -X (toward the left goal) and sits in the right half.
	cx := f.CenterSpot.X
	for _, p := range rp {
		if p.Facing.X >= 0 {
			t.Errorf("right player %d facing %v not toward opponent goal", p.PlayerID, p.Facing)
		}
		if p.Position.X < cx {
			t.Errorf("right player %d at X=%.1f past halfway", p.PlayerID, p.Position.X)
		}
	}
}

func TestBuildMatchAsymmetric3v2(t *testing.T) {
	m := BuildMatchSized(NewStandardField(), 3, 2)
	if len(m.Teams[0].Players) != 3 {
		t.Errorf("home size = %d, want 3", len(m.Teams[0].Players))
	}
	if len(m.Teams[1].Players) != 2 {
		t.Errorf("away size = %d, want 2", len(m.Teams[1].Players))
	}
	if len(m.Players) != 5 {
		t.Errorf("total players = %d, want 5", len(m.Players))
	}
	// Each team has exactly one keeper at its index 0.
	if m.Teams[0].Players[0].Role != RoleKeeper || m.Teams[1].Players[0].Role != RoleKeeper {
		t.Errorf("each team's index 0 should be a keeper")
	}
}

func TestResetKickoffStagedArmsAndPlacesTaker(t *testing.T) {
	m := BuildMatchSized(NewStandardField(), 4, 4)
	// No goal yet -> KickoffSide is SideLeft.
	side := m.KickoffSide()
	taker := m.kickoffTaker(side)
	if taker == nil {
		t.Fatal("expected a kickoff taker")
	}

	m.resetKickoff(true)

	if !m.KickoffArmed() {
		t.Fatal("staged resetKickoff should arm the kickoff flag")
	}
	// The taker sits a bit behind the ball, inside the centre circle, facing the goal.
	gap := geom.Dist(taker.Position, m.Ball.Position)
	wantGap := m.Ball.Radius() + taker.Radius() + kickoffTakerStandoff
	if d := gap - wantGap; d > 0.01 || d < -0.01 {
		t.Errorf("taker gap = %.2f, want %.2f", gap, wantGap)
	}
	// Facing toward the opponent goal (+X for the left team).
	goalC := m.AttackingGoal(taker.Team).Center
	toGoal := geom.Unit(goalC.Sub(m.Ball.Position))
	if dot := taker.Facing.X*toGoal.X + taker.Facing.Y*toGoal.Y; dot < 0.99 {
		t.Errorf("taker facing %v not toward goal dir %v (dot=%.3f)", taker.Facing, toGoal, dot)
	}
	// The taker is on its own side of the ball (between the ball and its own half).
	if taker.Position.X >= m.Ball.Position.X {
		t.Errorf("taker X=%.1f should be behind the ball X=%.1f", taker.Position.X, m.Ball.Position.X)
	}
	// Ball remains dead-centre and stopped.
	if geom.Dist(m.Ball.Position, m.Field.CenterSpot) > 0.01 {
		t.Errorf("ball not on centre spot: %v", m.Ball.Position)
	}
	if geom.Norm(m.Ball.Velocity) != 0 {
		t.Errorf("ball should be stationary at kickoff")
	}
}

func TestResetKickoffUnstagedDoesNotStage(t *testing.T) {
	m := BuildMatchSized(NewStandardField(), 4, 4)
	taker := m.kickoffTaker(m.KickoffSide())
	home := taker.HomePosition

	m.resetKickoff(false)

	if m.KickoffArmed() {
		t.Error("unstaged resetKickoff should not arm the flag")
	}
	if geom.Dist(taker.Position, home) > 0.01 {
		t.Errorf("unstaged: taker should be at home %v, got %v", home, taker.Position)
	}
	// Everyone faces their attacking goal.
	for _, p := range m.Teams[0].Players {
		if p.Facing.X <= 0 {
			t.Errorf("left player %d not facing opponent goal: %v", p.PlayerID, p.Facing)
		}
	}
}

func TestStepDisarmsKickoffOnTouch(t *testing.T) {
	m := BuildMatchSized(NewStandardField(), 2, 2)
	m.resetKickoff(true)
	if !m.KickoffArmed() {
		t.Fatal("precondition: kickoff should be armed")
	}
	// Drive the taker into the ball and strike: it starts a bit off the ball (inside the circle),
	// so move toward the goal (the ball is just ahead) while charging, then release.
	taker := m.kickoffTaker(m.KickoffSide())
	in := map[int]Intent{
		taker.PlayerID: {Move: geom.NewVec(1, 0), Throttle: 1, ShootHeld: true, Aim: geom.NewVec(1, 0)},
	}
	for i := 0; i < 30; i++ {
		m.Step(in, 1.0/60.0)
	}
	in[taker.PlayerID] = Intent{Move: geom.NewVec(1, 0), Throttle: 1, ShootHeld: false}
	m.Step(in, 1.0/60.0)
	if m.LastTouch == nil {
		t.Fatal("expected the taker to have touched the ball")
	}
	if m.KickoffArmed() {
		t.Error("kickoff should be disarmed once a touch is recorded")
	}
}
