package sim

import (
	"testing"

	"phootball/internal/config"
	"phootball/internal/geom"
)

// TestZoneRectOverlapsCircle: a player counts as in the box when ANY part of its circle overlaps
// it, with correctly rounded corners (not a square AABB).
func TestZoneRectOverlapsCircle(t *testing.T) {
	z := ZoneRect{Min: geom.NewVec(0, 0), Max: geom.NewVec(100, 100)}
	const rad = 10
	cases := []struct {
		name string
		c    geom.Vec
		want bool
	}{
		{"centre inside", geom.NewVec(50, 50), true},
		{"centre on an edge", geom.NewVec(0, 50), true},
		{"centre outside but circle overlaps an edge", geom.NewVec(-5, 50), true},
		{"centre far outside an edge", geom.NewVec(-15, 50), false},
		{"circle reaches into a corner", geom.NewVec(-5, -5), true},          // dist ~7.07 < 10
		{"corner diagonal just clear (rounded)", geom.NewVec(-8, -8), false}, // dist ~11.3 > 10
	}
	for _, tc := range cases {
		if got := z.overlapsCircle(tc.c, rad); got != tc.want {
			t.Errorf("%s: overlapsCircle(%v, %g) = %v, want %v", tc.name, tc.c, float64(rad), got, tc.want)
		}
	}
}

// TestBoxCapDefendingAndOpponentSeparately: the penalty box caps the defending team and the
// opponent SEPARATELY; occupancy is counted on any circle overlap; and a full box is a BARRIER
// that pushes the surplus player's circle clear (blocking entry past the cap).
func TestBoxCapDefendingAndOpponentSeparately(t *testing.T) {
	m := BuildMatchFromConfig(NewStandardField(), 4, config.Default())
	m.Rules.Enforcement = config.EnforceClamp // correct immediately, no grace
	m.Rules.OffsideEnabled = false
	m.Rules.GoalAreaMaxPlayers, m.Rules.GoalAreaMaxOpponents = 0, 0

	var defTeam, oppTeam *Team
	for _, tm := range m.Teams {
		if tm.Side == SideLeft {
			defTeam = tm // defends the left goal -> owns the left penalty box
		} else {
			oppTeam = tm
		}
	}
	box := m.Field.PenaltyAreaBox(SideLeft)
	if box.empty() {
		t.Fatal("standard field should have a left penalty area")
	}
	rad := defTeam.Players[0].Radius()
	midY := (box.Min.Y + box.Max.Y) / 2
	deepInside := geom.NewVec((box.Min.X+box.Max.X)/2, midY)
	// centre just OUTSIDE the pitch-facing (max-X) edge, but the circle still overlaps the box.
	edgeOverlap := geom.NewVec(box.Max.X+rad*0.5, midY)
	away := geom.NewVec((box.Min.X+box.Max.X)/2, box.Min.Y-10*rad) // clear of every box

	place := func() {
		for _, p := range m.Players {
			p.Position = away
		}
		// Defending team: P0 (the keeper) deep inside -> always kept; P1 only overlapping the edge
		// -> the surplus, walled out.
		defTeam.Players[0].Position = deepInside
		defTeam.Players[1].Position = edgeOverlap
		// Opponent: P0 deep inside (established -> kept), P1 only overlapping the edge (a newcomer
		// just poking in -> the surplus). The cap keeps the DEEPEST occupant, not roster order, so a
		// player pushing in cannot eject one already settled inside.
		oppTeam.Players[0].Position = deepInside
		oppTeam.Players[1].Position = edgeOverlap
	}

	// --- Caps of 1 for each side: one of each stays, the extra is barriered out. ---
	place()
	m.Rules.PenaltyBoxMaxPlayers = 1
	m.Rules.PenaltyBoxMaxOpponents = 1
	enforceZoneRules(m, 1.0/60)

	if !box.overlapsCircle(defTeam.Players[0].Position, rad) {
		t.Error("the allowed defender should remain in the box")
	}
	if box.overlapsCircle(defTeam.Players[1].Position, rad) {
		t.Error("the surplus defender (counted via circle overlap) should be barriered out of the box")
	}
	if !box.overlapsCircle(oppTeam.Players[0].Position, rad) {
		t.Error("the allowed attacker should remain in the box")
	}
	if box.overlapsCircle(oppTeam.Players[1].Position, rad) {
		t.Error("the surplus attacker should be barriered out of the box")
	}

	// --- Caps are independent: with the opponent cap OFF, both attackers stay even though the
	// defending cap (1) still evicts the surplus defender. ---
	place()
	m.Rules.PenaltyBoxMaxPlayers = 1
	m.Rules.PenaltyBoxMaxOpponents = 0 // opponents unlimited
	enforceZoneRules(m, 1.0/60)

	if box.overlapsCircle(defTeam.Players[1].Position, rad) {
		t.Error("defending cap should still evict its surplus player")
	}
	if !box.overlapsCircle(oppTeam.Players[0].Position, rad) || !box.overlapsCircle(oppTeam.Players[1].Position, rad) {
		t.Error("with the opponent cap off, both attackers should be allowed to stay")
	}
}

