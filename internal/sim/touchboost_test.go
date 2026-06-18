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

// possessionTick runs one full player-possession tick exactly as Match.Step does: choose the
// builder, then build/drain/decay each player, then update who holds the ball. The sole builder
// (latest with the ball in reach) gains; a holder marked by an opponent that is NOT near the ball
// drains (denial); everyone else decays.
func possessionTick(m *Match, dt float64) {
	m.advancePossessionBuilder()
	for _, p := range m.Players {
		drain := p == m.possessor && m.markedByNonBallOpponent(p)
		build := !drain && p == m.possBuilder
		updatePossession(m.Ball, p, dt, build, drain)
	}
	m.updateBallPossessor(dt)
}

// TestPossessionStealFromPullRange: a challenger that is NOT touching the ball but has it within
// its pull radius can still contest/steal the holder's possession, and the trap extends that
// reach (a gap out of the base pull radius becomes reachable while trapping).
func TestPossessionStealFromPullRange(t *testing.T) {
	m := BuildMatchFromConfig(NewStandardField(), 3, config.Default())
	const dt = 1.0 / 60
	a, b := firstOn(m, SideLeft), firstOn(m, SideRight)

	// setup: a holds the ball (touching, possession 0.8); b sits at surface gap `gap` from the
	// ball with trap charge `trap`. Everyone else is parked far away.
	surface := a.Radius() + m.Ball.Radius()
	setup := func(gap, trap float64) {
		for _, p := range m.Players {
			p.Position = geom.NewVec(-1e5, float64(p.PlayerID)*60)
		}
		m.Ball.Position = geom.NewVec(0, 0)
		// a holds from pull range on the LEFT (in reach, not touching the ball); b sits `gap` from
		// the ball on the RIGHT. They are on opposite sides, far enough apart NOT to touch each
		// other, so this isolates the PULL-RANGE steal from the body-contact one.
		holdGap := (a.Stats.TouchRange + a.Stats.PullRange) / 2
		a.Position = geom.NewVec(-(surface + holdGap), 0)
		a.possession, a.control = 0.8, 0
		b.possession, b.control, b.trapAura = 0, 0, trap // trapAura is the effective trap strength driving the reach
		b.Position = geom.NewVec(surface+gap, 0)
		m.possessor = a
	}

	// 1. In the base pull radius but NOT touching: the challenger (latest with the ball in reach)
	// builds while the displaced holder falls away -- the steal works.
	setup(3, 0) // gap 3: >= TouchRange (not touching), < PullRange (in pull radius)
	if m.touching(b) {
		t.Fatalf("test setup: b should not be touching at gap 3")
	}
	possessionTick(m, dt)
	if !(b.possession > 0 && a.possession < 0.8) {
		t.Errorf("a challenger in pull range (not touching) should steal possession: a=%.3f b=%.3f", a.possession, b.possession)
	}

	// 2. Beyond the base pull radius with no trap: no steal -- the holder keeps/builds its own.
	setup(8, 0)
	if m.inPullRange(b) {
		t.Fatalf("test setup: gap 8 should be beyond the base pull radius")
	}
	possessionTick(m, dt)
	if !(b.possession == 0 && a.possession >= 0.8) {
		t.Errorf("a challenger beyond the pull radius should not steal (holder keeps it): a=%.3f b=%.3f", a.possession, b.possession)
	}

	// 3. The trap EXTENDS the reach: the same gap 8 is now within the trap-extended pull radius.
	setup(8, 1)
	if !m.inPullRange(b) {
		t.Fatalf("test setup: a full trap should bring gap 8 into the extended pull radius")
	}
	possessionTick(m, dt)
	if !(b.possession > 0 && a.possession < 0.8) {
		t.Errorf("a trapping challenger should steal from the extended pull radius (gap 8): a=%.3f b=%.3f", a.possession, b.possession)
	}
}

