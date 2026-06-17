package sim

import (
	"math"
	"testing"

	"phootball/internal/config"
	"phootball/internal/geom"
)

// TestPossessionInPullRange verifies the player-possession (not team) reaches into the pull
// radius: a player builds/holds possession while the ball is merely within its (trap-extended)
// pull radius without touching, a trap extends that reach, and a pull-range contest steals
// conservatively -- every drain on the holder is matched by an equal gain on the taker.
func TestPossessionInPullRange(t *testing.T) {
	const dt = 1.0 / 60

	parkAll := func(m *Match) {
		for _, q := range m.Players {
			q.Position = geom.NewVec(-1e5, float64(q.PlayerID)*60)
		}
	}

	// (1) Possession BUILDS from pull range alone -- ball near but not touching.
	{
		m := BuildMatchFromConfig(NewStandardField(), 3, config.Default())
		p := firstOn(m, SideLeft)
		parkAll(m)
		p.Position = geom.NewVec(0, 0)
		mid := (p.Stats.TouchRange + p.pullRadius()) / 2 // gap strictly between touch and pull
		p.possession = 0
		m.Ball.Position = geom.NewVec(p.Radius()+m.Ball.Radius()+mid, 0)
		if m.touching(p) {
			t.Fatalf("setup: ball should NOT be touching (gap %.2f, TouchRange %.2f)", mid, p.Stats.TouchRange)
		}
		if !m.inPullRange(p) {
			t.Fatalf("setup: ball should be within the pull radius (gap %.2f, pullRadius %.2f)", mid, p.pullRadius())
		}
		for i := 0; i < 60; i++ {
			m.advancePossessionBuilder()
			updatePossession(m.Ball, p, dt, p == m.possBuilder)
		}
		if !(p.possession > 0) {
			t.Errorf("possession should build while the ball is in the pull radius without touching, got %.3f", p.possession)
		}
	}

	// (2) A charged trap EXTENDS the reach: a ball just past the untrapped pull radius still
	// counts as possessed once the trap is charged.
	{
		m := BuildMatchFromConfig(NewStandardField(), 3, config.Default())
		p := firstOn(m, SideLeft)
		parkAll(m)
		p.Position = geom.NewVec(0, 0)
		justBeyondBase := p.Stats.PullRange + 1 // gap just past the untrapped pull radius
		m.Ball.Position = geom.NewVec(p.Radius()+m.Ball.Radius()+justBeyondBase, 0)
		p.trapCharge = 0
		if m.inPullRange(p) {
			t.Fatalf("setup: ball should be beyond the untrapped pull radius")
		}
		p.trapCharge = 1
		if !m.inPullRange(p) {
			t.Errorf("a charged trap should extend the pull radius to reach the ball (gap %.2f, trapped pullRadius %.2f)", justBeyondBase, p.pullRadius())
		}
	}

	// (3) A pull-range contest steals possession conservatively, neither player touching: the
	// holder's loss equals the taker's gain (nothing created or destroyed).
	{
		m := BuildMatchFromConfig(NewStandardField(), 3, config.Default())
		h, c := firstOn(m, SideLeft), firstOn(m, SideRight)
		parkAll(m)
		gap := (h.Stats.TouchRange + h.pullRadius()) / 2
		surface := h.Radius() + m.Ball.Radius()
		h.Position = geom.NewVec(0, 0)
		m.Ball.Position = geom.NewVec(surface+gap, 0)
		c.Position = geom.NewVec(2*(surface+gap), 0) // mirror -> same gap on the far side
		h.possession, c.possession = 0.8, 0.1
		m.possessor = h
		if m.touching(h) || m.touching(c) {
			t.Fatalf("setup: neither player should be touching the ball")
		}
		if !m.inPullRange(h) || !m.inPullRange(c) {
			t.Fatalf("setup: both players should have the ball in pull range")
		}
		before := h.possession + c.possession
		m.updateBallPossessor(dt)
		if !(h.possession < 0.8 && c.possession > 0.1) {
			t.Errorf("a pull-range contest should drain the holder into the challenger: h=%.4f c=%.4f", h.possession, c.possession)
		}
		if after := h.possession + c.possession; math.Abs(after-before) > 1e-9 {
			t.Errorf("possession must be conserved (drain == gain): before %.4f after %.4f", before, after)
		}
	}
}

// TestPossessionBuildsForLatestEntrantOnly: when the ball is in two players' pull radii at once,
// only the LATEST player it reached builds possession -- the earlier one stops and decays. A
// carrier building possession is overtaken the moment an opponent gets the ball into its reach.
func TestPossessionBuildsForLatestEntrantOnly(t *testing.T) {
	const dt = 1.0 / 60
	m := BuildMatchFromConfig(NewStandardField(), 3, config.Default())
	a, b := firstOn(m, SideLeft), firstOn(m, SideRight)
	for _, q := range m.Players {
		q.Position = geom.NewVec(-1e5, float64(q.PlayerID)*60) // park everyone far
	}
	surface := a.Radius() + m.Ball.Radius()
	gap := (a.Stats.TouchRange + a.pullRadius()) / 2 // strictly between touch and pull

	tick := func() {
		m.advancePossessionBuilder()
		updatePossession(m.Ball, a, dt, a == m.possBuilder)
		updatePossession(m.Ball, b, dt, b == m.possBuilder)
	}

	// Phase 1: A alone has the ball in reach (B still parked) -> A is the sole builder.
	a.Position = geom.NewVec(0, 0)
	m.Ball.Position = geom.NewVec(surface+gap, 0)
	a.possession, b.possession = 0, 0
	for i := 0; i < 60; i++ {
		tick()
	}
	if m.possBuilder != a || !(a.possession > 0.3) {
		t.Fatalf("A alone should build possession (builder=%v, a=%.3f)", m.possBuilder, a.possession)
	}
	aBefore := a.possession

	// Phase 2: B arrives so the ball is ALSO in B's pull radius -- B entered most recently, so it
	// becomes the sole builder; A stops building and decays even though the ball is still in reach.
	b.Position = m.Ball.Position.Add(geom.NewVec(surface+gap, 0)) // mirror of A: same gap on the far side
	if !m.inPullRange(a) || !m.inPullRange(b) {
		t.Fatalf("setup: the ball should be in BOTH players' pull radius")
	}
	for i := 0; i < 60; i++ {
		tick()
	}
	if m.possBuilder != b {
		t.Errorf("the latest entrant (B) should be the possession builder, got %v", m.possBuilder)
	}
	if !(b.possession > 0) {
		t.Errorf("the latest entrant (B) should build possession, got %.3f", b.possession)
	}
	if !(a.possession < aBefore) {
		t.Errorf("the earlier player (A) should stop building and decay once B arrives: was %.3f, now %.3f", aBefore, a.possession)
	}

	// Phase 3: B leaves -- the build falls back to A, the only one still in reach.
	b.Position = geom.NewVec(-1e5, 999)
	for i := 0; i < 10; i++ {
		tick()
	}
	if m.possBuilder != a {
		t.Errorf("after B leaves, the build should fall back to A (still in reach), got %v", m.possBuilder)
	}
}
