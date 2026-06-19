package policy

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
)

// Errors returned by Load/LoadBytes. They wrap a descriptive message; callers can match with
// errors.Is.
var (
	ErrBadMagic           = errors.New("policy: bad magic")
	ErrUnsupportedVersion = errors.New("policy: unsupported format version")
	ErrUnsupportedArch    = errors.New("policy: unsupported architecture")
	ErrShapeMismatch      = errors.New("policy: layer shape mismatch")
	ErrTruncated          = errors.New("policy: truncated weight data")
)

// Binary weight-file layout (little-endian throughout):
//
//	[8]byte  magic = "PHNNW1\0\0"
//	uint32   formatVersion (= FormatVersion)
//	uint32   archID        (= ArchDeepSetsV1)
//	uint32   entDim, selfDim, ballDim, globalDim, phiOut
//	uint32   nPhiLayers, nTrunkLayers, nHeads
//	[nHeads]uint32  head widths
//	layer table: for each layer in order [Phi..., Trunk..., Heads...]: uint32 in, uint32 out
//	weight blob:  for each layer in the same order: float32[in*out] W (out-major), float32[out] B
//
// The exporter (training/export.py) and the bootstrap cmd/gen-weights write exactly this.

type parser struct {
	b   []byte
	off int
	err bool
}

func (p *parser) u32() uint32 {
	if p.err || p.off+4 > len(p.b) {
		p.err = true
		return 0
	}
	v := binary.LittleEndian.Uint32(p.b[p.off:])
	p.off += 4
	return v
}

func (p *parser) take(n int) []byte {
	if p.err || n < 0 || p.off+n > len(p.b) {
		p.err = true
		return nil
	}
	s := p.b[p.off : p.off+n]
	p.off += n
	return s
}

func (p *parser) f32s(n int) []float32 {
	raw := p.take(n * 4)
	if p.err {
		return nil
	}
	out := make([]float32, n)
	for i := 0; i < n; i++ {
		out[i] = math.Float32frombits(binary.LittleEndian.Uint32(raw[i*4:]))
	}
	return out
}

// Load reads a weight file from r.
func Load(r io.Reader) (*Net, error) {
	b, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	return LoadBytes(b)
}

// LoadBytes parses a weight file from b, validating magic, version, architecture, and that
// every layer shape chains correctly. It fails loudly rather than silently misreading.
func LoadBytes(b []byte) (*Net, error) {
	p := &parser{b: b}
	magic := p.take(8)
	if p.err {
		return nil, ErrTruncated
	}
	if string(magic) != Magic {
		return nil, ErrBadMagic
	}
	ver := p.u32()
	arch := p.u32()
	if p.err {
		return nil, ErrTruncated
	}
	if ver != FormatVersion {
		return nil, fmt.Errorf("%w: got %d want %d", ErrUnsupportedVersion, ver, FormatVersion)
	}
	if arch != ArchDeepSetsV1 {
		return nil, fmt.Errorf("%w: got %d want %d", ErrUnsupportedArch, arch, ArchDeepSetsV1)
	}

	n := &Net{}
	n.EntDim = int(p.u32())
	n.SelfDim = int(p.u32())
	n.BallDim = int(p.u32())
	n.GlobalDim = int(p.u32())
	n.PhiOut = int(p.u32())
	nPhi := int(p.u32())
	nTrunk := int(p.u32())
	nHeads := int(p.u32())
	if p.err {
		return nil, ErrTruncated
	}
	if nPhi < 1 || nTrunk < 1 || nHeads < 1 {
		return nil, fmt.Errorf("%w: layer counts phi=%d trunk=%d heads=%d", ErrShapeMismatch, nPhi, nTrunk, nHeads)
	}

	n.HeadSizes = make([]int, nHeads)
	for i := range n.HeadSizes {
		n.HeadSizes[i] = int(p.u32())
	}

	total := nPhi + nTrunk + nHeads
	type shape struct{ in, out int }
	shapes := make([]shape, total)
	for i := 0; i < total; i++ {
		shapes[i] = shape{int(p.u32()), int(p.u32())}
	}
	if p.err {
		return nil, ErrTruncated
	}

	mk := func(s shape) (Dense, error) {
		if s.in <= 0 || s.out <= 0 {
			return Dense{}, fmt.Errorf("%w: non-positive layer %dx%d", ErrShapeMismatch, s.in, s.out)
		}
		w := p.f32s(s.in * s.out)
		bias := p.f32s(s.out)
		if p.err {
			return Dense{}, ErrTruncated
		}
		return Dense{In: s.in, Out: s.out, W: w, B: bias}, nil
	}

	idx := 0
	build := func(count int) ([]Dense, error) {
		ds := make([]Dense, count)
		for i := 0; i < count; i++ {
			d, err := mk(shapes[idx])
			if err != nil {
				return nil, err
			}
			ds[i] = d
			idx++
		}
		return ds, nil
	}

	var err error
	if n.Phi, err = build(nPhi); err != nil {
		return nil, err
	}
	if n.Trunk, err = build(nTrunk); err != nil {
		return nil, err
	}
	if n.Heads, err = build(nHeads); err != nil {
		return nil, err
	}

	if err := n.validate(); err != nil {
		return nil, err
	}
	return n, nil
}
