package sim

import "phootball/internal/geom"

// SoundKind identifies a gameplay sound the client may play. The simulation only emits
// these events; it never plays audio itself, so the deterministic, headless core never
// imports an audio library.
type SoundKind int

const (
	SoundNone SoundKind = iota
	SoundBallHit
	SoundKick
	SoundTrap
	SoundGoal
	SoundWhistle
)

// SoundEvent is one sound the simulation emitted this tick. Strength (roughly an impact
// speed or 0..1 intensity) lets the client scale volume; Pos allows future panning.
type SoundEvent struct {
	Kind     SoundKind
	Strength float64
	Pos      geom.Vec
}

// ballHitMinSpeed is the impact speed below which a ball contact is silent, so resting
// or gently rolling contact does not chatter.
const ballHitMinSpeed = 40.0

// emit appends a sound event for this tick.
func (m *Match) emit(kind SoundKind, strength float64, pos geom.Vec) {
	m.sounds = append(m.sounds, SoundEvent{Kind: kind, Strength: strength, Pos: pos})
}

// Sounds returns the events emitted this tick (cleared at the top of the next Step).
func (m *Match) Sounds() []SoundEvent { return m.sounds }

// DrainEvents returns this tick's events and clears the buffer, so a caller (the local
// game, or the server broadcasting a snapshot) consumes each event exactly once.
func (m *Match) DrainEvents() []SoundEvent {
	out := m.sounds
	m.sounds = nil
	return out
}
