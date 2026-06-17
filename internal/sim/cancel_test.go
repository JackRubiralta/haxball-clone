package sim

import "testing"

// TestCancelChargeSuppressesKick verifies the right-click cancel: an in-progress shot
// charge that is canceled drops to zero, does not rebuild while the shoot button stays
// held, and -- crucially -- does NOT fire when the button is finally released. The cancel
// latch resets on release so a fresh press charges and fires normally, and the same cancel
// tick still builds trap (cancel + settle in one motion).
func TestCancelChargeSuppressesKick(t *testing.T) {
	const dt = 1.0 / 60.0
	m := BuildSolo(NewStandardField())
	p := m.Players[0]

	// Build a charge over 20 ticks.
	for i := 0; i < 20; i++ {
		m.applyIntent(p, Intent{ShootHeld: true}, dt)
	}
	if p.shootCharge <= 0 {
		t.Fatalf("charge did not build (got %.3f)", p.shootCharge)
	}

	// Cancel this tick (shoot still held), also requesting trap -- cancel must coexist with trap.
	m.applyIntent(p, Intent{ShootHeld: true, CancelCharge: true, Trap: true}, dt)
	if p.shootCharge != 0 || !p.shootCanceled {
		t.Fatalf("cancel did not clear/latch the charge (charge=%.3f canceled=%v)", p.shootCharge, p.shootCanceled)
	}
	if p.trapCharge <= 0 {
		t.Errorf("cancel suppressed trap; right-click should still settle the ball (trapCharge=%.3f)", p.trapCharge)
	}

	// Hold shoot 20 more ticks: a canceled charge must NOT rebuild.
	for i := 0; i < 20; i++ {
		m.applyIntent(p, Intent{ShootHeld: true}, dt)
	}
	if p.shootCharge != 0 {
		t.Fatalf("charge rebuilt after cancel while shoot held (got %.3f)", p.shootCharge)
	}

	// Release: the canceled charge must not fire, and the latch must reset.
	m.applyIntent(p, Intent{ShootHeld: false}, dt)
	if p.WantsKick {
		t.Fatalf("canceled charge fired a kick on release")
	}
	if p.shootCanceled {
		t.Fatalf("cancel latch not reset on shoot release")
	}

	// A fresh charge after the cancel fires normally on release.
	for i := 0; i < 10; i++ {
		m.applyIntent(p, Intent{ShootHeld: true}, dt)
	}
	m.applyIntent(p, Intent{ShootHeld: false}, dt)
	if !p.WantsKick {
		t.Fatalf("a fresh charge after a prior cancel failed to fire")
	}
}
