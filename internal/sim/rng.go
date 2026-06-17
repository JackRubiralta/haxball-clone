package sim

import "math/rand"

// newRNG returns a deterministic RNG seeded from the config seed. It is used only for
// coin tosses (kickoff side, penalty order) so the server and every client agree and a
// match can be reproduced from its seed.
func newRNG(seed int64) *rand.Rand {
	return rand.New(rand.NewSource(seed))
}

// coinToss returns a deterministic random side from the match RNG.
func (m *Match) coinToss() Side {
	if m.rng == nil || m.rng.Intn(2) == 0 {
		return SideLeft
	}
	return SideRight
}
