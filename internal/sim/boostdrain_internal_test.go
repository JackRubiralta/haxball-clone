package sim

import (
	"testing"

	"phootball/internal/config"
	"phootball/internal/geom"
)

// TestBoostDrainOnlyDrainsContactedPlayer: when a player on the boosted team is body-touched by
// an opponent (even with the ball nowhere near it), only THAT player's team-possession boost
// erodes -- an uncontacted team-mate keeps the full boost and the team charge is untouched -- and
// it recovers once the opponent leaves.
func TestBoostDrainOnlyDrainsContactedPlayer(t *testing.T) {
	m := BuildMatchFromConfig(NewStandardField(), 3, config.Default())
	const dt = 1.0 / 60

	// Left team owns a full team-possession charge.
	m.possSide, m.possProgress, m.possCoast = SideLeft, 1, 0

	var a1, a2, opp *Player // a1: contacted left player (off the ball); a2: free left team-mate; opp: right player
	for _, p := range m.Players {
		if p.Team.Side == SideLeft {
			if a1 == nil {
				a1 = p
			} else if a2 == nil {
				a2 = p
			}
		} else if opp == nil {
			opp = p
		}
	}

	// Park everyone apart. Keep the charge full by having the FREE team-mate (a2) hold the ball;
	// the contacted player (a1) is far from the ball, overlapped by an opponent (a body-check).
	for _, p := range m.Players {
		p.Position = geom.NewVec(-1e5, float64(p.PlayerID)*60)
	}
	a2.Position = geom.NewVec(500, 0)
	m.Ball.Position = a2.Position // a2 touches the ball -> left team keeps the charge at full
	a1.Position = geom.NewVec(0, 0)
	opp.Position = geom.NewVec(0, 0) // overlapping a1 -> opponent body contact (a1 is NOT on the ball)

	if m.touching(a1) {
		t.Fatalf("test setup: a1 should not be touching the ball")
	}

	for i := 0; i < 60; i++ { // ~1s of contact
		m.advanceTeamPossession(dt)
	}

	full := a2.Stats.TouchQuality.OwnTeamMax * m.teamPossessionStrength(SideLeft)
	if !(a1.touchCoef < full*0.25) {
		t.Errorf("the contacted boosted player should have its boost drained: a1=%.3f (full=%.3f)", a1.touchCoef, full)
	}
	if !(a2.touchCoef > full*0.99) {
		t.Errorf("an uncontacted team-mate should keep the full boost: a2=%.3f (full=%.3f)", a2.touchCoef, full)
	}
	if m.possProgress != 1 {
		t.Errorf("the per-player drain must NOT change the team charge: possProgress=%.3f", m.possProgress)
	}

	// Opponent leaves: a1's boost recovers.
	opp.Position = geom.NewVec(-1e5, 0)
	for i := 0; i < 120; i++ { // ~2s
		m.advanceTeamPossession(dt)
	}
	if !(a1.touchCoef > full*0.99) {
		t.Errorf("a1's boost should recover once the opponent leaves: a1=%.3f (full=%.3f)", a1.touchCoef, full)
	}
}