// TestBoxCapKeepsEstablishedNotNewcomer: a full box keeps the player already SETTLED deep inside
// and walls out the one merely poking in at the edge -- decided by how deep each sits, NOT roster
// order. So a player pushing in can never eject an established occupant (the reported bug).
func TestBoxCapKeepsEstablishedNotNewcomer(t *testing.T) {
	m := BuildMatchFromConfig(NewStandardField(), 4, config.Default())
	m.Rules.Enforcement = config.EnforceClamp
	m.Rules.OffsideEnabled = false
	m.Rules.GoalAreaMaxPlayers, m.Rules.GoalAreaMaxOpponents = 0, 0
	m.Rules.PenaltyBoxMaxPlayers, m.Rules.PenaltyBoxMaxOpponents = 0, 1 // cap the opponent at 1

	var opp *Team // the right-side team is the opponent in the LEFT box (no keeper exemption there)
	for _, tm := range m.Teams {
		if tm.Side == SideRight {
			opp = tm
		}
	}
	box := m.Field.PenaltyAreaBox(SideLeft)
	rad := opp.Players[0].Radius()
	midY := (box.Min.Y + box.Max.Y) / 2
	for _, p := range m.Players {
		p.Position = geom.NewVec((box.Min.X+box.Max.X)/2, box.Min.Y-10*rad) // everyone clear of the box
	}
	// LOWER-roster newcomer only overlapping the pitch edge; HIGHER-roster player settled deep.
	newcomer, settled := opp.Players[1], opp.Players[2]
	newcomer.Position = geom.NewVec(box.Max.X+rad*0.5, midY)
	settled.Position = geom.NewVec((box.Min.X+box.Max.X)/2, midY)

	enforceZoneRules(m, 1.0/60)

	if !box.overlapsCircle(settled.Position, rad) {
		t.Error("the settled (deep) occupant should be kept, even though it is higher roster than the newcomer")
	}
	if box.overlapsCircle(newcomer.Position, rad) {
		t.Error("the newcomer poking in at the edge should be the one walled out, not the established occupant")
	}
}

