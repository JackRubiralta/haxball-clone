// Package audio plays the simulation's sound events on the Ebiten audio device. It is
// client-side only: the headless server and the deterministic simulation never import
// it. The simulation emits sim.SoundEvent values each tick; this package turns the ones
// the client drains into sounds, so audio is driven by deterministic events without the
// simulation depending on any audio library.
package audio

import (
	"bytes"
	"embed"
	"io"
	"log/slog"

	"github.com/hajimehoshi/ebiten/v2/audio"
	"github.com/hajimehoshi/ebiten/v2/audio/wav"

	"phootball/internal/sim"
)

//go:embed assets/*.wav
var assets embed.FS

const sampleRate = 44100

// Settings controls playback volume and muting.
type Settings struct {
	Volume float64
	Muted  bool
}

// Manager loads the embedded clips and plays them in response to sim events.
type Manager struct {
	ctx      *audio.Context
	clips    map[sim.SoundKind][]byte
	settings Settings
}

var assetFiles = map[sim.SoundKind]string{
	sim.SoundBallHit: "ballhit.wav",
	sim.SoundKick:    "kick.wav",
	sim.SoundTrap:    "trap.wav",
	sim.SoundGoal:    "goal.wav",
	sim.SoundWhistle: "whistle.wav",
}

// New creates a manager and decodes the embedded clips. Missing or undecodable assets
// are skipped with a logged warning, so the game still runs (silent for those sounds).
func New(s Settings) *Manager {
	m := &Manager{ctx: audio.NewContext(sampleRate), clips: map[sim.SoundKind][]byte{}, settings: s}
	for kind, name := range assetFiles {
		data, err := assets.ReadFile("assets/" + name)
		if err != nil {
			slog.Warn("audio asset missing", "file", name, "err", err)
			continue
		}
		stream, err := wav.DecodeWithoutResampling(bytes.NewReader(data))
		if err != nil {
			slog.Warn("audio decode failed", "file", name, "err", err)
			continue
		}
		pcm, err := io.ReadAll(stream)
		if err != nil {
			slog.Warn("audio read failed", "file", name, "err", err)
			continue
		}
		m.clips[kind] = pcm
	}
	return m
}

// SetVolume sets the master volume (0..1).
func (m *Manager) SetVolume(v float64) { m.settings.Volume = clamp01(v) }

// SetMuted mutes or unmutes all sound.
func (m *Manager) SetMuted(b bool) { m.settings.Muted = b }

// Dispatch plays every sound event from a tick.
func (m *Manager) Dispatch(events []sim.SoundEvent) {
	for _, e := range events {
		m.Play(e.Kind, e.Strength)
	}
}

// Play starts one sound at a volume scaled by the master volume and, for impacts, the
// event strength. Each call creates a short-lived player so overlapping sounds mix.
func (m *Manager) Play(kind sim.SoundKind, strength float64) {
	if m.settings.Muted || m.settings.Volume <= 0 {
		return
	}
	pcm, ok := m.clips[kind]
	if !ok {
		return
	}
	vol := m.settings.Volume
	switch kind {
	case sim.SoundBallHit, sim.SoundKick:
		s := strength / 300
		if s < 0.15 {
			s = 0.15
		} else if s > 1 {
			s = 1
		}
		vol *= s
	}
	p := m.ctx.NewPlayerFromBytes(pcm)
	p.SetVolume(clamp01(vol))
	p.Play()
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}
