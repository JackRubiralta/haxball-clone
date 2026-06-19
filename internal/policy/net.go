package policy

import "fmt"

// Format constants for the embedded weight file. See weights.go for the byte layout.
const (
	Magic          = "PHNNW1\x00\x00" // 8 bytes
	FormatVersion  = 1
	ArchDeepSetsV1 = 1
)

// Net holds the immutable weights of a Deep-Sets policy network. It is read-only after Load,
// so a single *Net is safely shared across many controllers; each controller owns a Workspace
// (NewWorkspace) that holds all per-call scratch.
type Net struct {
	EntDim    int // per-entity (teammate/opponent) feature-row width
	SelfDim   int // self block width
	BallDim   int // ball block width
	GlobalDim int // global/context block width
	PhiOut    int // per-entity encoder output width

	HeadSizes []int   // factored policy head widths, in order
	Phi       []Dense // per-entity encoder (shared by teammates and opponents), all ReLU
	Trunk     []Dense // policy trunk over the concatenated vector, all ReLU
	Heads     []Dense // factored output heads, linear

	ConcatDim      int // = SelfDim+BallDim+GlobalDim+4*PhiOut
	TrunkOut       int // = last(Trunk).Out
	TotalHeadWidth int // = sum(HeadSizes)
}

func last(d []Dense) Dense { return d[len(d)-1] }

// validate computes the derived dims and checks that every layer chains correctly. It fails
// loudly so a mismatched or corrupt weight file is rejected at Load, never silently misread.
func (n *Net) validate() error {
	if n.EntDim <= 0 || n.PhiOut <= 0 {
		return fmt.Errorf("%w: entDim=%d phiOut=%d", ErrShapeMismatch, n.EntDim, n.PhiOut)
	}
	if n.Phi[0].In != n.EntDim {
		return fmt.Errorf("%w: phi[0].in=%d entDim=%d", ErrShapeMismatch, n.Phi[0].In, n.EntDim)
	}
	for i := 1; i < len(n.Phi); i++ {
		if n.Phi[i].In != n.Phi[i-1].Out {
			return fmt.Errorf("%w: phi layer %d in=%d prev.out=%d", ErrShapeMismatch, i, n.Phi[i].In, n.Phi[i-1].Out)
		}
	}
	if last(n.Phi).Out != n.PhiOut {
		return fmt.Errorf("%w: phi last out=%d phiOut=%d", ErrShapeMismatch, last(n.Phi).Out, n.PhiOut)
	}

	n.ConcatDim = n.SelfDim + n.BallDim + n.GlobalDim + 4*n.PhiOut
	if n.Trunk[0].In != n.ConcatDim {
		return fmt.Errorf("%w: trunk[0].in=%d concatDim=%d", ErrShapeMismatch, n.Trunk[0].In, n.ConcatDim)
	}
	for i := 1; i < len(n.Trunk); i++ {
		if n.Trunk[i].In != n.Trunk[i-1].Out {
			return fmt.Errorf("%w: trunk layer %d in=%d prev.out=%d", ErrShapeMismatch, i, n.Trunk[i].In, n.Trunk[i-1].Out)
		}
	}
	n.TrunkOut = last(n.Trunk).Out

	if len(n.Heads) != len(n.HeadSizes) {
		return fmt.Errorf("%w: %d heads but %d head sizes", ErrShapeMismatch, len(n.Heads), len(n.HeadSizes))
	}
	n.TotalHeadWidth = 0
	for h := range n.Heads {
		if n.Heads[h].In != n.TrunkOut {
			return fmt.Errorf("%w: head[%d].in=%d trunkOut=%d", ErrShapeMismatch, h, n.Heads[h].In, n.TrunkOut)
		}
		if n.Heads[h].Out != n.HeadSizes[h] {
			return fmt.Errorf("%w: head[%d].out=%d headSize=%d", ErrShapeMismatch, h, n.Heads[h].Out, n.HeadSizes[h])
		}
		n.TotalHeadWidth += n.Heads[h].Out
	}
	return nil
}

