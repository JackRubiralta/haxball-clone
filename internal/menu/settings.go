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

// TeamControl describes who plays one team and how. A team is either driven by a local
// human (at slot HumanSlot, a 1-based jersey-style index into its roster) with the rest
// of the roster filled by AI, or it is entirely AI at the given Difficulty. Size is the
// team's roster size (min 1). This is a controller/lobby concern and never leaks into
// config, which stays renderer- and AI-agnostic.
type TeamControl struct {
	Human      bool   // true: a local human plays this team
	HumanSlot  int    // 1-based slot the human occupies (clamped to [1, Size])
	Difficulty string // AI difficulty tier for the non-human players (see control.SkillFromString)
	Size       int    // roster size (min 1)
}

// Team side indices into Settings.Teams.
const (
	teamHome = 0 // Blue / left
	teamAway = 1 // Red / right
)

// Settings is the in-memory, lobby-editable match configuration: the shared
// config.MatchSetup (the single mapping to a config.Config) plus per-team control (who
// plays each team and at what difficulty -- a controller concern that does not belong in
// the config layer). Not persisted (v1).
type Settings struct {
	config.MatchSetup
	Teams [2]TeamControl // [teamHome] = Blue, [teamAway] = Red
}

// DefaultSettings returns the lobby's starting configuration: a 3v3 on the standard pitch
// with both boxes, decided by first-to-3 goals or a 5-minute clock, with the local human
// playing Blue at slot 2 and Red controlled by a hard AI.
func DefaultSettings() Settings {
	ms := config.DefaultMatchSetup()
	ms.WinByGoals, ms.WinScore = true, 3
	ms.WinByTime, ms.Minutes = true, 5
	s := Settings{
		MatchSetup: ms,
		Teams: [2]TeamControl{
			teamHome: {Human: true, HumanSlot: 2, Difficulty: "hard", Size: 3},
			teamAway: {Human: false, HumanSlot: 1, Difficulty: "hard", Size: 3},
		},
	}
	s.syncSizes()
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
	difficultyPresets = []string{"easy", "normal", "hard"}
	cameraPresets     = []string{"ball", "player", "fit"}
	controlPresets    = []string{"Human", "AI"}
)

// syncSizes mirrors the per-team sizes into the config.MatchSetup (which the geometry/
// validation layer reads via HomeSize/AwaySize).
func (s *Settings) syncSizes() {
	s.HomeSize = s.Teams[teamHome].Size
	s.AwaySize = s.Teams[teamAway].Size
}

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

// ClampDependents enforces the relational constraints live after every edit so a
// menu-built setup always stays inside the validator's envelope:
//   - sizes mirrored into the config; human slot within [1, team size];
//   - goal width <= goal-area width <= penalty width (and depths);
//   - pitch length (PlayWidth) >= pitch width (PlayHeight) when both are overridden.
//
// It nudges the looser bound up rather than clamping the just-edited value down, so the
// box nesting reads naturally as the player widens an inner box.
func (s *Settings) ClampDependents() {
	s.syncSizes()
	for i := range s.Teams {
		if s.Teams[i].Size < 1 {
			s.Teams[i].Size = 1
		}
		if s.Teams[i].HumanSlot < 1 {
			s.Teams[i].HumanSlot = 1
		}
		if s.Teams[i].HumanSlot > s.Teams[i].Size {
			s.Teams[i].HumanSlot = s.Teams[i].Size
		}
	}
	s.syncSizes()

	// Width nesting: goal mouth <= goal-area <= penalty-area.
	if s.GoalArea && s.GoalAreaWidth < s.GoalWidth {
		s.GoalAreaWidth = s.GoalWidth
	}
	if s.PenaltyArea {
		floor := s.GoalWidth
		if s.GoalArea && s.GoalAreaWidth > floor {
			floor = s.GoalAreaWidth
		}
		if s.PenaltyWidth < floor {
			s.PenaltyWidth = floor
		}
	}
	// Depth nesting: goal pocket <= goal-area <= penalty-area. GoalDepth defaults to 0
	// (inherit the preset pocket), so only nest the editable box depths here.
	if s.PenaltyArea && s.GoalArea && s.PenaltyDepth < s.GoalAreaDepth {
		s.PenaltyDepth = s.GoalAreaDepth
	}
	// Pitch proportions only when both dimensions are overridden (0 = inherit preset).
	if s.PlayWidth > 0 && s.PlayHeight > 0 && s.PlayWidth < s.PlayHeight {
		s.PlayWidth = s.PlayHeight
	}

	// Win/draw fields: keep the validator's envelope. A shootout needs a positive best-of
	// (the stepper's 0 reads as "default"); golden goal is a no-op without extra time.
	if s.Penalties && s.PenaltyBestOf < 1 {
		s.PenaltyBestOf = 5
	}
	if !s.Penalties {
		s.PenaltyBestOf = 0
	}
	if !s.ExtraTime {
		s.GoldenGoal = false
	}
	if !s.GoldenGoal {
		s.GoldenGoalCapped = false
	}
}

