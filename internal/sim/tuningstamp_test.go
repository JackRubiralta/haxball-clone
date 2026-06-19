package sim

import (
	"testing"

	"phootball/internal/config"
)

// TestApplyConfigStampsPlayerTuning: a custom config.Tuning.Player reaches every player's
// Tuning AND the de-normalized physics body (max speed, radius, mass -> InvMass), proving
// config.Tuning is authoritative over player physics (mirrors the ball restamp).
func TestApplyConfigStampsPlayerTuning(t *testing.T) {
	cfg := config.Default()
	cfg.Tuning.Player.MaxSpeed = 222
	cfg.Tuning.Player.Radius = 30
	cfg.Tuning.Player.Mass = 40
	cfg.Tuning.Player.Shoot = config.CurveSpec{Front: 1000, Back: 300}

	m := BuildMatchFromConfig(NewStandardField(), 3, cfg)
	for _, p := range m.Players {
		if p.Tuning.MaxSpeed != 222 {
			t.Errorf("player %d Tuning.MaxSpeed = %v, want 222", p.PlayerID, p.Tuning.MaxSpeed)
		}
		if p.Body.MaxSpeed != 222 {
			t.Errorf("player %d Body.MaxSpeed = %v, want 222 (body not re-synced)", p.PlayerID, p.Body.MaxSpeed)
		}
		if p.Radius() != 30 {
			t.Errorf("player %d Radius() = %v, want 30 (shape not re-synced)", p.PlayerID, p.Radius())
		}
		if got := 1 / p.Body.InvMass; got != 40 {
			t.Errorf("player %d mass = %v, want 40 (InvMass not re-synced)", p.PlayerID, got)
		}
		if p.Tuning.Shoot.Front != 1000 {
			t.Errorf("player %d Shoot.Front = %v, want 1000", p.PlayerID, p.Tuning.Shoot.Front)
		}
	}
}

// TestDefaultPlayerTuningRegression pins the canonical default profile (the values that now
// live solely in config/tuning.go): 140 max speed, radius 18, and the unified 575 shot front.
func TestDefaultPlayerTuningRegression(t *testing.T) {
	s := config.DefaultPlayerTuning()
	if s.MaxSpeed != 140 || s.Radius != 18 {
		t.Errorf("default speed/radius = %v/%v, want 140/18", s.MaxSpeed, s.Radius)
	}
	if s.Shoot.Front != 575 {
		t.Errorf("default Shoot.Front = %v, want 575 (the unified shot force)", s.Shoot.Front)
	}
}

// TestPossessionTuningReadFromConfig: a custom team-possession build window reaches the sim
// (the durations now live in config.Tuning.Possession, read off m.Tuning each tick).
func TestPossessionTuningReadFromConfig(t *testing.T) {
	cfg := config.Default()
	cfg.Tuning.Possession.BuildExponent = 1.0 // linear build instead of the default cubic
	m := BuildMatchFromConfig(NewStandardField(), 3, cfg)
	if got := m.teamBuildCurve(0.5); got != 0.5 {
		t.Errorf("with BuildExponent 1.0 the build curve should be linear: teamBuildCurve(0.5) = %v, want 0.5", got)
	}
}
