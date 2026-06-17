package sim

import (
	"math"
	"testing"
)

// Probe: opponent intercepts a coasting (in-flight) charge. Spec: "the OTHER team
// touching hands ownership over and restarts their build from zero". Even mid-coast
// (possSide still left, possCoast>0), a right touch must take over with a FRESH build,
// NOT inherit the left team's progress via the bake.
func TestProbeOpponentInterceptsCoast(t *testing.T) {
	const dt = 1.0 / 60
	m := BuildMatchFromConfig(NewStandardField(), 3, NewDefaultConfigForProbe())
	left := firstOnP(m, SideLeft)
	right := firstOnP(m, SideRight)
	buildToFull(m, left, dt)
	onlyToucher(m, nil)
	for i := 0; i < 30; i++ { // coast a bit, still in hold
		m.advanceTeamPossession(dt)
	}
	if m.possSide != SideLeft || m.possCoast <= 0 {
		t.Fatalf("precondition: left should still own a coasting charge, side=%v coast=%.3f", m.possSide, m.possCoast)
	}
	onlyToucher(m, right)
	m.advanceTeamPossession(dt)
	if m.possSide != SideRight {
		t.Fatalf("opponent intercept mid-coast should hand ownership over, got %v", m.possSide)
	}
	if m.possProgress > 0.05 {
		t.Errorf("intercept should reset build to ~0, got %.4f (did the bake leak left's progress?)", m.possProgress)
	}
	if m.possCoast != 0 {
		t.Errorf("coast should reset on takeover, got %.4f", m.possCoast)
	}
}

// Probe: the publish step (step 1) when an opponent intercepts mid-coast. The conceding
// team is now the LEFT team (still possSide at publish time). The RIGHT toucher should
// receive a NEGATIVE coef (OtherTeam * strength) on the intercept tick, so a blocked
// shot flies off them. Confirm publish uses pre-takeover ownership.
func TestProbeInterceptPublishesNegative(t *testing.T) {
	const dt = 1.0 / 60
	m := BuildMatchFromConfig(NewStandardField(), 3, NewDefaultConfigForProbe())
	left := firstOnP(m, SideLeft)
	right := firstOnP(m, SideRight)
	buildToFull(m, left, dt)
	onlyToucher(m, nil)
	for i := 0; i < 10; i++ { // still full (within hold)
		m.advanceTeamPossession(dt)
	}
	strength := m.teamPossessionStrength(SideLeft)
	onlyToucher(m, right)
	m.advanceTeamPossession(dt)
	want := DefaultStats(500).TouchQuality.OtherTeam * strength
	if math.Abs(right.touchCoef-want) > 1e-6 {
		t.Errorf("intercept tick should publish OtherTeam coef %.4f, got %.4f", want, right.touchCoef)
	}
}

// Probe: contested (both teams touch) while a charge is built. Must clear entirely.
func TestProbeContestedClears(t *testing.T) {
	const dt = 1.0 / 60
	m := BuildMatchFromConfig(NewStandardField(), 3, NewDefaultConfigForProbe())
	left := firstOnP(m, SideLeft)
	right := firstOnP(m, SideRight)
	buildToFull(m, left, dt)
	// place both on the ball
	for _, p := range m.Players {
		p.Position = vecFar(p.PlayerID)
	}
	left.Position = zeroVec()
	right.Position = zeroVec()
	m.Ball.Position = zeroVec()
	m.advanceTeamPossession(dt)
	if m.possSide != SideNone || m.possProgress != 0 {
		t.Errorf("contested should clear: side=%v progress=%.4f", m.possSide, m.possProgress)
	}
}
