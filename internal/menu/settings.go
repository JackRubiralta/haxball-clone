// Package menu is the client-side front end for the local game: a top-level state
// machine (main menu, pre-match setup, settings, playing, pause, result) drawn with an
// immediate-mode UI over the render canvas. It is the only place outside cmd that reads
// keyboard/mouse input, and it is never imported by the headless server.
package menu

import (
	"phootball/internal/config"
	"phootball/internal/control"
	"phootball/internal/input"
	"phootball/internal/sim"
)

// matchKind is the play mode chosen from the main menu.
type matchKind int

const (
	kindVsAI matchKind = iota
	kindPractice
	kindWatchAI
)

// Settings is the in-memory, lobby-editable match configuration: the shared
// config.MatchSetup (the single mapping to a config.Config) plus the AI difficulty (a
// controller concern that does not belong in the config layer). Not persisted (v1).
type Settings struct {
	config.MatchSetup
	Difficulty string // AI difficulty tier (see control.SkillFromString)
}

// DefaultSettings returns the lobby's starting configuration: a quick best-of-3 on the
// standard pitch with both boxes, no positional limits, hard AI.
func DefaultSettings() Settings {
	ms := config.DefaultMatchSetup()
	ms.Mode = "quick"
	ms.WinScore = 3
	s := Settings{MatchSetup: ms, Difficulty: "hard"}
	s.seedSizesFromField()
	return s
}

// AppPrefs is the global, app-level configuration edited on the Settings screen: camera
// and audio. In-memory only (v1).
type AppPrefs struct {
	CameraMode string // fit, ball, player
	Zoom       float64
	Volume     float64
	Muted      bool
}

// DefaultAppPrefs returns the default camera/audio preferences.
func DefaultAppPrefs() AppPrefs {
	return AppPrefs{CameraMode: "ball", Zoom: 2, Volume: 0.8}
}

var (
	fieldPresets      = []string{"standard", "small", "large"}
	modePresets       = []string{"friendly", "quick", "timed", "cup", "golden"}
	difficultyPresets = []string{"easy", "normal", "hard"}
	cameraPresets     = []string{"ball", "player", "fit"}
	fracPresets       = []float64{0.5, 0.6, 2.0 / 3.0, 0.75}
)

// seedSizesFromField fills the geometry size fields from the chosen preset, so the lobby
// edits absolute sizes (and switching presets resets them to that preset's values).
func (s *Settings) seedSizesFromField() {
	g, ok := config.PresetByName(s.Field)
	if !ok {
		return
	}
	// Seed only sizes the lobby edits (mouth width + box dimensions). The play area and
	// goal-pocket depth stay at 0 (= inherit the preset) so they never trigger the
	// surface re-derive in MatchSetup.Geometry, which would shift the standard pitch.
	s.GoalWidth = g.GoalMouthWidth
	s.PenaltyWidth = g.PenaltyWidth
	s.PenaltyDepth = g.PenaltyDepth
	s.GoalAreaWidth = g.GoalAreaWidth
	s.GoalAreaDepth = g.GoalAreaDepth
}

// Config builds the config.Config for this match via the single mapping.
func (s Settings) Config() config.Config {
	cfg, err := s.MatchSetup.Build()
	if err != nil {
		return config.Default()
	}
	return cfg
}

// BuildMatch builds a fresh match and its controllers. practice is a single-player
// friendly dribble session; otherwise it is a match against AI at the chosen difficulty,
// with a local human on the blue team unless human is false (watch-AI).
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

	m := sim.BuildMatchFromConfig(field, s.TeamSize, cfg)
	skill, _ := control.SkillFromString(s.Difficulty)
	humanID := -1
	if human {
		humanID = humanSlot(m.Teams[0])
	}
	for _, p := range m.Players {
		if p.PlayerID == humanID {
			controllers[p.PlayerID] = input.NewHuman()
		} else {
			controllers[p.PlayerID] = control.NewAISkill(p.PlayerID, skill)
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

// cycleFrac steps to the next/prev preset offside fraction nearest to cur.
func cycleFrac(cur float64, dir int) float64 {
	idx, best := 0, 1e9
	for i, v := range fracPresets {
		if d := absF(v - cur); d < best {
			best, idx = d, i
		}
	}
	idx = (idx + dir + len(fracPresets)) % len(fracPresets)
	return fracPresets[idx]
}

func absF(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
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

func clampF(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
