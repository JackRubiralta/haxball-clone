// Command gen-weights writes a deterministic, randomly-initialized neural-net weights
// file in the internal/policy "PHNNW1" binary format. It exists only to bootstrap M0:
// internal/policy //go:embed-s weights/*.bin, so a valid file must exist before the
// package (or its tests) can build. The real, trained weights are produced later by the
// Python exporter (training/export.py), which overwrites this same file in-place.
//
// It deliberately does NOT import internal/policy (that package can't build until this
// file has produced its embedded weights), so the byte layout below is duplicated here as
// the bootstrap source of truth. Keep it in lockstep with internal/policy/weights.go and
// the Deep-Sets dims in internal/control/neural.
package main

import (
	"bufio"
	"encoding/binary"
	"flag"
	"log"
	"math"
	"math/rand"
	"os"
)

func main() {
	out := flag.String("out", "internal/policy/weights/neural_v1.bin", "output weights path")
	seed := flag.Int64("seed", 1, "deterministic RNG seed")
	flag.Parse()

	// Deep-Sets dims. MUST match internal/control/neural (features.go/action.go) and the
	// loader's expectations in internal/policy/weights.go.
	const (
		entDim    = 12
		selfDim   = 16
		ballDim   = 8
		globalDim = 12
		phiOut    = 32
		trunkHid  = 128
	)
	headSizes := []uint32{9, 3, 16, 4, 2} // MoveDir, Throttle, AimBin, Ability, Cancel
	concat := selfDim + ballDim + globalDim + 4*phiOut

	type ld struct{ in, out int }
	phi := []ld{{entDim, 32}, {32, phiOut}}
	trunk := []ld{{concat, trunkHid}, {trunkHid, trunkHid}}
	var heads []ld
	for _, h := range headSizes {
		heads = append(heads, ld{trunkHid, int(h)})
	}

	f, err := os.Create(*out)
	if err != nil {
		log.Fatalf("create: %v", err)
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	defer w.Flush()

	put32 := func(v uint32) {
		var b [4]byte
		binary.LittleEndian.PutUint32(b[:], v)
		if _, err := w.Write(b[:]); err != nil {
			log.Fatal(err)
		}
	}
	putf := func(v float32) {
		var b [4]byte
		binary.LittleEndian.PutUint32(b[:], math.Float32bits(v))
		if _, err := w.Write(b[:]); err != nil {
			log.Fatal(err)
		}
	}

	if _, err := w.Write([]byte{'P', 'H', 'N', 'N', 'W', '1', 0, 0}); err != nil {
		log.Fatal(err)
	}
	put32(1) // format version
	put32(1) // arch DeepSetsV1
	put32(entDim)
	put32(selfDim)
	put32(ballDim)
	put32(globalDim)
	put32(phiOut)
	put32(uint32(len(phi)))
	put32(uint32(len(trunk)))
	put32(uint32(len(headSizes)))
	for _, h := range headSizes {
		put32(h)
	}
	emit := func(l ld) { put32(uint32(l.in)); put32(uint32(l.out)) }
	for _, l := range phi {
		emit(l)
	}
	for _, l := range trunk {
		emit(l)
	}
	for _, l := range heads {
		emit(l)
	}

	r := rand.New(rand.NewSource(*seed))
	writeLayer := func(l ld) {
		// Xavier-ish uniform init, biases zero.
		s := float32(math.Sqrt(6.0 / float64(l.in+l.out)))
		for i := 0; i < l.in*l.out; i++ {
			putf((r.Float32()*2 - 1) * s)
		}
		for i := 0; i < l.out; i++ {
			putf(0)
		}
	}
	for _, l := range phi {
		writeLayer(l)
	}
	for _, l := range trunk {
		writeLayer(l)
	}
	for _, l := range heads {
		writeLayer(l)
	}
	log.Printf("wrote %s (entDim=%d self=%d ball=%d global=%d phiOut=%d heads=%v)",
		*out, entDim, selfDim, ballDim, globalDim, phiOut, headSizes)
}
