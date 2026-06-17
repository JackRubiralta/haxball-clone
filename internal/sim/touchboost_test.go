package sim

import (
	"math"
	"testing"

	"phootball/internal/config"
	"phootball/internal/geom"
)

// onlyToucher parks every player far away (at distinct, out-of-the-way spots) and then, if a
// holder is given, drops the ball on top of it so it is the SOLE player in contact. With no
// holder the ball is parked away from everyone (a ball in flight). Used to drive the team
// possession charge deterministically through advanceTeamPossession.
func onlyToucher(m *Match, holder *Player) {
	for _, p := range m.Players {
		p.Position = geom.NewVec(-1e5, float64(p.PlayerID)*60)
	}
	if holder != nil {
		holder.Position = geom.NewVec(0, 0)
		m.Ball.Position = geom.NewVec(0, 0) // overlapping -> touching
	} else {
		m.Ball.Position = geom.NewVec(1e5, 0) // far from everyone -> in flight
	}
}

func firstOn(m *Match, side Side) *Player {
	for _, p := range m.Players {
		if p.Team.Side == side {
			return p
		}
	}
	return nil
}

// buildToFull holds the ball with `holder` long enough to drive its team's charge to full.
func buildToFull(m *Match, holder *Player, dt float64) {
	onlyToucher(m, holder)
	for i := 0; i < int(teamBuildSeconds/dt)+5; i++ {
		m.advanceTeamPossession(dt)
	}
}

// TestTeamBuildCurve pins the build ramp: 0->0, 1->1, monotonic, and ACCELERATING (weaker
// than linear early, so the rate increases toward the end).
func TestTeamBuildCurve(t *testing.T) {
	if teamBuildCurve(0) != 0 || teamBuildCurve(1) != 1 {
		t.Errorf("build curve endpoints: got %.3f, %.3f want 0, 1", teamBuildCurve(0), teamBuildCurve(1))
	}
	if teamBuildCurve(0.5) >= 0.5 {
		t.Errorf("build curve should be convex (weak early): teamBuildCurve(0.5)=%.3f", teamBuildCurve(0.5))
	}
	if !(teamBuildCurve(0.25) < teamBuildCurve(0.5) && teamBuildCurve(0.5) < teamBuildCurve(0.75)) {
		t.Errorf("build curve should be monotonic increasing")
	}
}

// TestTeamCoastEnvelope pins the post-release fade: full through the hold, then a smooth convex
// decay to zero by the end -- a gentle fall at first that speeds up toward the end.
func TestTeamCoastEnvelope(t *testing.T) {
	cases := []struct {
		name        string
		coast, want float64
	}{
		{"start of hold", 0, 1},
		{"end of hold", teamHoldSeconds, 1},
		{"decay end", teamDecaySeconds, 0},
		{"past the end", teamDecaySeconds + 1, 0},
	}
	for _, c := range cases {
		if got := teamCoastEnvelope(c.coast); math.Abs(got-c.want) > 1e-9 {
			t.Errorf("teamCoastEnvelope(%s, %.2f) = %.4f, want %.4f", c.name, c.coast, got, c.want)
		}
	}

	mid := (teamHoldSeconds + teamDecaySeconds) / 2
	// Strictly decreasing across the decay window.
	if !(teamCoastEnvelope(teamHoldSeconds) > teamCoastEnvelope(mid) && teamCoastEnvelope(mid) > teamCoastEnvelope(teamDecaySeconds)) {
		t.Errorf("envelope should decrease across the decay window")
	}
	// Convex: the first half of the decay loses less than the second half (slow then fast).
	firstHalf := teamCoastEnvelope(teamHoldSeconds) - teamCoastEnvelope(mid)
	secondHalf := teamCoastEnvelope(mid) - teamCoastEnvelope(teamDecaySeconds)
	if secondHalf <= firstHalf {
		t.Errorf("decay should be slower early, faster late: first half lost %.3f, second half %.3f", firstHalf, secondHalf)
	}
}

