package policy

// Dense is one fully-connected layer with row-major (out-major) weights, stored and computed
// entirely in float32. W has length In*Out laid out so the weights for output o occupy
// W[o*In : o*In+In]; B has length Out.
type Dense struct {
	In, Out int
	W       []float32
	B       []float32
}

// applyInto computes, for every output o,
//
//	dst[o] = act( sum_i W[o*In+i]*src[i] + B[o] )
//
// accumulating the dot product strictly left-to-right in a float32 accumulator (no float64
// widening, no FMA, no reordered/parallel reduction) so the result is bit-for-bit reproducible
// and matches the reference exporter. relu toggles the ReLU activation. The caller guarantees
// len(src) >= In and len(dst) >= Out; only the first In/Out elements are read/written.
func (d *Dense) applyInto(dst, src []float32, relu bool) {
	for o := 0; o < d.Out; o++ {
		base := o * d.In
		var acc float32
		for i := 0; i < d.In; i++ {
			acc += d.W[base+i] * src[i]
		}
		acc += d.B[o]
		if relu && acc < 0 {
			acc = 0
		}
		dst[o] = acc
	}
}
