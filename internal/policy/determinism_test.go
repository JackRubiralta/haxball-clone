package policy

import (
	"hash/fnv"
	"math"
	"testing"
)

// TestForwardDeterministic asserts Forward is a pure deterministic function of its inputs and
// weights: two independent Workspaces over the same shared Net, and repeated calls on one
// Workspace, all produce bit-identical logits. This guards against accidental float64
// widening, reduction-order changes, or scratch aliasing (the bit-exact Go<->Python parity is
// the complementary cross-implementation guard, in TestForwardGoldenVector).
func TestForwardDeterministic(t *testing.T) {
	n, err := LoadDefault()
	if err != nil {
		t.Fatalf("LoadDefault: %v", err)
	}
	ws1 := n.NewWorkspace()
	ws2 := n.NewWorkspace()

	for _, tc := range []struct{ k, m int }{{0, 0}, {1, 1}, {3, 4}, {10, 11}} {
		self, ball, global, team, opp := detInput(n, int64(tc.k*100+tc.m), tc.k, tc.m)
		a := append([]float32(nil), n.Forward(ws1, self, ball, global, team, opp)...)
		b := append([]float32(nil), n.Forward(ws2, self, ball, global, team, opp)...)
		// Re-run on ws1 to ensure no first-call state leaked.
		c := append([]float32(nil), n.Forward(ws1, self, ball, global, team, opp)...)
		for i := range a {
			if math.Float32bits(a[i]) != math.Float32bits(b[i]) {
				t.Fatalf("k=%d m=%d logit %d: ws1 %08x != ws2 %08x", tc.k, tc.m, i, math.Float32bits(a[i]), math.Float32bits(b[i]))
			}
			if math.Float32bits(a[i]) != math.Float32bits(c[i]) {
				t.Fatalf("k=%d m=%d logit %d: call1 %08x != call2 %08x", tc.k, tc.m, i, math.Float32bits(a[i]), math.Float32bits(c[i]))
			}
		}
	}
}

// TestForwardStableHash hashes Forward's output bytes over a fixed input sweep. The value is
// not pinned to a constant here (the M0 weights are bootstrap-random and will be replaced by
// the trained export); the test asserts the hash is reproducible within the run, which is the
// determinism property we can guarantee without coupling to a specific weight file.
func TestForwardStableHash(t *testing.T) {
	n, err := LoadDefault()
	if err != nil {
		t.Fatalf("LoadDefault: %v", err)
	}
	digest := func() uint64 {
		ws := n.NewWorkspace()
		h := fnv.New64a()
		var buf [4]byte
		for k := 0; k <= 6; k++ {
			self, ball, global, team, opp := detInput(n, int64(k+1), k, k+1)
			out := n.Forward(ws, self, ball, global, team, opp)
			for _, v := range out {
				bits := math.Float32bits(v)
				buf[0] = byte(bits)
				buf[1] = byte(bits >> 8)
				buf[2] = byte(bits >> 16)
				buf[3] = byte(bits >> 24)
				_, _ = h.Write(buf[:])
			}
		}
		return h.Sum64()
	}
	if digest() != digest() {
		t.Fatal("Forward digest not reproducible across fresh workspaces")
	}
}