// TestPossessionDeniedByMarking: an opponent marking the holder body-to-body WITHOUT the ball in
// its own reach DRAINS (denies) the holder's possession but gains nothing for itself -- pure
// denial (Rule 2). A marker that IS near the ball instead steals by out-building (pull-range test).
func TestPossessionDeniedByMarking(t *testing.T) {
	m := BuildMatchFromConfig(NewStandardField(), 3, config.Default())
	const dt = 1.0 / 60
	a, b := firstOn(m, SideLeft), firstOn(m, SideRight)
	for _, p := range m.Players {
		p.Position = geom.NewVec(-1e5, float64(p.PlayerID)*60)
	}
	m.Ball.Position = geom.NewVec(0, 0)
	a.Position = geom.NewVec(0, 0) // a holds the ball (touching it)
	a.possession = 0.8
	m.possessor = a
	b.possession = 0
	b.Position = geom.NewVec(a.Radius()+b.Radius()-1, 0) // bumping a body-to-body, ball NOT in b's reach

	if m.inPullRange(b) {
		t.Fatalf("setup: the ball should be out of b's reach (this tests pure denial)")
	}
	if !playersTouching(a, b) {
		t.Fatalf("setup: b should be marking a body-to-body")
	}
	possessionTick(m, dt)
	if !(a.possession < 0.8) {
		t.Errorf("a holder marked by an opponent off the ball should be denied (drained), got a=%.3f", a.possession)
	}
	if b.possession != 0 {
		t.Errorf("a marker not near the ball should gain nothing (pure denial), got b=%.3f", b.possession)
	}
}