// TestTouchQualityMultipliers checks the coefficient -> multiplier mappings: 0 is the baseline
// (1.0), a cleaner touch means more capture and less bounce, a worse touch the reverse.
func TestTouchQualityMultipliers(t *testing.T) {
	tq := DefaultStats(500).TouchQuality
	approx := func(a, b float64) bool { return math.Abs(a-b) < 1e-9 }
	if !approx(tq.captureMul(0), 1) || !approx(tq.restitutionMul(0), 1) {
		t.Errorf("coefficient 0 should be the baseline: capMul=%.4f restMul=%.4f", tq.captureMul(0), tq.restitutionMul(0))
	}
	if !(tq.captureMul(1) > tq.captureMul(0) && tq.captureMul(0) > tq.captureMul(-1)) {
		t.Errorf("captureMul should increase with the coefficient")
	}
	if !(tq.restitutionMul(1) < tq.restitutionMul(0) && tq.restitutionMul(0) < tq.restitutionMul(-1)) {
		t.Errorf("restitutionMul should decrease with the coefficient")
	}
}

// TestTeamChargeShapesContact is the physics proof: the owning team at full charge takes the
// ball cleanly, while the conceding team (and the baseline) bounce it away -- the conceding
// team the most. This is "better touches for my team, the ball flies off the other team".
func TestTeamChargeShapesContact(t *testing.T) {
	const ballRadius = 10
	tq := DefaultStats(500).TouchQuality

	// A head-on front contact; only the touch coefficient varies. Returns ball velocity after.
	contact := func(impact, coef float64) geom.Vec {
		p := NewPlayer(1, geom.NewVec(0, 0), DefaultStats(500), &Team{Side: SideLeft})
		p.Facing = geom.NewVec(1, 0)
		p.touchCoef = coef
		b := NewBall(geom.NewVec(p.Radius()+ballRadius-0.5, 0), ballRadius)
		b.Velocity = geom.NewVec(-impact, 0)
		handleBallToPlayerInteraction(b, p, 1.0/60)
		return b.Velocity
	}

	own := contact(380, tq.OwnTeamMax)
	base := contact(380, 0)
	other := contact(380, tq.OtherTeam)
	if math.Abs(own.X) > 5 {
		t.Errorf("owning team at full charge should capture a 380 ball (vx ~ 0), got %.2f", own.X)
	}
	if !(other.X > base.X && base.X >= 0) {
		t.Errorf("conceding team should bounce a ball further than baseline: other=%.2f base=%.2f", other.X, base.X)
	}

	hardOther := contact(700, tq.OtherTeam)
	hardBase := contact(700, 0)
	if hardOther.X <= hardBase.X {
		t.Errorf("a blocked shot should fly off the conceding team more: other=%.2f base=%.2f", hardOther.X, hardBase.X)
	}
}

// TestTeamChargeInheritedAcrossPass is the headline behaviour: a partial charge built by one
// player is PRESERVED through a pass (held, not decayed) and the receiving teammate CONTINUES
// the build from there -- finishing in the remaining time rather than starting a fresh second.
func TestTeamChargeInheritedAcrossPass(t *testing.T) {
	m := BuildMatchFromConfig(NewStandardField(), 3, config.Default())
	const dt = 1.0 / 60
	var a, b *Player
	for _, p := range m.Players {
		if p.Team.Side == SideLeft {
			if a == nil {
				a = p
			} else {
				b = p
				break
			}
		}
	}

	// A holds the ball long enough to build the team charge to ~0.75 progress.
	buildTo := 0.75
	onlyToucher(m, a)
	for i := 0; i < int(buildTo*teamBuildSeconds/dt); i++ {
		m.advanceTeamPossession(dt)
	}
	if m.possSide != SideLeft {
		t.Fatalf("the left team should own the charge, got %v", m.possSide)
	}
	built := m.possProgress
	if math.Abs(built-buildTo) > 0.03 {
		t.Fatalf("progress should be ~%.2f, got %.3f", buildTo, built)
	}
	heldStrength := m.teamPossessionStrength(SideLeft)

	// A passes: the ball is in flight (nobody touching) for ~0.5s, inside the 1.0s hold
	// window -- the charge is held at strength and the progress is preserved, not decayed.
	onlyToucher(m, nil)
	for i := 0; i < 30; i++ {
		m.advanceTeamPossession(dt)
	}
	if m.possSide != SideLeft {
		t.Fatalf("the charge should survive a 1s pass, owner now %v", m.possSide)
	}
	if math.Abs(m.possProgress-built) > 1e-9 {
		t.Errorf("progress must be preserved in flight: was %.3f, now %.3f", built, m.possProgress)
	}
	if math.Abs(m.teamPossessionStrength(SideLeft)-heldStrength) > 1e-9 {
		t.Errorf("strength should be held during the hold window: was %.3f, now %.3f", heldStrength, m.teamPossessionStrength(SideLeft))
	}

	// Teammate B receives and CONTINUES the build: it should reach full in the remaining
	// (1-built) of the build window, far short of a fresh full build (which would mean it
	// restarted from zero).
	fullBuildTicks := int(teamBuildSeconds / dt)
	onlyToucher(m, b)
	ticks := 0
	for m.possProgress < 1 && ticks < fullBuildTicks+5 {
		m.advanceTeamPossession(dt)
		ticks++
	}
	if ticks >= fullBuildTicks {
		t.Fatalf("the receiver never finished the inherited build (restarted from zero?)")
	}
	// The remaining build is ~(1-built); allow a little slack but it must be well under a full build.
	if want := int((1 - built) * teamBuildSeconds / dt); ticks > want+10 {
		t.Errorf("the receiver should finish the inherited build in ~%d ticks, took %d", want, ticks)
	}
	if m.possSide != SideLeft {
		t.Errorf("ownership should stay with the left team through the pass, got %v", m.possSide)
	}
}

