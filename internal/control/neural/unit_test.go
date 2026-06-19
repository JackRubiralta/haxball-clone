package neural

import (
	"testing"
)

// TestAimBinForRel: bins stay in range, are monotonic in the relative angle, and the center
// region maps near the middle (straight-ahead) bins.
func TestAimBinForRel(t *testing.T) {
	for _, rel := range []float64{-10, -AimArcMax, -0.1, 0, 0.1, AimArcMax, 10} {
		i := aimBinForRel(rel)
		if i < 0 || i >= AimBins {
			t.Fatalf("rel %v -> bin %d out of [0,%d)", rel, i, AimBins)
		}
	}
	if aimBinForRel(-AimArcMax) >= aimBinForRel(AimArcMax) {
		t.Fatal("aimBinForRel not increasing across the arc")
	}
	mid := aimBinForRel(0)
	if mid < AimBins/2-1 || mid > AimBins/2 {
		t.Fatalf("straight-ahead bin %d not near center %d", mid, AimBins/2)
	}
}

// TestThrottleBin maps to the three discrete levels.
func TestThrottleBin(t *testing.T) {
	cases := map[float64]int{0: 0, 0.1: 0, 0.5: 1, 0.6: 1, 1.0: 2, 0.9: 2}
	for v, want := range cases {
		if got := throttleBin(v); got != want {
			t.Errorf("throttleBin(%v) = %d, want %d", v, got, want)
		}
	}
}

// TestHeadSizesMatchPolicyExpectation: the action factorization must sum to the embedded net's
// total head width (kept in lockstep with cmd/gen-weights and the exporter).
func TestHeadSizesSum(t *testing.T) {
	if TotalLogits() != 34 {
		t.Fatalf("TotalLogits = %d, want 34", TotalLogits())
	}
	off := headOffsets()
	if off[len(off)-1] != TotalLogits() {
		t.Fatalf("headOffsets last = %d, want %d", off[len(off)-1], TotalLogits())
	}
}
