package sim

import (
	"math"
	"testing"

	"phootball/internal/config"
	"phootball/internal/geom"
)

// TestDirectionalSpeedMul pins the directional speed curve: full ahead, MoveSide at 90deg off
// facing, MoveBack straight back, and no penalty (1) when idle.
func TestDirectionalSpeedMul(t *testing.T) {
	tun := config.Tuning{MoveForward: 1.0, MoveSide: 0.8, MoveBack: 0.5}
	facing := geom.NewVec(1, 0)
	cases := []struct {
		name string
		move geom.Vec
		want float64
	}{
		{"forward", geom.NewVec(1, 0), 1.0},
		{"side+", geom.NewVec(0, 1), 0.8},
		{"side-", geom.NewVec(0, -1), 0.8},
		{"back", geom.NewVec(-1, 0), 0.5},
		{"idle", geom.Vec{}, 1.0}, // not moving -> no penalty
	}
	for _, c := range cases {
		if got := directionalSpeedMul(c.move, facing, tun); math.Abs(got-c.want) > 1e-9 {
			t.Errorf("%s: directionalSpeedMul=%.4f, want %.4f", c.name, got, c.want)
		}
	}
	// Monotonic between the anchors: a 45deg move sits between forward and side.
	mid := directionalSpeedMul(geom.NewVec(1, 1), facing, tun)
	if !(mid < tun.MoveForward && mid > tun.MoveSide) {
		t.Errorf("45deg multiplier %.4f should be between forward %.2f and side %.2f", mid, tun.MoveForward, tun.MoveSide)
	}
}

// TestMoveRelativeToFacing pins the heading-locked frame: W (screen-up) drives along facing, S
// backward, A/D strafe -- and the rotation preserves magnitude (it is orthonormal).
func TestMoveRelativeToFacing(t *testing.T) {
	facing := geom.NewVec(1, 0) // east; in the Y-down world, "right of east" is +Y (south)
	type tc struct {
		name       string
		key, world geom.Vec
	}
	for _, c := range []tc{
		{"W forward", geom.NewVec(0, -1), geom.NewVec(1, 0)},
		{"S back", geom.NewVec(0, 1), geom.NewVec(-1, 0)},
		{"D right", geom.NewVec(1, 0), geom.NewVec(0, 1)},
		{"A left", geom.NewVec(-1, 0), geom.NewVec(0, -1)},
	} {
		got := moveRelativeToFacing(c.key, facing)
		if geom.Norm(got.Sub(c.world)) > 1e-9 {
			t.Errorf("%s: got %v, want %v", c.name, got, c.world)
		}
	}
	// Magnitude preserved for an arbitrary diagonal and facing.
	in := geom.NewVec(1, -1)
	out := moveRelativeToFacing(in, geom.NewVec(0.6, 0.8))
	if d := math.Abs(geom.Norm(out) - geom.Norm(in)); d > 1e-9 {
		t.Errorf("rotation changed magnitude by %.6f", d)
	}
	// A zero move or facing passes through unchanged.
	if moveRelativeToFacing(geom.Vec{}, facing) != (geom.Vec{}) {
		t.Error("zero move should pass through")
	}
}
