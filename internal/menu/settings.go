// Package menu is the client-side front end for the local game: a top-level state
// machine (main menu, settings, playing, pause, result) drawn with an immediate-mode UI
// over the render canvas. It is the only place outside cmd that reads keyboard/mouse
// input, and it is never imported by the headless server.
package menu

import (
	"phootball/internal/config"
	"phootball/internal/control"
	"phootball/internal/geom"
	"phootball/internal/input"
	"phootball/internal/sim"
)

// Settings is the in-memory, menu-editable configuration. It maps onto a config.Config
// when a match starts. Settings are NOT persisted between launches (v1); the command
// line is the persistent surface.
type Settings struct {
	Field    string
	TeamSize int
	Mode     string
	Minutes  float64
	WinScore int
	Offside  bool
	GKBox    bool
	Seed     int64
}

// DefaultSettings returns the starting menu configuration.
func DefaultSettings() Settings {
	return Settings{
		Field:    "standard",
		TeamSize: 3,
		Mode:     "quick",
		Minutes:  3,
		WinScore: 3,
		Seed:     1,
	}
}

var fieldPresets = []string{"standard", "small", "large"}
var modePresets = []string{"friendly", "quick", "timed", "cup", "golden"}

// Config converts the settings into a config.Config.
func (s Settings) Config() config.Config {
	cfg := config.Default()
	if g, ok := config.PresetByName(s.Field); ok {
		cfg.Geometry = g
	}
	if r, err := config.RulesetForMode(s.Mode, s.Minutes, s.WinScore); err == nil {
		if s.Offside {
			r.OffsideEnabled = true
			r.OffsideFrac = 2.0 / 3.0
		}
		if s.GKBox {
			r.GKBoxEnabled = true
			r.GKBoxMax = 1
		}
		cfg.Ruleset = r
	}
	cfg.Seed = s.Seed
	return cfg
}

// BuildMatch builds a fresh match and its controllers. practice is a single-player,
// friendly dribble session; otherwise it is a match against AI, with a local human on
// the blue team unless human is false (watch-AI).
func (s Settings) BuildMatch(practice, human bool) (*sim.Match, map[int]control.Controller) {
	cfg := s.Config()
	field := sim.NewFieldFromGeometry(cfg.Geometry)
	controllers := map[int]control.Controller{}

	if practice {
		m := sim.BuildSolo(field)
		for _, p := range m.Players {
			controllers[p.PlayerID] = input.NewHuman()
		}
		return m, controllers
	}

	field.AddObstacle(sim.NewConeObstacle(geom.NewVec(field.CenterSpot.X, field.Min.Y+120), 14))
	field.AddObstacle(sim.NewConeObstacle(geom.NewVec(field.CenterSpot.X, field.Max.Y-120), 14))
	m := sim.BuildMatchFromConfig(field, s.TeamSize, cfg)
	humanID := -1
	if human {
		humanID = humanSlot(m.Teams[0])
	}
	for _, p := range m.Players {
		if p.PlayerID == humanID {
			controllers[p.PlayerID] = input.NewHuman()
		} else {
			controllers[p.PlayerID] = control.NewAI(p.PlayerID)
		}
	}
	return m, controllers
}

func humanSlot(t *sim.Team) int {
	if len(t.Players) > 1 {
		return t.Players[1].PlayerID
	}
	return t.Players[0].PlayerID
}

func cycle(list []string, cur string, dir int) string {
	idx := 0
	for i, v := range list {
		if v == cur {
			idx = i
		}
	}
	idx = (idx + dir + len(list)) % len(list)
	return list[idx]
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