// TestTeamChargeResetByOpponent: the other team touching the ball hands ownership over and
// resets the build (an intercepted possession is not inherited by the thief at full strength).
func TestTeamChargeResetByOpponent(t *testing.T) {
	m := BuildMatchFromConfig(NewStandardField(), 3, config.Default())
	const dt = 1.0 / 60
	left, right := firstOn(m, SideLeft), firstOn(m, SideRight)

	buildToFull(m, left, dt)
	if m.possSide != SideLeft || m.possProgress < 0.99 {
		t.Fatalf("the left team should be fully charged, side=%v progress=%.3f", m.possSide, m.possProgress)
	}

	onlyToucher(m, right)
	m.advanceTeamPossession(dt)
	if m.possSide != SideRight {
		t.Errorf("an opponent touch should hand ownership over, got %v", m.possSide)
	}
	if m.possProgress > 0.05 {
		t.Errorf("the build should reset for the new owner, got %.3f", m.possProgress)
	}
}

// TestTeamChargeExpiresAfterDecayWindow: a released charge holds at full within the hold
// window and is gone once the full decay window of nobody touching it has elapsed.
func TestTeamChargeExpiresAfterDecayWindow(t *testing.T) {
	m := BuildMatchFromConfig(NewStandardField(), 3, config.Default())
	const dt = 1.0 / 60
	left := firstOn(m, SideLeft)

	buildToFull(m, left, dt)
	onlyToucher(m, nil) // release: ball in flight

	for i := 0; i < int((teamHoldSeconds*0.8)/dt); i++ { // inside the hold
		m.advanceTeamPossession(dt)
	}
	if m.possSide != SideLeft || m.teamPossessionStrength(SideLeft) < 0.99 {
		t.Errorf("charge should still be full within the %.1fs hold, side=%v strength=%.3f",
			teamHoldSeconds, m.possSide, m.teamPossessionStrength(SideLeft))
	}
	for i := 0; i < int((teamDecaySeconds+0.3)/dt); i++ { // run well past the full decay window
		m.advanceTeamPossession(dt)
	}
	if m.possSide != SideNone {
		t.Errorf("the charge should expire after %.1fs of no touch, side=%v", teamDecaySeconds, m.possSide)
	}
}