// TestGoalAreaKeeperOnly: the keeper-only goal-area mode admits ONLY the box owner's keeper and
// walls out everyone else (an own outfielder AND an opponent) at the line -- as a one-tick clamp
// just outside, never a teleport. It also confirms the wall rests just clear of the marking
// (boxLineClearance) rather than flush on it.
func TestGoalAreaKeeperOnly(t *testing.T) {
	m := BuildMatchFromConfig(NewStandardField(), 4, config.Default())
	m.Rules.Enforcement = config.EnforceClamp // correct immediately, no grace
	m.Rules.OffsideEnabled = false
	m.Rules.PenaltyBoxMaxPlayers, m.Rules.PenaltyBoxMaxOpponents = 0, 0
	m.Rules.GoalAreaMaxPlayers, m.Rules.GoalAreaMaxOpponents = 0, 0
	m.Rules.GoalAreaKeeperOnly = true

	var defTeam, oppTeam *Team
	for _, tm := range m.Teams {
		if tm.Side == SideLeft {
			defTeam = tm // owns the left goal area
		} else {
			oppTeam = tm
		}
	}
	box := m.Field.GoalAreaBox(SideLeft)
	if box.empty() {
		t.Fatal("standard field should have a left goal area")
	}
	if defTeam.Players[0].Role != RoleKeeper {
		t.Fatalf("expected P0 of the owning team to be the keeper")
	}
	rad := defTeam.Players[0].Radius()
	midY := (box.Min.Y + box.Max.Y) / 2
	deepInside := geom.NewVec((box.Min.X+box.Max.X)/2, midY)
	away := geom.NewVec((box.Min.X+box.Max.X)/2, box.Min.Y-10*rad)

	for _, p := range m.Players {
		p.Position = away
	}
	keeper := defTeam.Players[0]
	ownOutfield := defTeam.Players[1]
	opponent := oppTeam.Players[1]
	keeper.Position = deepInside
	ownOutfield.Position = deepInside
	opponent.Position = deepInside

	enforceZoneRules(m, 1.0/60)

	if !box.overlapsCircle(keeper.Position, rad) {
		t.Error("keeper-only: the owner's keeper must remain inside the goal area")
	}
	if box.overlapsCircle(ownOutfield.Position, rad) {
		t.Error("keeper-only: an own outfielder must be walled out of the goal area")
	}
	if box.overlapsCircle(opponent.Position, rad) {
		t.Error("keeper-only: an opponent must be walled out of the goal area")
	}
	// Not a teleport: the walled-out outfielder is clamped just OUTSIDE the pitch-facing (max-X)
	// face, its edge resting at the line's outer edge (radius + half the line width), not flush.
	wantX := box.Max.X + rad + boxLineClearance
	if d := ownOutfield.Position.X - wantX; d < -1e-9 || d > 1e-9 {
		t.Errorf("walled-out outfielder X = %g, want a one-tick clamp to %g (just clear of the line)", ownOutfield.Position.X, wantX)
	}
}

// TestGoalAreaNumericCaps: with keeper-only OFF, the numeric goal-area cap still admits exactly
// cap-1 (one) and cap-2 (two) of the owning team's players (keeper kept first), walling out the
// rest -- so cap-1/cap-2 remain selectable via the numeric cap.
func TestGoalAreaNumericCaps(t *testing.T) {
	box := NewStandardField().GoalAreaBox(SideLeft)
	if box.empty() {
		t.Fatal("standard field should have a left goal area")
	}
	for _, cap := range []int{1, 2} {
		m := BuildMatchFromConfig(NewStandardField(), 4, config.Default())
		m.Rules.Enforcement = config.EnforceClamp
		m.Rules.OffsideEnabled = false
		m.Rules.PenaltyBoxMaxPlayers, m.Rules.PenaltyBoxMaxOpponents = 0, 0
		m.Rules.GoalAreaKeeperOnly = false
		m.Rules.GoalAreaMaxPlayers = cap
		m.Rules.GoalAreaMaxOpponents = 0

		var def *Team
		for _, tm := range m.Teams {
			if tm.Side == SideLeft {
				def = tm
			}
		}
		rad := def.Players[0].Radius()
		midY := (box.Min.Y + box.Max.Y) / 2
		// Stack all of the owning team deep inside, each at a slightly different depth so the
		// deepest-kept ranking is deterministic.
		for i, p := range def.Players {
			p.Position = geom.NewVec(box.Min.X+rad*2+float64(i)*0.01, midY)
		}

		enforceZoneRules(m, 1.0/60)

		inside := 0
		for _, p := range def.Players {
			if box.overlapsCircle(p.Position, rad) {
				inside++
			}
		}
		if inside != cap {
			t.Errorf("cap-%d: %d players remained inside the goal area, want %d", cap, inside, cap)
		}
	}
}
