package sim

import (
	"testing"

	"phootball/internal/config"
)

// TestTrapAuraGrowsToMaxThenWeakens verifies the cosmetic trap aura: while the trap is held it
// swells to a max then WEAKENS as it is over-held (so the on-screen circle grows then shrinks),
// and on release it only ever shrinks (never grows-then-shrinks).
func TestTrapAuraGrowsToMaxThenWeakens(t *testing.T) {
	// Shape: peaks at the peak charge, weaker (but still visible) when fully over-held.
	if got := trapAuraShape(trapAuraPeak); got < 0.999 {
		t.Errorf("aura should reach its max (~1) at the peak charge, got %.3f", got)
	}
	if full := trapAuraShape(1); !(full < 0.999 && full > 0) {
		t.Errorf("a fully over-held trap aura should be weaker than max but still > 0, got %.3f", full)
	}

	m := BuildMatchFromConfig(NewStandardField(), 3, config.Default())
	p := firstOn(m, SideLeft)
	const dt = 1.0 / 60

	// Hold the trap right through a full charge-up and record the aura each tick.
	var held []float64
	for i := 0; i < int(trapChargeTime/dt)+10; i++ {
		m.applyIntent(p, Intent{Trap: true}, dt)
		held = append(held, p.TrapAura())
	}
	maxLvl, maxIdx := 0.0, 0
	for i, l := range held {
		if l > maxLvl {
			maxLvl, maxIdx = l, i
		}
	}
	if !(maxIdx > 0 && maxIdx < len(held)-1) {
		t.Errorf("aura should peak PARTWAY through holding (a hump), peak at tick %d of %d", maxIdx, len(held))
	}
	if end := held[len(held)-1]; !(end < maxLvl) {
		t.Errorf("aura should weaken after its peak while over-held: end %.3f vs max %.3f", end, maxLvl)
	}

	// Release: the aura must only shrink (never re-grow) and reach 0.
	prev := p.TrapAura()
	for i := 0; i < 120; i++ {
		m.applyIntent(p, Intent{Trap: false}, dt)
		cur := p.TrapAura()
		if cur > prev+1e-9 {
			t.Fatalf("aura must never grow on release: %.4f -> %.4f at release tick %d", prev, cur, i)
		}
		prev = cur
	}
	if prev != 0 {
		t.Errorf("aura should shrink to 0 after release, got %.4f", prev)
	}
}