// TestTeamChargeDecaysAndRebuildsOnLateReception is the user's worked example: a full charge
// released and received LATE (1.75s, deep in the decay) lands at ~30%, the receiver inherits
// that decayed coefficient, and rebuilds from there toward full (faster than a fresh second).
func TestTeamChargeDecaysAndRebuildsOnLateReception(t *testing.T) {
	m := BuildMatchFromConfig(NewStandardField(), 3, config.Default())
	const dt = 1.0 / 60
	var a, b *Player
	for _, p := range m.Players {
		if p.Team.Side == SideLeft {
			if a == nil {
				a = p
			} else {
				b = p
				break
			}
		}
	}

	// A builds the charge to full.
	buildToFull(m, a, dt)
	if m.possProgress < 0.999 {
		t.Fatalf("the charge should be fully built, got progress %.3f", m.possProgress)
	}

	// Release and let the ball fly into the middle of the decay window -- partly decayed, not gone.
	onlyToucher(m, nil)
	mid := (teamHoldSeconds + teamDecaySeconds) / 2
	for i := 0; i < int(mid/dt); i++ {
		m.advanceTeamPossession(dt)
	}
	decayed := m.teamPossessionStrength(SideLeft)
	if !(decayed > 0.05 && decayed < 0.95) {
		t.Fatalf("strength mid-decay should be partial (decayed but not gone), got %.3f", decayed)
	}

	// Teammate B receives. The contact this tick sees the DECAYED coefficient, and the charge is
	// re-based to that strength so it rebuilds from there -- not from full, and not from zero.
	onlyToucher(m, b)
	m.advanceTeamPossession(dt)
	if math.Abs(b.touchCoef-decayed) > 0.03 {
		t.Errorf("the receiver should take the ball at the decayed coefficient ~%.2f, got %.3f", decayed, b.touchCoef)
	}
	if want := teamBuildCurveInv(decayed); math.Abs(m.possProgress-want) > 0.05 {
		t.Errorf("the charge should re-base to progress ~%.3f (the decayed strength), got %.3f", want, m.possProgress)
	}

	// It rebuilds to full from the decayed point -- in well under a fresh second.
	ticks := 1
	for m.possProgress < 1 && ticks < 60 {
		m.advanceTeamPossession(dt)
		ticks++
	}
	if ticks >= 55 {
		t.Errorf("rebuild from a decayed charge should beat a fresh second, took %d ticks", ticks)
	}
}

// TestTeamChargeResets covers the kickoff and shootout reset paths.
func TestTeamChargeResets(t *testing.T) {
	const dt = 1.0 / 60

	m := BuildMatchFromConfig(NewStandardField(), 3, config.Default())
	onlyToucher(m, firstOn(m, SideLeft))
	for i := 0; i < 30; i++ {
		m.advanceTeamPossession(dt)
	}
	if m.possSide == SideNone {
		t.Fatalf("precondition: a charge should have built")
	}
	m.resetKickoff()
	if m.possSide != SideNone || m.possProgress != 0 {
		t.Errorf("resetKickoff should drop the charge, side=%v progress=%.3f", m.possSide, m.possProgress)
	}
	for _, p := range m.Players {
		if p.touchCoef != 0 {
			t.Errorf("resetKickoff should zero player %d's coefficient, got %.3f", p.PlayerID, p.touchCoef)
		}
	}

	m2 := BuildMatchFromConfig(NewStandardField(), 3, config.Default())
	onlyToucher(m2, firstOn(m2, SideRight))
	for i := 0; i < 30; i++ {
		m2.advanceTeamPossession(dt)
	}
	m2.beginShootout()
	if m2.possSide != SideNone {
		t.Errorf("beginShootout should drop the charge, side=%v", m2.possSide)
	}
}

// TestTeamChargeThroughStep locks the real pipeline: a player holding the ball through Step
// builds its team's charge, gives its team a positive touch coefficient, and leaves the other
// team non-positive (so a shot would fly off them).
func TestTeamChargeThroughStep(t *testing.T) {
	m := BuildMatchFromConfig(NewStandardField(), 3, config.Default())
	const dt = 1.0 / 60
	carrier := firstOn(m, SideLeft)
	carrier.Facing = geom.NewVec(1, 0)
	m.Ball.Position = carrier.Position.Add(geom.NewVec(carrier.Radius()+m.Ball.Radius()-0.5, 0))
	m.Ball.Velocity = geom.Vec{}

	for i := 0; i < 40; i++ {
		m.Step(map[int]Intent{}, dt)
	}

	if m.PossessionCharge(SideLeft) <= 0 {
		t.Errorf("the left team should build a charge through Step while holding the ball, got %.3f",
			m.PossessionCharge(SideLeft))
	}
	if carrier.TouchCoefficient() <= 0 {
		t.Errorf("the carrier's coefficient should be positive, got %.3f", carrier.TouchCoefficient())
	}
	for _, p := range m.Players {
		if p.Team.Side == SideRight && p.TouchCoefficient() > 0 {
			t.Errorf("the conceding team's coefficient should be non-positive, player %d got %.3f",
				p.PlayerID, p.TouchCoefficient())
		}
	}
}