// SeedCLI seeds the per-team control from the legacy command-line flags: both teams take
// the given roster size and AI difficulty, keeping the default human-on-Blue layout. This
// keeps the CLI's -size/-difficulty flags meaningful while the lobby owns the rest.
func (s *Settings) SeedCLI(size int, difficulty string) {
	if size > 0 {
		s.Teams[teamHome].Size = size
		s.Teams[teamAway].Size = size
	}
	if difficulty != "" {
		s.Teams[teamHome].Difficulty = difficulty
		s.Teams[teamAway].Difficulty = difficulty
	}
	s.ClampDependents()
	s.seedSizesFromField()
}

// Validate reports the first relational/range error in the resolved setup, delegating to
// the single config-layer validator. The menu gates Start on this.
func (s Settings) Validate() error { return s.MatchSetup.Validate() }

// Config builds the config.Config for this match via the single mapping.
func (s Settings) Config() config.Config {
	cfg, err := s.MatchSetup.Build()
	if err != nil {
		return config.Default()
	}
	return cfg
}

// BuildMatch builds a fresh match and its controllers from the per-team control model.
// Each team gets a human at its chosen slot (when Human is set) with the rest of the
// roster filled by control.NewAISkill at that team's difficulty; a both-AI configuration
// is a "watch" match with no human. The legacy practice/human flags are accepted for
// call-site compatibility: practice still builds a solo dribble session, and a false
// human forces both teams to AI regardless of the per-team control.
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

	home, away := s.Teams[teamHome].Size, s.Teams[teamAway].Size
	if home < 1 {
		home = 1
	}
	if away < 1 {
		away = 1
	}
	m := sim.BuildMatchFromConfigSized(field, home, away, cfg)

	// humanID is the single local human's PlayerID, or -1 for a watch match. With two
	// human teams configured we still drive only one local player (the home team's),
	// matching the single-keyboard input model.
	humanID := -1
	for ti, t := range []*sim.Team{m.Teams[teamHome], m.Teams[teamAway]} {
		tc := s.Teams[ti]
		if tc.Human && human && humanID < 0 {
			humanID = slotPlayerID(t, tc.HumanSlot)
		}
		skill, _ := control.SkillFromString(tc.Difficulty)
		for _, p := range t.Players {
			if p.PlayerID == humanID {
				controllers[p.PlayerID] = input.NewHuman()
			} else {
				controllers[p.PlayerID] = control.NewAISkill(p.PlayerID, skill)
			}
		}
	}
	return m, controllers
}

// slotPlayerID maps a 1-based human slot to a PlayerID on the team, clamped to the
// roster. Slot 1 is the keeper (jersey 1); the default lobby seeds slot 2 (an outfielder)
// so the human is not forced into goal.
func slotPlayerID(t *sim.Team, slot int) int {
	if len(t.Players) == 0 {
		return -1
	}
	idx := slot - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(t.Players) {
		idx = len(t.Players) - 1
	}
	return t.Players[idx].PlayerID
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

// indexOf returns the index of cur in list, or 0 if absent (for segmented controls).
func indexOf(list []string, cur string) int {
	for i, v := range list {
		if v == cur {
			return i
		}
	}
	return 0
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
