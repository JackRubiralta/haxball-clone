// Package policy is a pure-Go, deterministic neural-network inference engine for the game's
// neural controller. It is game-agnostic: it imports nothing from phootball and exchanges
// plain []float32 with its caller. It uses no cgo, no BLAS/gonum, no ONNX, and no Ebiten,
// so it is safe to embed in the headless server and preserves the project's
// cross-platform-determinism and byte-exact-replay invariants.
//
// # Determinism contract (the load-bearing invariant)
//
// Forward is a deterministic function of its inputs and the loaded weights:
//
//   - All arithmetic is float32 end-to-end, accumulated in a float32 accumulator summed
//     strictly left-to-right in index order. There is no float64 widening, no math.FMA (Go
//     does not fuse multiply-add across statements; plain `acc += w*x` is safe), and no
//     reordered/parallel/pairwise/SIMD reduction. This is what lets the Go forward pass match
//     the Python reference exporter bit-for-bit (see internal/policy net_test.go parity test).
//   - Hidden activations are ReLU only (exact, no transcendentals). The encoder (Phi) and the
//     trunk are fully ReLU; the policy heads are linear. Decoding is argmax (ties -> lowest
//     index), so no output squashing is needed.
//   - No maps, no goroutines, no wall clock, no global mutable state are touched in Forward.
//
// # Architecture (Deep Sets)
//
// A shared per-entity encoder Phi maps each teammate/opponent feature row to a phiOut vector;
// the rows of each group are pooled with a permutation-invariant symmetric pool (sum and max,
// concatenated). The pooled teammate and opponent vectors are concatenated with the fixed-width
// self, ball, and global blocks and fed to the trunk MLP, whose output drives the factored
// policy heads. This handles variable rosters (1..11 players) natively.
//
// A read-only *Net (weights only) is shareable across many controllers; per-call scratch lives
// in a per-controller Workspace, so a single net serves every player without data races.
package policy
