package sim

import (
	"math"
	"testing"

	"phootball/internal/config"
)

// TestTrapEnergyDrainsAndRegenerates: holding the trap drains the 0..1 energy bar (trapCharge)
// linearly; releasing regenerates it at ~1/3 the drain rate.
func TestTrapEnergyDrainsAndRegenerates(t *testing.T) {
	m := BuildMatchFromConfig(NewStandardField(), 3, config.Default())
	p := firstOn(m, SideLeft)
	const dt = 1.0 / 60
	tn := p.Tuning

	if p.TrapCharge() != 1 {
		t.Fatalf("a fresh player should start with a full trap bar, got %.3f", p.TrapCharge())
	}

	held := int(0.5 / dt)
	for i := 0; i < held; i++ {
		m.applyIntent(p, Intent{Trap: true}, dt)
	}
	drained := p.TrapCharge()
	want := 1 - tn.TrapDrainPerSecond*float64(held)*dt
	if drained <= 0 || drained >= 1 || math.Abs(drained-want) > 1e-6 {
		t.Fatalf("after %d ticks held: trapCharge=%.4f, want %.4f (linear drain)", held, drained, want)
	}

	for i := 0; i < held; i++ {
		m.applyIntent(p, Intent{Trap: false}, dt)
	}
	gain, loss := p.TrapCharge()-drained, 1-drained
	if gain <= 0 {
		t.Fatalf("the bar should regenerate when released, gained %.4f", gain)
	}
	if ratio := gain / loss; math.Abs(ratio-1.0/3.0) > 0.05 {
		t.Errorf("regen should be ~1/3 of drain over equal time, got ratio %.3f", ratio)
	}
}

// TestTrapAuraRampsUpPlateausThenFloors: from a full bar the effective strength ramps up (gradually,
// not the old instant snap) to ~full, holds there while the bar has energy, then settles to the
// TrapMinAura FLOOR once the bar drains out while still held -- and only collapses to 0 on release.
func TestTrapAuraRampsUpPlateausThenFloors(t *testing.T) {
	m := BuildMatchFromConfig(NewStandardField(), 3, config.Default())
	p := firstOn(m, SideLeft)
	const dt = 1.0 / 60
	floor := p.Tuning.TrapMinAura

	var aura []float64
	for i := 0; i < 160; i++ { // long enough to rise, plateau, and drain the full bar
		m.applyIntent(p, Intent{Trap: true}, dt)
		aura = append(aura, p.TrapAura())
	}
	maxLvl, maxIdx := 0.0, 0
	for i, a := range aura {
		if a > maxLvl {
			maxLvl, maxIdx = a, i
		}
	}
	if maxLvl < 0.99 {
		t.Errorf("from a full bar the aura should rise to ~full, peak %.3f", maxLvl)
	}
	if !(maxIdx > 0 && maxIdx < len(aura)-1) {
		t.Errorf("aura should rise then fall (interior peak), peaked at %d of %d", maxIdx, len(aura))
	}
	if maxIdx < 20 { // gradual linear come-up, NOT the old ~10-tick snap
		t.Errorf("come-up should be gradual; reached peak too fast at tick %d", maxIdx)
	}
	atPeak := 0
	for _, a := range aura {
		if a > maxLvl-1e-6 {
			atPeak++
		}
	}
	if atPeak < 3 {
		t.Errorf("aura should HOLD at its peak (a plateau), only %d ticks at peak", atPeak)
	}
	if end := aura[len(aura)-1]; math.Abs(end-floor) > 1e-3 {
		t.Errorf("a drained but still-held trap should settle at the min-aura floor %.3f, got %.4f", floor, end)
	}

	// Releasing collapses the aura the rest of the way to 0.
	for i := 0; i < 60; i++ {
		m.applyIntent(p, Intent{Trap: false}, dt)
	}
	if p.TrapAura() > 1e-9 {
		t.Errorf("releasing should collapse the aura to 0, got %.4f", p.TrapAura())
	}
}

// TestTrapAuraPeakLimitedByEnergy: pressing with a half-full bar yields a smaller peak ("big but not
// fully big"), and once it drains it settles at the min floor (not 0) while still held.
func TestTrapAuraPeakLimitedByEnergy(t *testing.T) {
	m := BuildMatchFromConfig(NewStandardField(), 3, config.Default())
	p := firstOn(m, SideLeft)
	const dt = 1.0 / 60
	p.trapCharge = 0.5 // start half-full
	floor := p.Tuning.TrapMinAura

	maxLvl := 0.0
	for i := 0; i < 160; i++ {
		m.applyIntent(p, Intent{Trap: true}, dt)
		if a := p.TrapAura(); a > maxLvl {
			maxLvl = a
		}
	}
	if !(maxLvl > 0.2 && maxLvl < 0.7) {
		t.Errorf("a half-full bar should give a partial peak (~0.5), got %.3f", maxLvl)
	}
	if end := p.TrapAura(); math.Abs(end-floor) > 1e-3 {
		t.Errorf("a drained but still-held trap should settle at the min floor %.3f, got %.4f", floor, end)
	}
}
