package control

import "math"

// Deterministic noise. The AI must stay deterministic (the server is authoritative and
// tests replay matches), so it never touches a random source. Instead it derives all
// "chaos" from a stateless hash of replicated state -- the player id, the current tick,
// and a channel selector -- which is identical on every machine and every replay.

// hash64 is a SplitMix64 finalizer: a fast, well-mixed integer hash.
func hash64(x uint64) uint64 {
	x += 0x9e3779b97f4a7c15
	x = (x ^ (x >> 30)) * 0xbf58476d1ce4e5b9
	x = (x ^ (x >> 27)) * 0x94d049bb133111eb
	return x ^ (x >> 31)
}

// noise returns a deterministic value in [-1, 1) that varies with the player, the tick,
// and the channel. Use channel to draw independent streams (aim vs movement vs timing).
func noise(id int, tick uint64, channel uint64) float64 {
	h := hash64(uint64(int64(id))*0x100000001b3 ^ tick*0xff51afd7ed558ccd ^ channel*0xc4ceb9fe1a85ec53)
	return float64(h>>11)/float64(uint64(1)<<53)*2 - 1
}

// personality is a per-player constant bias in [-1, 1] derived once from the id, used to
// give each player a stable temperament (aggression, risk appetite) without per-tick
// cost. channel selects which trait.
func personality(id int, channel uint64) float64 {
	h := hash64(uint64(int64(id))*0x2545f4914f6cdd1d ^ channel*0x9e3779b97f4a7c15)
	return float64(h>>11)/float64(uint64(1)<<53)*2 - 1
}

// gaussian returns an approximately normal deterministic value (mean 0, sd 1) by summing
// independent uniform noise streams (central limit), for aim/timing error.
func gaussian(id int, tick uint64, channel uint64) float64 {
	const k = 3
	sum := 0.0
	for i := uint64(0); i < k; i++ {
		sum += noise(id, tick, channel*97+i)
	}
	return sum / math.Sqrt(float64(k)/3.0) // scale so variance ~= 1
}
