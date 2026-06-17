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

// bothTouch parks everyone, then places leftP and rightP both overlapping the ball, so
// touchingSides reports a CONTESTED ball (both teams touching at once).
func bothTouch(m *Match, leftP, rightP *Player) {
	for _, p := range m.Players {
		p.Position = geom.NewVec(-1e5, float64(p.PlayerID)*60)
	}
	leftP.Position = geom.NewVec(0, 0)
	rightP.Position = geom.NewVec(0, 0)
	m.Ball.Position = geom.NewVec(0, 0)
}

// TestPossessionContestGradualTransfer: while the holder and a challenger are BOTH on the ball,
// possession flows gradually from the holder to the challenger, and a sustained contest hands
// the ball over once the challenger holds more.
func TestPossessionContestGradualTransfer(t *testing.T) {
	m := BuildMatchFromConfig(NewStandardField(), 3, config.Default())
	const dt = 1.0 / 60
	a, b := firstOn(m, SideLeft), firstOn(m, SideRight)
	a.possession = 0.8
	m.possessor = a

	bothTouch(m, a, b) // a (holder) and b (challenger) both in contact

	// One tick: the challenger gains, the holder loses, but the holder still holds (only a sliver moved).
	m.updateBallPossessor(dt)
	if !(b.possession > 0 && a.possession < 0.8) {
		t.Fatalf("a contest should start transferring possession: a=%.3f b=%.3f", a.possession, b.possession)
	}
	if m.possessor != a {
		t.Errorf("after one tick the holder should still hold, got %v", m.possessor)
	}

	// Sustained contest: the challenger eventually wins the ball.
	for i := 0; i < 120 && m.possessor != b; i++ {
		m.updateBallPossessor(dt)
	}
	if m.possessor != b {
		t.Errorf("a sustained contest should hand the ball to the challenger, holder still %v", m.possessor)
	}
	if !(b.possession > a.possession) {
		t.Errorf("after winning the challenger should hold the larger share: a=%.3f b=%.3f", a.possession, b.possession)
	}
}

// TestPossessionCleanTakeover: when the holder is OFF the ball, the nearest toucher simply takes
// over with no transfer (there is no one to contest).
func TestPossessionCleanTakeover(t *testing.T) {
	m := BuildMatchFromConfig(NewStandardField(), 3, config.Default())
	a, b := firstOn(m, SideLeft), firstOn(m, SideRight)
	a.possession = 0.8
	m.possessor = a

	onlyToucher(m, b) // a off the ball, b on it
	m.updateBallPossessor(1.0 / 60)

	if m.possessor != b {
		t.Errorf("the nearest toucher should take over when the holder is off the ball, got %v", m.possessor)
	}
	if b.possession != 0 {
		t.Errorf("a clean takeover (no contest) should transfer nothing, got b=%.3f", b.possession)
	}
}

// TestPossessionControlBonusAndCone is a cheap pin on two tuning values: the per-player Control
// boost reaches x1.09 at full possession, and the role preset mirrors the widened capture cone.
func TestPossessionControlBonusAndCone(t *testing.T) {
	s := DefaultStats(500)
	if mul := 1 + s.PossessionControlBonus*1; math.Abs(mul-1.09) > 1e-9 {
		t.Errorf("Control multiplier at full possession = %.4f, want 1.09", mul)
	}
	if r := fieldPlayerStats(); math.Abs(r.CaptureConeRadians-s.CaptureConeRadians) > 1e-9 {
		t.Errorf("role cone %.6f should match DefaultStats %.6f", r.CaptureConeRadians, s.CaptureConeRadians)
	}
}

// TestTeamChargeConeScaling: the capture cone scales asymmetrically with the team-possession
// coefficient -- biggest with the buff, baseline without, and WAY smaller with the debuff (so a
// debuffed opponent catches far less). Never negative.
func TestTeamChargeConeScaling(t *testing.T) {
	s := DefaultStats(500)
	base := s.CaptureConeRadians
	buff := s.captureConeRadians(1) // owning team, full charge
	neutral := s.captureConeRadians(0)
	debuff := s.captureConeRadians(-1) // conceding team, full enemy charge

	if math.Abs(neutral-base) > 1e-9 {
		t.Errorf("a neutral coefficient should leave the cone unchanged: %.5f vs %.5f", neutral, base)
	}
	// Ordering: buff > neutral > debuff.
	if !(buff > neutral && neutral > debuff) {
		t.Errorf("cone should be biggest with buff, then neutral, then debuff: buff=%.4f neutral=%.4f debuff=%.4f", buff, neutral, debuff)
	}
	// The debuff must shrink the cone WAY more than the buff grows it (asymmetric), and to a
	// small fraction of the baseline.
	grow := buff - neutral
	shrink := neutral - debuff
	if !(shrink > 3*grow) {
		t.Errorf("the debuff should shrink the cone much more than the buff grows it: grow=%.4f shrink=%.4f", grow, shrink)
	}
	if !(debuff < base*0.5) {
		t.Errorf("a fully-debuffed cone should be way smaller than baseline (< half): %.4f vs base %.4f", debuff, base)
	}
	if s.captureConeRadians(-100) < 0 {
		t.Errorf("the cone must never go negative")
	}
}

