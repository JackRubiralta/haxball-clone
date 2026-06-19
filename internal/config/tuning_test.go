package config

import (
	"bytes"
	"encoding/gob"
	"testing"
)

// TestMatchSetupBuildCarriesTuning: an edited MatchSetup.Tuning flows through Build into the
// Config the sim consumes (the menu -> match path).
func TestMatchSetupBuildCarriesTuning(t *testing.T) {
	s := DefaultMatchSetup()
	s.Tuning.Player.MaxSpeed = 200
	cfg, err := s.Build()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Tuning.Player.MaxSpeed != 200 {
		t.Errorf("Build dropped the edited tuning: MaxSpeed = %v, want 200", cfg.Tuning.Player.MaxSpeed)
	}
}

// TestMatchSetupZeroTuningKeepsDefault: a setup that never seeds Tuning (zero value) falls
// back to DefaultTuning, so CLI/older call sites are unaffected.
func TestMatchSetupZeroTuningKeepsDefault(t *testing.T) {
	s := DefaultMatchSetup()
	s.Tuning = Tuning{} // unset
	cfg, err := s.Build()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Tuning != DefaultTuning() {
		t.Errorf("a zero Tuning should fall back to DefaultTuning()")
	}
}

// TestTuningGobRoundTrip: the tuning survives gob (the netcode wire). This is the guard for the
// "curve kinds are hardcoded, not stored" decision -- a function-typed field would fail here.
func TestTuningGobRoundTrip(t *testing.T) {
	s := DefaultMatchSetup()
	s.Tuning.Player.MaxSpeed = 199
	s.Tuning.Player.Restitution.Front = 0.4
	s.Tuning.Possession.BuildSeconds = 2.5

	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(s); err != nil {
		t.Fatalf("gob encode failed (a stored func field would do this): %v", err)
	}
	var got MatchSetup
	if err := gob.NewDecoder(&buf).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Tuning.Player.MaxSpeed != 199 || got.Tuning.Player.Restitution.Front != 0.4 || got.Tuning.Possession.BuildSeconds != 2.5 {
		t.Errorf("tuning did not survive gob: %+v", got.Tuning)
	}
}

func TestTuningValidate(t *testing.T) {
	if err := DefaultTuning().Validate(); err != nil {
		t.Errorf("default tuning should validate: %v", err)
	}
	bad := DefaultTuning()
	bad.Player.Mass = 0
	if err := bad.Validate(); err == nil {
		t.Errorf("zero player mass should fail validation (divide-by-zero trap)")
	}
}
