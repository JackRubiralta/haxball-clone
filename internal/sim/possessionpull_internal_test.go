package sim

import (
	"testing"

	"phootball/internal/config"
	"phootball/internal/geom"
)

// TestPossessionInPullRange verifies the player-possession (not team) reaches into the pull
// radius: a player builds/holds possession while the ball is merely within its BASE pull radius
// without touching, a trap does NOT extend that reach (the trap extends only the ball
// attraction), and a pull-range contest steals the displaced holder's possession.
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
			updatePossession(m.Ball, p, dt, p == m.possBuilder, false)
		}
		if !(p.possession > 0) {
			t.Errorf("possession should build while the ball is in the pull radius without touching, got %.3f", p.possession)
		}
	}

	// (2) A charged trap does NOT extend the POSSESSION reach: a ball just past the base pull
	// radius stays out of possession reach even at full trap. The trap extends only the ball
	// ATTRACTION (the centre-pull in handleBallToPlayerInteraction), never who builds/contests
	// possession -- so trapping cannot warp the possession radius.
	{
		m := BuildMatchFromConfig(NewStandardField(), 3, config.Default())
		p := firstOn(m, SideLeft)
		parkAll(m)
		p.Position = geom.NewVec(0, 0)
		justBeyondBase := p.Stats.PullRange + 1 // gap just past the base pull radius
		m.Ball.Position = geom.NewVec(p.Radius()+m.Ball.Radius()+justBeyondBase, 0)
		p.trapAura = 0
		if m.inPullRange(p) {
			t.Fatalf("setup: ball should be beyond the base pull radius")
		}
		p.trapAura = 1 // trap at peak strength -- must NOT change the possession reach
		if m.inPullRange(p) {
			t.Errorf("a charged trap must NOT extend the possession pull radius (gap %.2f, base PullRange %.2f) -- the trap extends only the ball attraction", justBeyondBase, p.Stats.PullRange)
		}
	}

	// (3) A pull-range contest, neither player touching: the challenger (the latest with the ball
	// in reach) BUILDS while the displaced holder falls away -- the steal works. Drain and gain
	// are decoupled now (denial vs acquisition), so it is not conserved.
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
		possessionTick(m, dt)
		if !(h.possession < 0.8 && c.possession > 0.1) {
			t.Errorf("a pull-range contest should drain the holder and build the challenger: h=%.4f c=%.4f", h.possession, c.possession)
		}
	}
}

// TestPossessionRangeDecoupledFromPull verifies PossessionRange is an INDEPENDENT knob from the
// attraction base PullRange: it defaults to the same value (so possession reach == attraction base
// today), it is NEVER trap-extended, tuning it changes ONLY the possession reach (not the ball
// attraction), and a value <= 0 falls back to PullRange.
func TestPossessionRangeDecoupledFromPull(t *testing.T) {
	m := BuildMatchFromConfig(NewStandardField(), 3, config.Default())
	p := firstOn(m, SideLeft)
	for _, q := range m.Players {
		q.Position = geom.NewVec(-1e5, float64(q.PlayerID)*60)
	}
	p.Position = geom.NewVec(0, 0)

	// Default: PossessionRange seeds equal to PullRange, so reach is identical to before.
	if p.Stats.PossessionRange != p.Stats.PullRange {
		t.Fatalf("default PossessionRange (%.2f) should equal PullRange (%.2f)", p.Stats.PossessionRange, p.Stats.PullRange)
	}
	if got := p.possessionRadius(); got != p.Stats.PullRange {
		t.Errorf("possessionRadius should default to PullRange: got %.2f want %.2f", got, p.Stats.PullRange)
	}

	// A full trap must NOT extend the possession reach, but MUST still extend the attraction reach.
	p.trapAura = 1
	if got := p.possessionRadius(); got != p.Stats.PossessionRange {
		t.Errorf("a full trap must not change possessionRadius: got %.2f want %.2f", got, p.Stats.PossessionRange)
	}
	if !(p.pullRadius() > p.possessionRadius()) {
		t.Errorf("a full trap should extend the ATTRACTION pullRadius (%.2f) beyond the possession reach (%.2f)", p.pullRadius(), p.possessionRadius())
	}
	p.trapAura = 0

	// Decoupling: shrink PossessionRange below PullRange. A ball between the two is OUT of
	// possession reach (inPullRange false) yet still inside the attraction base (pullRadius).
	p.Stats.PossessionRange = 2
	surface := p.Radius() + m.Ball.Radius()
	gap := (p.Stats.PossessionRange + p.Stats.PullRange) / 2 // 3.5: inside Pull(5), outside Possession(2)
	m.Ball.Position = geom.NewVec(surface+gap, 0)
	if m.inPullRange(p) {
		t.Errorf("ball at gap %.2f should be OUTSIDE the shrunk possession reach %.2f", gap, p.Stats.PossessionRange)
	}
	if !(gap < p.pullRadius()) {
		t.Errorf("ball at gap %.2f should still be inside the attraction pull radius %.2f (decoupled)", gap, p.pullRadius())
	}

	// Fallback: PossessionRange <= 0 means "use PullRange".
	p.Stats.PossessionRange = 0
	if got := p.possessionRadius(); got != p.Stats.PullRange {
		t.Errorf("PossessionRange<=0 should fall back to PullRange: got %.2f want %.2f", got, p.Stats.PullRange)
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
		updatePossession(m.Ball, a, dt, a == m.possBuilder, false)
		updatePossession(m.Ball, b, dt, b == m.possBuilder, false)
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