// TestPossessionNotStolenAfterPass: a passed ball carries no possession (shoot zeros it), so a
// receiving player gets no head start from the passer.
func TestPossessionNotStolenAfterPass(t *testing.T) {
	m := BuildMatchFromConfig(NewStandardField(), 3, config.Default())
	a, b := firstOn(m, SideLeft), firstOn(m, SideRight)
	a.possession = 0 // a "passed" -- shoot() reset its possession to 0
	m.possessor = a
	onlyToucher(m, b)
	m.updateBallPossessor(1.0 / 60)
	if b.possession != 0 {
		t.Errorf("a received ball carrying no possession should give no head start, got %.3f", b.possession)
	}
}

// TestTeamChargeDrainedByChallenge: an opposing-player collision that involves the ball CARRIER
// DRAINS the team possession charge over time (it does not reset it), while an off-ball
// collision leaves it untouched.
func TestTeamChargeDrainedByChallenge(t *testing.T) {
	m := BuildMatchFromConfig(NewStandardField(), 3, config.Default())
	const dt = 1.0 / 60
	l, r := firstOn(m, SideLeft), firstOn(m, SideRight)
	park := func() {
		for _, p := range m.Players {
			p.Position = geom.NewVec(-1e5, float64(p.PlayerID)*60)
		}
	}
	challenge := func() { // L holds the ball; R overlaps L (and the ball) -- an opposing challenge
		park()
		m.Ball.Position = m.Field.CenterSpot
		l.Position = m.Field.CenterSpot
		r.Position = m.Field.CenterSpot.Add(geom.NewVec(10, 0))
	}

	// One tick of challenge DRAINS the charge a little -- it does NOT reset, and ownership stays.
	m.possSide, m.possProgress = SideLeft, 1
	challenge()
	m.resolveInteractions(dt)
	if m.possSide != SideLeft {
		t.Errorf("a single challenge tick should not hand ownership over, side=%v", m.possSide)
	}
	if !(m.possProgress < 1 && m.possProgress > 0) {
		t.Errorf("a challenge should DRAIN the charge a little, got progress=%.4f", m.possProgress)
	}

	// A sustained challenge drains it to empty (re-overlapping each tick, since Resolve separates them).
	for i := 0; i < 200 && m.possProgress > 0; i++ {
		challenge()
		m.resolveInteractions(dt)
	}
	if m.possProgress > 0.01 {
		t.Errorf("a sustained challenge should drain the charge to ~0, got %.4f", m.possProgress)
	}

	// Off-ball collision: L and R overlap each other far from the ball -> no drain.
	m.possSide, m.possProgress = SideLeft, 1
	park()
	m.Ball.Position = m.Field.CenterSpot
	l.Position = geom.NewVec(50000, 0)
	r.Position = geom.NewVec(50010, 0) // overlap each other, nowhere near the ball
	m.resolveInteractions(dt)
	if m.possProgress != 1 {
		t.Errorf("an off-ball collision should not drain the charge, got progress=%.4f", m.possProgress)
	}
}

