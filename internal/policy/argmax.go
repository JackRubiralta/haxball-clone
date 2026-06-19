package policy

// Argmax returns the index of the largest element of xs, resolving ties to the LOWEST index
// (a later element must be strictly greater to win). This is the canonical decode rule shared
// by the Go controller and the Python trainer so the two agree on every action. xs must be
// non-empty.
func Argmax(xs []float32) int {
	best := 0
	for i := 1; i < len(xs); i++ {
		if xs[i] > xs[best] {
			best = i
		}
	}
	return best
}
