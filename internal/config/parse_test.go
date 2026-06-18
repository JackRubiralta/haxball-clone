package config

import (
	"errors"
	"io"
	"reflect"
	"testing"
)

func TestPresetsValidate(t *testing.T) {
	for name, g := range map[string]Geometry{
		"standard": StandardGeometry(),
		"small":    SmallGeometry(),
		"large":    LargeGeometry(),
	} {
		if err := g.Validate(); err != nil {
			t.Errorf("%s preset failed Validate: %v", name, err)
		}
	}
}

func TestNormalizeIdempotent(t *testing.T) {
	for name, g := range map[string]Geometry{
		"standard": StandardGeometry(),
		"small":    SmallGeometry(),
		"large":    LargeGeometry(),
	} {
		once := g.Normalize()
		twice := once.Normalize()
		if !reflect.DeepEqual(once, twice) {
			t.Errorf("%s: Normalize not idempotent:\n once=%+v\n twice=%+v", name, once, twice)
		}
	}
}

func TestDefaultMatchSetupBuildsDefault(t *testing.T) {
	got, err := DefaultMatchSetup().Build()
	if err != nil {
		t.Fatalf("DefaultMatchSetup().Build(): %v", err)
	}
	def := Default()
	// The geometry/tuning/seed must reproduce the canonical Default exactly.
	if !reflect.DeepEqual(got.Geometry, def.Geometry) {
		t.Errorf("Build geometry != Default geometry")
	}
	if !reflect.DeepEqual(got.Tuning, def.Tuning) {
		t.Errorf("Build tuning != Default tuning")
	}
	if got.Seed != def.Seed {
		t.Errorf("Build seed %d != Default seed %d", got.Seed, def.Seed)
	}
}

func TestParseGameVersionAndHelp(t *testing.T) {
	opts, err := ParseGame("game", []string{"-version"}, io.Discard)
	if err != nil || !opts.Version {
		t.Errorf("-version: opts.Version=%v err=%v, want true,nil", opts.Version, err)
	}
	if _, err := ParseGame("game", []string{"-h"}, io.Discard); !errors.Is(err, ErrHelp) {
		t.Errorf("-h: err=%v, want ErrHelp", err)
	}
}

func TestParseGameUsageErrors(t *testing.T) {
	cases := [][]string{
		{"-team-size", "0"},                  // out of range
		{"-volume", "5"},                      // out of [0,1]
		{"-camera", "spinny"},                 // unknown camera
		{"-zone-enforce", "nope"},             // unknown enforcement
	}
	for _, args := range cases {
		if _, err := ParseGame("game", args, io.Discard); !errors.Is(err, ErrUsage) {
			t.Errorf("args %v: err=%v, want ErrUsage", args, err)
		}
	}
}

// TestParseGameDifficultyNotValidatedHere: config no longer owns difficulty validation (it
// moved to the cmd layer via control.ValidSkill), so config accepts any string and the cmd
// rejects an unknown one.
func TestParseGameDifficultyNotValidatedHere(t *testing.T) {
	opts, err := ParseGame("game", []string{"-difficulty", "bogus"}, io.Discard)
	if err != nil {
		t.Errorf("config should not reject difficulty %q anymore: %v", "bogus", err)
	}
	if opts.Difficulty != "bogus" {
		t.Errorf("difficulty = %q, want passthrough %q", opts.Difficulty, "bogus")
	}
}

func TestParseServerVersionAndUsage(t *testing.T) {
	opts, err := ParseServer("server", []string{"-version"}, io.Discard)
	if err != nil || !opts.Version {
		t.Errorf("-version: opts.Version=%v err=%v", opts.Version, err)
	}
	if _, err := ParseServer("server", []string{"-tick-rate", "9000"}, io.Discard); !errors.Is(err, ErrUsage) {
		t.Errorf("-tick-rate 9000: err=%v, want ErrUsage", err)
	}
}
