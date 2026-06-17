package sim

import (
	"math"
	"testing"
)

// Probe: does the published reception coefficient match the strength right before
// the bake, with NO discontinuity? The spec says a late receiver "starts at the
// decayed coefficient". The publish (step 1) happens before the update (step 2),
// so the receiver's touchCoef = OwnTeamMax * strength(pre-bake). After the bake
// the NEXT tick's strength must be >= this (monotone rebuild, no dip).
func TestProbeReceptionContinuity(t *testing.T) {
	const dt = 1.0 / 60
	for _, coastTicks := range []int{0, 30, 90, 120, 150, 180, 200, 209, 210} {
		m := BuildMatchFromConfig(NewStandardField(), 3, NewDefaultConfigForProbe())
		a := firstOnP(m, SideLeft)
		b := secondOnP(m, SideLeft)
		buildToFull(m, a, dt)
		// release
		onlyToucher(m, nil)
		for i := 0; i < coastTicks; i++ {
			m.advanceTeamPossession(dt)
		}
		preStrength := m.teamPossessionStrength(SideLeft)
		preSide := m.possSide
		// B receives this tick
		onlyToucher(m, b)
		m.advanceTeamPossession(dt)
		if preSide == SideNone {
			// expired before reception; coef must be 0 and a fresh build started
			if b.touchCoef != 0 {
				t.Errorf("coast=%d expired: receiver coef should be 0, got %.4f", coastTicks, b.touchCoef)
			}
			continue
		}
		// receiver took the ball at the pre-bake strength
		if math.Abs(b.touchCoef-preStrength) > 1e-6 {
			t.Errorf("coast=%d: receiver coef %.6f != pre strength %.6f", coastTicks, b.touchCoef, preStrength)
		}
		// strength now (post-bake, after one build increment) must be >= pre (no dip)
		postStrength := m.teamPossessionStrength(SideLeft)
		if postStrength+1e-9 < preStrength {
			t.Errorf("coast=%d: strength DIPPED on reception: pre=%.6f post=%.6f", coastTicks, preStrength, postStrength)
		}
	}
}

// Probe drift: repeated within-hold re-touch should not accumulate progress drift.
func TestProbeWithinHoldDrift(t *testing.T) {
	const dt = 1.0 / 60
	m := BuildMatchFromConfig(NewStandardField(), 3, NewDefaultConfigForProbe())
	a := firstOnP(m, SideLeft)
	// build partway
	onlyToucher(m, a)
	for i := 0; i < 40; i++ {
		m.advanceTeamPossession(dt)
	}
	p := m.possProgress
	maxDrift := 0.0
	for i := 0; i < 100000; i++ {
		// simulate a 1-tick coast (within hold) then a re-touch, but DON'T let build increment
		// by undoing it, to isolate the bake round-trip
		m.possCoast = 0.001
		before := m.possProgress
		m.possProgress = teamBuildCurveInv(teamBuildCurve(m.possProgress) * teamCoastEnvelope(m.possCoast))
		d := math.Abs(m.possProgress - before)
		if d > maxDrift {
			maxDrift = d
		}
	}
	_ = p
	if maxDrift > 1e-9 {
		t.Errorf("within-hold bake drift too large: %.3e", maxDrift)
	}
}
