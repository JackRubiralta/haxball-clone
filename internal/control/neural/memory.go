package neural

import "phootball/internal/geom"

// frameMemory recovers per-player velocity from successive observed positions held in the
// controller's OWN memory. This is cheat-safe: ObservedView exposes no velocity, and a human
// likewise infers motion across rendered frames. It is keyed by player ID and only ever
// point-looked-up (never range-iterated for a decision), so it adds no map-iteration
// nondeterminism. After the first sighting of each player the map stops growing.
type frameMemory struct {
	m       map[int]*tracker
	maxStep float64 // a single-tick position jump beyond this is treated as a teleport (kickoff reset)
}

type tracker struct {
	prevPos, curPos   geom.Vec
	prevTick, curTick uint64
	have              int // 0, 1, or 2 samples
}

func newFrameMemory(maxStep float64) *frameMemory {
	return &frameMemory{m: make(map[int]*tracker, 24), maxStep: maxStep}
}

// observe records player id at pos on the given tick. Re-observing the same tick is a no-op.
// A jump larger than maxStep (a kickoff teleport) discards the stale sample so the finite
// difference never invents a huge velocity.
func (fm *frameMemory) observe(id int, pos geom.Vec, tick uint64) {
	t := fm.m[id]
	if t == nil {
		t = &tracker{}
		fm.m[id] = t
	}
	if t.have > 0 && t.curTick == tick {
		return
	}
	if t.have > 0 && geom.Dist(pos, t.curPos) > fm.maxStep {
		t.have = 1
		t.curPos, t.curTick = pos, tick
		return
	}
	t.prevPos, t.prevTick = t.curPos, t.curTick
	t.curPos, t.curTick = pos, tick
	if t.have < 2 {
		t.have++
	}
}

// estVel returns the finite-difference world velocity from the two most recent distinct-tick
// samples, or the zero vector if there is not yet enough history.
func (fm *frameMemory) estVel(id int, dt float64) geom.Vec {
	t := fm.m[id]
	if t == nil || t.have < 2 || t.curTick <= t.prevTick {
		return geom.Vec{}
	}
	span := float64(t.curTick-t.prevTick) * dt
	if span <= 0 {
		return geom.Vec{}
	}
	return t.curPos.Sub(t.prevPos).Scale(1 / span)
}