// TestPossessionHandoffToLatestEntrantNoLeak: when an opponent reaches a carrier's ball, the
// LATEST entrant builds and the carrier drains INTO it -- and once handed over, the new holder
// KEEPS its possession. It must not leak back into the displaced player and decay both to zero
// (the earlier bug, where the contest drained the holder into the wrong player).
func TestPossessionHandoffToLatestEntrantNoLeak(t *testing.T) {
	m := BuildMatchFromConfig(NewStandardField(), 3, config.Default())
	const dt = 1.0 / 60
	a, b := firstOn(m, SideLeft), firstOn(m, SideRight)
	for _, p := range m.Players {
		p.Position = geom.NewVec(-1e5, float64(p.PlayerID)*60)
	}
	surface := a.Radius() + m.Ball.Radius()
	gap := (a.Stats.TouchRange + a.pullRadius()) / 2
	m.Ball.Position = geom.NewVec(0, 0)
	a.Position = geom.NewVec(-(surface + gap), 0) // a holds from pull range (left)
	b.Position = geom.NewVec(surface+gap, 0)      // b arrives in pull range (right), not touching a
	a.possession, b.possession = 1.0, 0
	m.possessor = a

	tick := func() {
		m.advancePossessionBuilder()
		updatePossession(m.Ball, a, dt, a == m.possBuilder, false)
		updatePossession(m.Ball, b, dt, b == m.possBuilder, false)
		m.updateBallPossessor(dt)
	}
	for i := 0; i < 240; i++ {
		tick()
	}

	if !(b.possession > 0.8) {
		t.Errorf("the latest entrant should build and hold high possession, got b=%.3f", b.possession)
	}
	if !(a.possession < 0.2) {
		t.Errorf("the displaced carrier should drain low (not both to zero), got a=%.3f", a.possession)
	}
	if m.possessor != b {
		t.Errorf("possession should hand over to the latest entrant (got possessor==a: %v)", m.possessor == a)
	}
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

	// One tick: the challenger (latest at the ball) gains, the displaced holder falls away, but the
	// holder still holds (only a sliver moved).
	possessionTick(m, dt)
	if !(b.possession > 0 && a.possession < 0.8) {
		t.Fatalf("a contest should start moving possession: a=%.3f b=%.3f", a.possession, b.possession)
	}
	if m.possessor != a {
		t.Errorf("after one tick the holder should still hold, got %v", m.possessor)
	}

	// Sustained contest: the challenger eventually wins the ball.
	for i := 0; i < 120 && m.possessor != b; i++ {
		possessionTick(m, dt)
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
	m.advancePossessionBuilder()
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

// TestFullPowerPassBouncesOffFront: the baseline is tuned so a full-power point-blank pass
// DEFLECTS off a teammate's front (escapes the grip) rather than sticking -- the capture speed
// sits below the full shot power at EVERY teammate coefficient (so a firm ball always bounces,
// no trap needed), while a gentle pass stays below it and is still absorbed. The front
// restitution is high enough that even a buffed receiver's deflection escapes the grip, and the
// debuffed bounce is springier still but tamed off the near-elastic 0.95 cap.
func TestFullPowerPassBouncesOffFront(t *testing.T) {
	s := fieldPlayerStats() // the in-game preset
	tq := s.TouchQuality
	capFront := s.CaptureSpeed.Front
	restFront := s.Restitution.Front
	fullShot := s.Shoot.Eval(0) // full-power front shot speed (575)

	// A full-power front shot must exceed the capture speed at both neutral and full buff, so it
	// always lands in the BOUNCE branch -- a teammate cannot catch a point-blank blast untrapped.
	for _, coef := range []float64{0, tq.OwnTeamMax} {
		if cap := capFront * tq.captureMul(coef); cap >= fullShot {
			t.Errorf("full shot (%.0f) must exceed the capture speed at coef %+.1f (%.0f) so it bounces off", fullShot, coef, cap)
		}
	}
	// ...but the capture speed stays high enough that a GENTLE pass is still absorbed (caught).
	if capFront <= 200 {
		t.Errorf("capture speed %.0f is too low -- gentle passes would bounce too", capFront)
	}
	// Front restitution was RAISED so the deflection escapes the sticky hold even for a buffed
	// receiver; the debuffed bounce is springier but not near-elastic.
	if restFront <= 0.20 {
		t.Errorf("front restitution %.3f should be raised so a hard pass deflects off (not stick)", restFront)
	}
	buffedRest := restFront * tq.restitutionMul(tq.OwnTeamMax)
	debuffRest := restFront * tq.restitutionMul(tq.OtherTeam)
	if buffedRest < 0.12 {
		t.Errorf("buffed front restitution %.4f too low -- a buffed teammate would catch a full pass", buffedRest)
	}
	if debuffRest <= buffedRest || debuffRest > 0.6 {
		t.Errorf("debuffed front restitution %.4f should be springier than buffed (%.4f) but tamed (<=0.6)", debuffRest, buffedRest)
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
	m.advancePossessionBuilder()
	m.updateBallPossessor(1.0 / 60)
	if b.possession != 0 {
		t.Errorf("a received ball carrying no possession should give no head start, got %.3f", b.possession)
	}
}

// TestTeamChargeDrainedByChallenge: while both teams contest the ball (the owner holding it and an
// opponent also on it), the OWNING team's buff drains (possBuffDrain) AND the conceding team's
// debuff drains team-wide (possDebuffDrain) -- but the build progress is preserved and, while the
// owner is still on the ball, ownership does NOT flip. It hands over only once the defender has the
// ball alone (tested at the end). An off-ball collision leaves the charge untouched.
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

	// One tick of challenge starts draining the owner's BUFF (possBuffDrain) but does NOT touch the
	// build progress (which drives the conceding team's debuff) or hand ownership over.
	m.possSide, m.possProgress, m.possCoast, m.possBuffDrain = SideLeft, 1, 0, 0
	challenge()
	m.advanceTeamPossession(dt)
	if m.possSide != SideLeft {
		t.Errorf("a single challenge tick should not hand ownership over yet, side=%v", m.possSide)
	}
	if !(m.possBuffDrain > 0) {
		t.Errorf("a challenge should start draining the owner's buff, got possBuffDrain=%.4f", m.possBuffDrain)
	}
	if m.possProgress != 1 {
		t.Errorf("a challenge must NOT drain the build progress / the conceding team's debuff, got %.4f", m.possProgress)
	}

	// After draining for a while: the owner's BUFF is suppressed AND -- because the defender is ALSO
	// on the ball (a true contest) -- the conceding team's DEBUFF drains team-wide toward neutral
	// (the rule: a contested ball drains the debuff gradually rather than only clearing at handover).
	// The owner is still on the ball, so ownership has NOT flipped and the build progress is kept.
	for i := 0; i < 30; i++ {
		challenge()
		m.advanceTeamPossession(dt)
	}
	if m.possSide != SideLeft {
		t.Fatalf("a contest with the owner still on the ball must not hand over, side=%v", m.possSide)
	}
	strength := m.teamPossessionStrength(SideLeft)
	fullDebuff := r.Stats.TouchQuality.OtherTeam * strength
	if !(r.touchCoef > fullDebuff && r.touchCoef < 0) {
		t.Errorf("a contested ball with the defender on it should drain the conceding debuff toward neutral, got %.4f (full %.4f)", r.touchCoef, fullDebuff)
	}
	if !(l.touchCoef < l.Stats.TouchQuality.OwnTeamMax*strength) {
		t.Errorf("the owner's buff should be suppressed by the drain, got %.4f (full %.4f)", l.touchCoef, l.Stats.TouchQuality.OwnTeamMax*strength)
	}

	// A sustained contest with the owner STILL on the ball does NOT hand over -- it only drains.
	// Ownership flips only once the defender has the ball ALONE (a clean takeover).
	for i := 0; i < 120; i++ {
		challenge()
		m.advanceTeamPossession(dt)
	}
	if m.possSide != SideLeft {
		t.Errorf("a sustained contest with the owner on the ball must NOT hand over, got side=%v", m.possSide)
	}
	// The owner LEAVES the ball -- the defender now has it alone, so it wins it outright (handover).
	onlyToucher(m, r)
	m.advanceTeamPossession(dt)
	if m.possSide != SideRight {
		t.Errorf("the defender alone on the ball should win it and take the charge, got side=%v", m.possSide)
	}

	// Off-ball collision: L and R overlap each other far from the ball -> no contest, no drain.
	m.possSide, m.possProgress, m.possCoast, m.possBuffDrain = SideLeft, 1, 0, 0
	park()
	m.Ball.Position = m.Field.CenterSpot
	l.Position = geom.NewVec(50000, 0)
	r.Position = geom.NewVec(50010, 0) // overlap each other, nowhere near the ball
	m.advanceTeamPossession(dt)
	if m.possProgress != 1 {
		t.Errorf("an off-ball collision should not drain the team charge, got progress=%.4f", m.possProgress)
	}
}

// TestTeamChargeDrainedByOpponentInPullRange: while the owning team has the ball within a
// player's pull radius, an OPPONENT that also has it within their pull radius drains the team
// charge -- a ranged challenge, no body contact needed; with no opponent in range it is left alone.
func TestTeamChargeDrainedByOpponentInPullRange(t *testing.T) {
	m := BuildMatchFromConfig(NewStandardField(), 3, config.Default())
	const dt = 1.0 / 60
	a, b := firstOn(m, SideLeft), firstOn(m, SideRight)

	setup := func() {
		for _, p := range m.Players {
			p.Position = geom.NewVec(-1e5, float64(p.PlayerID)*60)
		}
		c := m.Field.CenterSpot // use the field centre so ConfineBall doesn't move the ball
		m.Ball.Position = c
		m.Ball.Velocity = geom.NewVec(0, 0)
		a.Position = c.Add(geom.NewVec(-(a.Radius() + m.Ball.Radius() + 1), 0)) // left owner: touching the ball
		b.Position = c.Add(geom.NewVec(b.Radius()+m.Ball.Radius()+3, 0))        // right opponent: gap 3 (in pull range, not touching)
		m.possSide, m.possProgress, m.possCoast, m.possBuffDrain = SideLeft, 1.0, 0, 0
	}

	// Owner has the ball; opponent has it in its pull radius (not touching) -> the owner's buff
	// drains, but the build progress (the conceding team's debuff) is left intact.
	setup()
	if !m.inPullRange(b) || m.touching(b) {
		t.Fatalf("test setup: opponent should be in pull range but not touching at gap 3")
	}
	m.advanceTeamPossession(dt)
	if !(m.possBuffDrain > 0) {
		t.Errorf("an opponent with the ball in its pull radius should drain the owner's buff, got possBuffDrain=%.4f", m.possBuffDrain)
	}
	if m.possProgress != 1.0 {
		t.Errorf("the drain must NOT reduce the build progress / the conceding team's debuff, got %.4f", m.possProgress)
	}

	// No opponent in range -> the charge is left alone (the owner is on the ball, so it holds/builds).
	setup()
	b.Position = m.Field.CenterSpot.Add(geom.NewVec(300, 0)) // far from the ball, out of pull range
	m.advanceTeamPossession(dt)
	if m.possProgress != 1.0 {
		t.Errorf("no opponent in pull range should leave the charge untouched, got %.4f", m.possProgress)
	}
}

// TestTeamDebuffDrainedByDefenderOnBall: while the defending team ALSO has the ball (a contest),
// its team-wide debuff drains gradually toward neutral (possDebuffDrain) for EVERY conceding player
// -- including one off the ball -- which is relief, never a buff; and the moment the defender has
// the ball ALONE the debuff clears outright via the handover.
func TestTeamDebuffDrainedByDefenderOnBall(t *testing.T) {
	m := BuildMatchFromConfig(NewStandardField(), 3, config.Default())
	const dt = 1.0 / 60
	owner, def := firstOn(m, SideLeft), firstOn(m, SideRight)
	var def2 *Player // a second defender, off the ball, to prove the relief is team-wide
	for _, p := range m.Players {
		if p.Team.Side == SideRight && p != def {
			def2 = p
			break
		}
	}

	contest := func() {
		for _, p := range m.Players {
			p.Position = geom.NewVec(-1e5, float64(p.PlayerID)*60)
		}
		c := m.Field.CenterSpot
		m.Ball.Position = c
		m.Ball.Velocity = geom.NewVec(0, 0)
		owner.Position = c.Add(geom.NewVec(-(owner.Radius() + m.Ball.Radius() + 1), 0)) // owner touching
		def.Position = c.Add(geom.NewVec(def.Radius()+m.Ball.Radius()+3, 0))            // defender in pull range
		if def2 != nil {
			def2.Position = geom.NewVec(-2e5, 0) // far off the ball
		}
	}

	m.possSide, m.possProgress, m.possCoast = SideLeft, 1.0, 0
	m.possBuffDrain, m.possDebuffDrain = 0, 0

	// Both teams on the ball -> a contest: the conceding debuff drains team-wide, no handover.
	for i := 0; i < 30; i++ {
		contest()
		m.advanceTeamPossession(dt)
	}
	if m.possSide != SideLeft {
		t.Fatalf("a contest should not hand over while the owner is on the ball, side=%v", m.possSide)
	}
	if !(m.possDebuffDrain > 0) {
		t.Errorf("a defender on the contested ball should drain the conceding debuff, got possDebuffDrain=%.4f", m.possDebuffDrain)
	}
	strength := m.teamPossessionStrength(SideLeft)
	full := def.Stats.TouchQuality.OtherTeam * strength
	conceding := []*Player{def}
	if def2 != nil {
		conceding = append(conceding, def2)
	}
	for _, p := range conceding {
		if !(p.touchCoef > full && p.touchCoef < 0) {
			t.Errorf("conceding player %d should be relieved team-wide toward neutral, got %.4f (full %.4f)", p.PlayerID, p.touchCoef, full)
		}
	}

	// The defender gets the ball ALONE: the debuff keeps draining smoothly (no instant reset). One
	// tick must NOT hand over while the relief is still draining.
	onlyToucher(m, def)
	m.advanceTeamPossession(dt)
	if m.possSide != SideLeft {
		t.Errorf("a defender just-alone on the ball must not snap the handover while the debuff is still draining, side=%v", m.possSide)
	}
	// Sustained possession by the defender alone drains the debuff fully, THEN hands the charge over.
	for i := 0; i < 200 && m.possSide == SideLeft; i++ {
		onlyToucher(m, def)
		m.advanceTeamPossession(dt)
	}
	if m.possSide != SideRight {
		t.Errorf("once the debuff fully drains, the defender alone on the ball should take the charge, got %v", m.possSide)
	}
	if m.possDebuffDrain != 0 {
		t.Errorf("the handover should reset the debuff relief to 0, got %.4f", m.possDebuffDrain)
	}
}

// TestDebuffReliefDrainsRegeneratesFreezes pins the debuff tug-of-war with the contest latch: a
// defender contesting the ball DRAINS both the conceding debuff and the owner's buff (a bare touch
// is enough, no possession); a LOOSE ball after a defender touched it (a deflection) KEEPS draining
// both (latched); the owning team regaining clean control REGENERATES the debuff (and recovers the
// buff), clearing the latch; and a CLEAN release the defender never touched FREEZES it.
func TestDebuffReliefDrainsRegeneratesFreezes(t *testing.T) {
	m := BuildMatchFromConfig(NewStandardField(), 3, config.Default())
	const dt = 1.0 / 60
	atk, def := firstOn(m, SideLeft), firstOn(m, SideRight) // atk = owner/attacking, def = conceding
	m.possSide, m.possProgress, m.possBuffDrain, m.possDebuffDrain = SideLeft, 1, 0, 0

	// DRAIN: a defender contesting the ball drains the conceding debuff AND the owner's buff together
	// -- on a bare touch, no possession required (the "first touch drains both" rule).
	for i := 0; i < 8; i++ {
		onlyToucher(m, def)
		m.advanceTeamPossession(dt)
	}
	if !(m.possDebuffDrain > 0 && m.possBuffDrain > 0) {
		t.Fatalf("a defender touch should drain BOTH debuff (%.4f) and buff (%.4f)", m.possDebuffDrain, m.possBuffDrain)
	}

	// LATCHED LOOSE (the deflection case): the ball flies loose after the defender touched it -- both
	// the debuff and the buff KEEP draining, with nobody on the ball.
	dbDrain, bfDrain := m.possDebuffDrain, m.possBuffDrain
	for i := 0; i < 6; i++ {
		onlyToucher(m, nil) // ball in flight, nobody in reach
		m.advanceTeamPossession(dt)
	}
	if !(m.possDebuffDrain > dbDrain && m.possBuffDrain > bfDrain) {
		t.Errorf("after a defender touch, a loose (deflected) ball should KEEP draining both: debuff %.4f->%.4f buff %.4f->%.4f", dbDrain, m.possDebuffDrain, bfDrain, m.possBuffDrain)
	}

	// REGENERATE: the owning team regains clean control -- the debuff climbs back toward full and the
	// latch clears.
	dbPeak := m.possDebuffDrain
	for i := 0; i < 5; i++ {
		onlyToucher(m, atk)
		m.advanceTeamPossession(dt)
	}
	if !(m.possDebuffDrain < dbPeak) {
		t.Errorf("with the owner in clean control, the debuff should REGENERATE: was %.4f, now %.4f", dbPeak, m.possDebuffDrain)
	}
	if m.possSide != SideLeft {
		t.Fatalf("ownership should stay with the attacking team while it holds the ball, got %v", m.possSide)
	}

	// FREEZE on a CLEAN release: the latch is now cleared (the owner just had clean control and no
	// defender has touched since), so a loose ball neither drains nor regenerates.
	held := m.possDebuffDrain
	for i := 0; i < 10; i++ {
		onlyToucher(m, nil)
		m.advanceTeamPossession(dt)
	}
	if math.Abs(m.possDebuffDrain-held) > 1e-9 {
		t.Errorf("a clean release (latch cleared, defender never touched) should FREEZE the debuff: was %.4f, now %.4f", held, m.possDebuffDrain)
	}
}

// TestReleasedCarrierMarkedDrainsOnlyOwnBoost pins refinement P1: a player that has RELEASED the
// ball (a stale m.possessor whose ball is no longer in its own pull reach) and is then marked by an
// opponent drains ONLY its own per-player boost (boostDrain), NOT the whole team buff. The carrier
// check now keys off m.inPullRange(p), not the stale possessor flag. The contrast case -- the same
// player WITH the ball in reach and marked -- DOES drain the team buff (possBuffDrain), pinning the
// distinction between "marking a real carrier" and "marking a player who already passed".
func TestReleasedCarrierMarkedDrainsOnlyOwnBoost(t *testing.T) {
	const dt = 1.0 / 60

	// mate is a parked team-mate on the boosted side, used to read the unsuppressed published buff.
	pick := func(m *Match) (p, mate, opp *Player) {
		for _, q := range m.Players {
			if q.Team.Side == SideLeft {
				if p == nil {
					p = q
				} else if mate == nil {
					mate = q
				}
			} else if opp == nil {
				opp = q
			}
		}
		return p, mate, opp
	}

	// Case A (the P1 behaviour): the ball is OUT of p's reach (p has released/passed), an opponent
	// marks p within reach but is NOT near the ball -> only p.boostDrain rises; the team buff and a
	// team-mate's published buff are untouched.
	{
		m := BuildMatchFromConfig(NewStandardField(), 3, config.Default())
		p, mate, opp := pick(m)
		for _, q := range m.Players {
			q.Position = geom.NewVec(-1e5, float64(q.PlayerID)*60)
		}
		m.possSide, m.possProgress, m.possCoast, m.possBuffDrain = SideLeft, 1, 0, 0
		m.possessor = p // stale: p still flagged as possessor though it has released the ball

		p.Position = geom.NewVec(0, 0)
		// Ball far away on the +X side so it is well beyond p's pull radius (p is NOT the carrier).
		m.Ball.Position = geom.NewVec(p.Radius()+m.Ball.Radius()+p.pullRadius()+200, 0)
		// Opponent overlapping p on the -X side: marks p (within p's reach) but far from the ball.
		opp.Position = geom.NewVec(-(p.Radius() + opp.Radius() - 1), 0)
		mate.Position = geom.NewVec(0, 5000) // parked, unpressured team-mate

		if m.inPullRange(p) {
			t.Fatalf("setup A: the ball should be OUT of p's pull range (p has released it)")
		}
		if !m.pressuredByOpponent(p) {
			t.Fatalf("setup A: the opponent should be marking p within reach")
		}
		if m.ballInTeamPullRange(opp.Team.Side) {
			t.Fatalf("setup A: the opponent must NOT be near the ball")
		}

		for i := 0; i < 30; i++ { // ~0.5s of marking
			m.advanceTeamPossession(dt)
		}

		if !(p.boostDrain > 0) {
			t.Errorf("a released carrier marked off the ball should drain its OWN boost, got boostDrain=%.4f", p.boostDrain)
		}
		if m.possBuffDrain != 0 {
			t.Errorf("marking a released (non-carrier) player must NOT drain the team buff, got possBuffDrain=%.4f", m.possBuffDrain)
		}
		full := mate.Stats.TouchQuality.OwnTeamMax * m.teamPossessionStrength(SideLeft)
		if !(mate.touchCoef > full*0.999) {
			t.Errorf("a parked team-mate should keep the full unsuppressed buff, got %.4f (full %.4f)", mate.touchCoef, full)
		}
	}

	// Case B (the contrast -- the real carrier): the SAME player WITH the ball in its reach, marked by
	// an opponent, DOES drain the whole team buff (possBuffDrain rises). This pins the distinction.
	{
		m := BuildMatchFromConfig(NewStandardField(), 3, config.Default())
		p, _, opp := pick(m)
		for _, q := range m.Players {
			q.Position = geom.NewVec(-1e5, float64(q.PlayerID)*60)
		}
		m.possSide, m.possProgress, m.possCoast, m.possBuffDrain = SideLeft, 1, 0, 0
		m.possessor = p

		c := m.Field.CenterSpot
		p.Position = c
		m.Ball.Position = c.Add(geom.NewVec(p.Radius()+m.Ball.Radius()+3, 0)) // ball in p's pull range
		// Opponent marks p (within p's reach) but is NOT itself near the ball, on the far side from it.
		opp.Position = c.Add(geom.NewVec(-(p.Radius() + opp.Radius() - 1), 0))

		if !m.inPullRange(p) {
			t.Fatalf("setup B: the ball should be in p's pull range (p IS the carrier)")
		}
		if !m.pressuredByOpponent(p) {
			t.Fatalf("setup B: the opponent should be marking p within reach")
		}

		m.advanceTeamPossession(dt)
		if !(m.possBuffDrain > 0) {
			t.Errorf("marking the real ball-carrier should drain the team buff, got possBuffDrain=%.4f", m.possBuffDrain)
		}
	}
}

// TestBoostHealsOnlyWhileTeamMateHasBall pins refinements P2 and EXT: a pre-drained per-player boost
// (boostDrain) AND a pre-faded team buff (possBuffDrain) are FROZEN while the ball is loose (no
// owning-team player has it in reach), and only RECOVER once a team-mate regains the ball.
func TestBoostHealsOnlyWhileTeamMateHasBall(t *testing.T) {
	const dt = 1.0 / 60
	m := BuildMatchFromConfig(NewStandardField(), 3, config.Default())

	var p, mate *Player // p: the drained player; mate: a team-mate who will hold the ball
	for _, q := range m.Players {
		if q.Team.Side == SideLeft {
			if p == nil {
				p = q
			} else if mate == nil {
				mate = q
			}
		}
	}

	m.possSide, m.possProgress, m.possCoast = SideLeft, 1, 0
	p.boostDrain, m.possBuffDrain = 0.5, 0.5

	// (a) Ball LOOSE -- nobody in reach (ownerNearBall false) and p unmarked: both drains FROZEN.
	for _, q := range m.Players {
		q.Position = geom.NewVec(-1e5, float64(q.PlayerID)*60)
	}
	p.Position = geom.NewVec(0, 0)
	m.Ball.Position = geom.NewVec(1e5, 0) // far from everyone -> loose / in flight
	if m.ballInTeamPullRange(SideLeft) || m.pressuredByOpponent(p) {
		t.Fatalf("setup (a): the ball should be loose and p unmarked")
	}

	beforeBoost, beforeBuff := p.boostDrain, m.possBuffDrain
	for i := 0; i < 30; i++ {
		m.advanceTeamPossession(dt)
	}
	if math.Abs(p.boostDrain-beforeBoost) > 1e-9 {
		t.Errorf("a drained boost should be FROZEN while the ball is loose: was %.4f, now %.4f", beforeBoost, p.boostDrain)
	}
	if math.Abs(m.possBuffDrain-beforeBuff) > 1e-9 {
		t.Errorf("the faded team buff should be FROZEN while the ball is loose: was %.4f, now %.4f", beforeBuff, m.possBuffDrain)
	}

	// (b) A team-mate gets on the ball (ownerNearBall true): both RECOVER (drain toward 0).
	onlyToucher(m, mate)
	for i := 0; i < 30; i++ {
		onlyToucher(m, mate) // keep mate holding (onlyToucher re-parks each call)
		m.advanceTeamPossession(dt)
	}
	if !(p.boostDrain < beforeBoost) {
		t.Errorf("p's boost should recover once a team-mate has the ball: was %.4f, now %.4f", beforeBoost, p.boostDrain)
	}
	if !(m.possBuffDrain < beforeBuff) {
		t.Errorf("the team buff should recover once a team-mate has the ball: was %.4f, now %.4f", beforeBuff, m.possBuffDrain)
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
	// Team-charge SHAPING (robust to the contact impulse-scaling tuning): the conceding team
	// flings the mid ball off harder than the owning team (which absorbs it) and harder than a
	// neutral baseline touch.
	if !(other.X > own.X && other.X > base.X) {
		t.Errorf("conceding team should fling the ball further than owning/baseline: own=%.2f base=%.2f other=%.2f", own.X, base.X, other.X)
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
		p.trapAura = trap            // trapAura is the effective trap strength the contact reads (trap=1 -> peak)
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

// TestTeamChargeHandedOverWhenBallWonOutright: a clean takeover -- the defending team gets the ball
// ALONE (the owner no longer on it) -- hands the charge over, but GRADUALLY: the conceding debuff
// drains to fully cleared first (no instant snap), THEN ownership flips, the new owner builds from
// zero, and the relief resets. A CONTESTED ball (both teams on it) likewise only drains without
// flipping (see TestTeamChargeDrainedByChallenge).
func TestTeamChargeHandedOverWhenBallWonOutright(t *testing.T) {
	m := BuildMatchFromConfig(NewStandardField(), 3, config.Default())
	const dt = 1.0 / 60
	left, right := firstOn(m, SideLeft), firstOn(m, SideRight)

	buildToFull(m, left, dt)
	if m.possSide != SideLeft || m.possProgress < 0.99 {
		t.Fatalf("the left team should be fully charged, side=%v progress=%.3f", m.possSide, m.possProgress)
	}

	// The opponent gets the ball ALONE (the owner parked away) -- a clean takeover, but GRADUAL: one
	// tick must NOT flip it while the debuff is still draining.
	onlyToucher(m, right)
	m.advanceTeamPossession(dt)
	if m.possSide != SideLeft {
		t.Errorf("a single tick of the defender on the ball must not snap the handover (gradual), got %v", m.possSide)
	}
	// Sustained possession by the defender alone drains the debuff fully, THEN hands the charge over.
	for i := 0; i < 200 && m.possSide == SideLeft; i++ {
		onlyToucher(m, right)
		m.advanceTeamPossession(dt)
	}
	if m.possSide != SideRight {
		t.Errorf("once the debuff fully drains, the defender alone on the ball should take the charge, got %v", m.possSide)
	}
	if m.possProgress > 0.1 {
		t.Errorf("after the handover the new owner should be building from ~zero, got %.3f", m.possProgress)
	}
	if m.possDebuffDrain != 0 {
		t.Errorf("the handover should reset the debuff relief, got %.3f", m.possDebuffDrain)
	}
}

// TestTeamChargeContestDrains: when both teams touch the ball at once, the charge is CONTESTED --
// it now DRAINS over time (Rule 4) rather than clearing instantly; ownership only changes once it
// has drained to zero.
func TestTeamChargeContestDrains(t *testing.T) {
	m := BuildMatchFromConfig(NewStandardField(), 3, config.Default())
	const dt = 1.0 / 60
	left, right := firstOn(m, SideLeft), firstOn(m, SideRight)

	buildToFull(m, left, dt)
	if m.possSide != SideLeft {
		t.Fatalf("precondition: the left team should own the charge, got %v", m.possSide)
	}

	bothTouch(m, left, right)
	m.advanceTeamPossession(dt)
	if m.possSide != SideLeft {
		t.Errorf("a contested ball should not instantly clear ownership now (it drains), got %v", m.possSide)
	}
	if !(m.possBuffDrain > 0) {
		t.Errorf("a contested ball should drain the owner's buff, got possBuffDrain=%.3f", m.possBuffDrain)
	}
	if m.possProgress < 0.99 {
		t.Errorf("a contested ball must NOT drain the build progress / debuff, got %.3f", m.possProgress)
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