// HeadOffsets returns the cumulative offsets of each head within the flat logits slice that
// Forward returns: head h occupies logits[offs[h]:offs[h+1]]. len == len(HeadSizes)+1.
func (n *Net) HeadOffsets() []int {
	offs := make([]int, len(n.HeadSizes)+1)
	for i, s := range n.HeadSizes {
		offs[i+1] = offs[i] + s
	}
	return offs
}

// Workspace holds all per-Forward scratch so a single read-only *Net can be shared across many
// controllers. One Workspace must not be used concurrently, but distinct Workspaces over a
// shared Net are fully independent. NewWorkspace sizes every buffer once, so Forward allocates
// nothing.
type Workspace struct {
	net          *Net
	phiScratch   [][]float32 // phiScratch[k] holds Phi[k]'s output
	trunkScratch [][]float32 // trunkScratch[k] holds Trunk[k]'s output
	concat       []float32   // the assembled trunk input
	logits       []float32   // the returned head logits
}

// NewWorkspace allocates per-controller scratch for n.
func (n *Net) NewWorkspace() *Workspace {
	ws := &Workspace{net: n}
	ws.phiScratch = make([][]float32, len(n.Phi))
	for k := range n.Phi {
		ws.phiScratch[k] = make([]float32, n.Phi[k].Out)
	}
	ws.trunkScratch = make([][]float32, len(n.Trunk))
	for k := range n.Trunk {
		ws.trunkScratch[k] = make([]float32, n.Trunk[k].Out)
	}
	ws.concat = make([]float32, n.ConcatDim)
	ws.logits = make([]float32, n.TotalHeadWidth)
	return ws
}

// phiForward runs one entity row through the shared encoder, returning Phi's final output
// (an alias into ws.phiScratch, valid until the next phiForward call).
func (ws *Workspace) phiForward(row []float32) []float32 {
	in := row
	for k := range ws.net.Phi {
		ws.net.Phi[k].applyInto(ws.phiScratch[k], in, true)
		in = ws.phiScratch[k]
	}
	return in
}

// pool writes the symmetric pool [sum(phiOut) ; max(phiOut)] of Phi over the entity rows into
// dst (len 2*PhiOut). An empty group pools to zeros. Because Phi's last layer is ReLU, entity
// encodings are >= 0, so max over an empty group is consistently 0. Sums and maxes are taken
// left-to-right in row order (the caller supplies a deterministic, ID-sorted order).
func (ws *Workspace) pool(rows []float32, dst []float32) {
	po := ws.net.PhiOut
	sum := dst[:po]
	mx := dst[po : 2*po]
	for j := 0; j < po; j++ {
		sum[j] = 0
		mx[j] = 0
	}
	ent := ws.net.EntDim
	count := 0
	if ent > 0 {
		count = len(rows) / ent
	}
	for e := 0; e < count; e++ {
		out := ws.phiForward(rows[e*ent : (e+1)*ent])
		for j := 0; j < po; j++ {
			sum[j] += out[j]
			if out[j] > mx[j] {
				mx[j] = out[j]
			}
		}
	}
}

// Forward runs one inference. self/ball/global are fixed-width blocks (len SelfDim/BallDim/
// GlobalDim). teammates/opponents are flattened entity rows (len == k*EntDim each, k >= 0,
// supplied in a deterministic order). It returns ws.logits (the flat factored-head logits),
// valid until the next Forward call on the same Workspace.
func (n *Net) Forward(ws *Workspace, self, ball, global, teammates, opponents []float32) []float32 {
	c := ws.concat
	off := 0
	copy(c[off:off+n.SelfDim], self)
	off += n.SelfDim
	copy(c[off:off+n.BallDim], ball)
	off += n.BallDim
	copy(c[off:off+n.GlobalDim], global)
	off += n.GlobalDim
	ws.pool(teammates, c[off:off+2*n.PhiOut])
	off += 2 * n.PhiOut
	ws.pool(opponents, c[off:off+2*n.PhiOut])

	in := c
	for k := range n.Trunk {
		n.Trunk[k].applyInto(ws.trunkScratch[k], in, true)
		in = ws.trunkScratch[k]
	}
	ho := 0
	for h := range n.Heads {
		n.Heads[h].applyInto(ws.logits[ho:ho+n.Heads[h].Out], in, false)
		ho += n.Heads[h].Out
	}
	return ws.logits
}