// TestPossessionGripSplit pins the directional design of player possession on the two hold
// forces: it changes the centre-pull grip only mildly (rising to 1), and slightly REDUCES the
// stickiness grip (a tiny debuff at full possession).
func TestPossessionGripSplit(t *testing.T) {
	s := DefaultStats(500)
	if got := s.centerPullGrip(0); math.Abs(got-s.CenterPullGripFloor) > 1e-9 {
		t.Errorf("centerPullGrip(0) = %.3f, want floor %.3f", got, s.CenterPullGripFloor)
	}
	if got := s.centerPullGrip(1); math.Abs(got-1) > 1e-9 {
		t.Errorf("centerPullGrip(1) = %.3f, want 1", got)
	}
	if swing := s.centerPullGrip(1) - s.centerPullGrip(0); swing > 0.5 {
		t.Errorf("possession should change the centre-pull only mildly (swing < the old 0.7), got %.3f", swing)
	}
	if got := s.stickinessGrip(0); math.Abs(got-1) > 1e-9 {
		t.Errorf("stickinessGrip(0) = %.3f, want 1", got)
	}
	if full := s.stickinessGrip(1); !(full < 1 && full > 0.9) {
		t.Errorf("stickinessGrip(1) should be a tiny debuff just below 1, got %.3f", full)
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
	s := DefaultStats(500)
	tq := s.TouchQuality

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

	// An impact midway between the baseline front capture (cone=1, coef 0) and the boosted
	// capture (×CaptureBest): the owning team at full charge absorbs it, baseline/conceding
	// bounce it. Derived from CaptureBest so it tracks retuning of the buff.
	mid := s.CaptureSpeed.Front * (1 + (tq.CaptureBest-1)*0.5)
	own := contact(mid, tq.OwnTeamMax)
	base := contact(mid, 0)
	other := contact(mid, tq.OtherTeam)
	if math.Abs(own.X) > 5 {
		t.Errorf("owning team at full charge should capture a %.0f ball (vx ~ 0), got %.2f", mid, own.X)
	}
	if !(other.X > base.X && base.X >= 0) {
		t.Errorf("conceding team should bounce a ball further than baseline: other=%.2f base=%.2f", other.X, base.X)
	}

	// A hard contact, well above any capture threshold: bounces off both, but more off the
	// conceding team.
	hard := s.CaptureSpeed.Front * 2.5
	hardOther := contact(hard, tq.OtherTeam)
	hardBase := contact(hard, 0)
	if hardOther.X <= hardBase.X {
		t.Errorf("a blocked shot should fly off the conceding team more: other=%.2f base=%.2f", hardOther.X, hardBase.X)
	}
}

// TestMaxShotNotCapturedByDebuffedOpponent: a full-power shot, dead-on, is NOT captured by a
// debuffed opponent (the conceding team when the shooter holds the possession boost) -- it
// deflects off rather than being caught, even if that opponent is fully trapping.
func TestMaxShotNotCapturedByDebuffedOpponent(t *testing.T) {
	const ballRadius = 10
	s := DefaultStats(500)
	maxShot := s.Shoot.Eval(0) // full-charge front shot power (the ball's launch speed)
	coef := s.TouchQuality.OtherTeam

	contact := func(trap float64) (geom.Vec, float64) {
		p := NewPlayer(1, geom.NewVec(0, 0), DefaultStats(500), &Team{Side: SideRight})
		p.Facing = geom.NewVec(1, 0) // facing the incoming ball: dead-on, inside the cone
		p.touchCoef = coef           // debuffed (the conceding team)
		p.trapCharge = trap
		b := NewBall(geom.NewVec(p.Radius()+ballRadius-0.5, 0), ballRadius)
		b.Velocity = geom.NewVec(-maxShot, 0) // straight at the player at the max shot speed
		_, bounce := handleBallToPlayerInteraction(b, p, 1.0/60)
		return b.Velocity, bounce
	}

	// Dead-on, no trap: not a soft capture (bounce > 0), and the ball deflects away.
	vel, bounce := contact(0)
	if bounce <= 0 {
		t.Errorf("a max shot should NOT be captured by a debuffed opponent (should bounce), bounce=%.1f", bounce)
	}
	if vel.X <= 0 {
		t.Errorf("the ball should deflect off the opponent (move away), got vx=%.1f", vel.X)
	}

	// Even a fully-trapping debuffed opponent cannot capture it (the shot beats the trapped capture).
	if _, bounceTrap := contact(1); bounceTrap <= 0 {
		t.Errorf("even a full-trapping debuffed opponent should not capture a max shot, bounce=%.1f", bounceTrap)
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

	// A passes: the ball is in flight (nobody touching) for part of the hold window, so the
	// charge is held at strength and the progress is preserved, not decayed.
	onlyToucher(m, nil)
	for i := 0; i < int(0.5*teamHoldSeconds/dt); i++ {
		m.advanceTeamPossession(dt)
	}
	if m.possSide != SideLeft {
		t.Fatalf("the charge should survive a pass within the hold window, owner now %v", m.possSide)
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

// TestTeamChargeContestedReset: when both teams touch the ball at once, the charge clears
// entirely (nobody gets a clean possession out of a scramble).
func TestTeamChargeContestedReset(t *testing.T) {
	m := BuildMatchFromConfig(NewStandardField(), 3, config.Default())
	const dt = 1.0 / 60
	left, right := firstOn(m, SideLeft), firstOn(m, SideRight)

	buildToFull(m, left, dt)
	if m.possSide != SideLeft {
		t.Fatalf("precondition: the left team should own the charge, got %v", m.possSide)
	}

	bothTouch(m, left, right)
	m.advanceTeamPossession(dt)
	if m.possSide != SideNone || m.possProgress != 0 {
		t.Errorf("a contested ball should clear the charge, side=%v progress=%.3f", m.possSide, m.possProgress)
	}
	for _, p := range m.Players {
		if p.touchCoef != 0 {
			t.Errorf("a contested reset should zero coefficients, player %d = %.3f", p.PlayerID, p.touchCoef)
		}
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

// TestTeamChargeDecaysAndRebuildsOnLateReception is the user's worked example, generalised: a
// full charge released and received LATE (mid-decay, partly decayed) is taken at the decayed
// coefficient, the receiver inherits it, and rebuilds from there toward full -- far faster
// than a fresh full build.
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

	// It rebuilds to full from the decayed point -- in well under a fresh full build.
	freshTicks := int(teamBuildSeconds / dt)
	ticks := 1
	for m.possProgress < 1 && ticks < freshTicks+5 {
		m.advanceTeamPossession(dt)
		ticks++
	}
	if ticks >= freshTicks {
		t.Errorf("rebuild from a decayed charge should beat a fresh full build (%d ticks), took %d", freshTicks, ticks)
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
