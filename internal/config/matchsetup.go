package config

import "fmt"

// MatchSetup is the flat, plain-data description of every match option a player can
// choose: team size, pitch and goal and box dimensions (and whether each box exists),
// the positional rules, and the win/draw conditions. It is the single mapping the
// command line and the pre-match lobby both feed, so the two never drift. Zero-valued
// dimension overrides mean "keep the field preset's value".
type MatchSetup struct {
	TeamSize int    // back-compat seed: per-team size when HomeSize/AwaySize are 0
	HomeSize int    // players on the home (left/Blue) team (0 = inherit TeamSize)
	AwaySize int    // players on the away (right/Red) team (0 = inherit TeamSize)
	Field    string // preset name (standard, small, large)

	// Geometry overrides (0 = inherit the preset).
	PlayWidth, PlayHeight float64
	GoalWidth, GoalDepth  float64
	CenterCircleRadius    float64 // centre-circle radius override (0 = inherit the preset)

	// Boxes: existence + size overrides + per-box caps (0 = no cap). Each box caps the
	// defending team and the opponent (attackers) separately.
	PenaltyArea        bool
	PenaltyWidth       float64
	PenaltyDepth       float64
	PenaltyBoxMax      int // defending players allowed in the penalty area
	PenaltyBoxMaxOpp   int // opponent (attacking) players allowed in the penalty area
	GoalArea           bool
	GoalAreaWidth      float64
	GoalAreaDepth      float64
	GoalAreaMax        int  // defending players allowed in the goal area
	GoalAreaMaxOpp     int  // opponent (attacking) players allowed in the goal area
	GoalAreaKeeperOnly bool // if set, only the box-owner's keeper may enter the goal area (overrides the numeric goal-area caps)

	// Positional rules.
	Offside     bool
	OffsideFrac float64 // 0 -> 2/3
	Enforcement EnforcementMode
	EvictGrace  float64

	// Win / draw conditions. The two win conditions are orthogonal and may combine: both
	// on is "first to N goals OR the clock, whichever comes first"; neither on is a
	// never-ending friendly. The draw resolution (extra time, golden goal, penalties) only
	// applies when regulation ends level.
	WinByGoals       bool    // end early once a team reaches WinScore goals
	WinScore         int     // goals needed to win when WinByGoals
	WinByTime        bool    // end when the regulation clock expires
	Minutes          float64 // regulation length in minutes when WinByTime
	ExtraTime        bool    // if drawn at regulation, play extra time
	ExtraMinutes     float64 // length of extra time in minutes (also the golden-goal cap when GoldenGoalCapped)
	GoldenGoal       bool    // modifier: when ExtraTime is on, extra time is sudden death (next goal wins)
	GoldenGoalCapped bool    // modifier: when GoldenGoal is on, cap sudden death at ExtraMinutes (else it runs until a goal)
	Penalties        bool    // if still drawn, decide on a shootout (DIRECT pens when ExtraTime is off)
	PenaltyBestOf    int     // kicks per side in a shootout (0 = the default of 5)

	Seed int64

	// Tuning is the per-match physics/gameplay tuning -- the player profile, ball/world physics,
	// and team-possession timings (the values the in-menu "Tuning" tab edits). It is pure data,
	// so it travels over the gob wire to the LAN server. A ZERO Tuning means "use the default"
	// (see Build); DefaultMatchSetup seeds it with DefaultTuning so the menu starts from the
	// baseline and edits a copy.
	Tuning Tuning
}

// sizes returns the resolved per-team roster sizes, falling back to TeamSize whenever a
// per-team override is left at zero. It is the single place the two size conventions meet.
func (s MatchSetup) sizes() (home, away int) {
	home, away = s.HomeSize, s.AwaySize
	if home <= 0 {
		home = s.TeamSize
	}
	if away <= 0 {
		away = s.TeamSize
	}
	return home, away
}

// DefaultMatchSetup reproduces the original default match (standard pitch, friendly
// rules, both boxes, no positional limits), so DefaultMatchSetup().Build() equals the
// legacy Default() config.
func DefaultMatchSetup() MatchSetup {
	return MatchSetup{
		TeamSize:     3,
		Field:        "standard",
		PenaltyArea:  true,
		GoalArea:     true,
		Minutes:      3,
		WinScore:     3,
		ExtraMinutes: 1,
		OffsideFrac:  2.0 / 3.0,
		Seed:         1,
		Tuning:       DefaultTuning(),
	}
}

// Validate checks the option ranges.
func (s MatchSetup) Validate() error {
	if _, ok := PresetByName(s.Field); !ok {
		return fmt.Errorf("unknown field preset %q (want standard, small, large, or custom)", s.Field)
	}
	home, away := s.sizes()
	if home < 1 || home > 11 {
		return fmt.Errorf("home team size must be between 1 and 11")
	}
	if away < 1 || away > 11 {
		return fmt.Errorf("away team size must be between 1 and 11")
	}
	if s.OffsideFrac < 0 || s.OffsideFrac > 1 {
		return fmt.Errorf("offside fraction must be between 0 and 1")
	}
	if s.PenaltyBoxMax < 0 || s.GoalAreaMax < 0 || s.PenaltyBoxMaxOpp < 0 || s.GoalAreaMaxOpp < 0 {
		return fmt.Errorf("box max players must not be negative")
	}
	if s.WinByGoals && s.WinScore < 1 {
		return fmt.Errorf("win score must be at least 1 when winning by goals")
	}
	if s.WinByTime && s.Minutes <= 0 {
		return fmt.Errorf("minutes must be positive when winning by time")
	}
	if s.ExtraTime && (!s.GoldenGoal || s.GoldenGoalCapped) && s.ExtraMinutes <= 0 {
		return fmt.Errorf("extra minutes must be positive when extra time is fixed-length or golden goal is capped")
	}
	if s.Penalties && s.PenaltyBestOf < 1 {
		return fmt.Errorf("penalty best-of must be at least 1 when penalties are enabled")
	}
	if s.Tuning != (Tuning{}) { // only a seeded (non-default) tuning needs range-checking
		if err := s.Tuning.Validate(); err != nil {
			return err
		}
	}
	// The resolved geometry must satisfy the relational constraints (box nesting, pitch
	// proportions). Validate the geometry exactly as the match will see it.
	g, err := s.Geometry()
	if err != nil {
		return err
	}
	if err := g.Validate(); err != nil {
		return err
	}
	return nil
}

// Geometry builds the pitch geometry: the preset, then any positive overrides, then the
// box-existence flags, normalised.
func (s MatchSetup) Geometry() (Geometry, error) {
	g, ok := PresetByName(s.Field)
	if !ok {
		return Geometry{}, fmt.Errorf("unknown field preset %q", s.Field)
	}
	override := func(dst *float64, v float64) {
		if v > 0 {
			*dst = v
		}
	}
	override(&g.PlayWidth, s.PlayWidth)
	override(&g.PlayHeight, s.PlayHeight)
	override(&g.GoalMouthWidth, s.GoalWidth)
	override(&g.GoalPocketDepth, s.GoalDepth)
	override(&g.CenterCircleRadius, s.CenterCircleRadius)
	override(&g.PenaltyWidth, s.PenaltyWidth)
	override(&g.PenaltyDepth, s.PenaltyDepth)
	override(&g.GoalAreaWidth, s.GoalAreaWidth)
	override(&g.GoalAreaDepth, s.GoalAreaDepth)
	// A play-area or goal-pocket override re-derives the surface so the pitch still fits.
	if s.PlayWidth > 0 || s.PlayHeight > 0 || s.GoalDepth > 0 {
		g.ScreenWidth, g.ScreenHeight = 0, 0
		g.Name = "custom"
	}
	g.HasPenaltyArea = s.PenaltyArea
	g.HasGoalArea = s.GoalArea
	return g.Normalize(), nil
}

// Ruleset builds the match ruleset from the orthogonal win/draw fields: the win condition
// (goals, time, both, or neither), the draw-resolution chain (extra time -- sudden death
// when golden -- then penalties), the offside line, and the per-box caps. A cap on a
// non-existent box is harmless -- enforcement no-ops when the box has no geometry.
func (s MatchSetup) Ruleset() (Ruleset, error) {
	r := DefaultRuleset()

	// Win condition: the two flags are orthogonal and combine into a hybrid.
	switch {
	case s.WinByGoals && s.WinByTime:
		r.Win = WinFirstAndTimed
		r.ScoreTarget = s.WinScore
		r.RegulationSeconds = s.Minutes * 60
	case s.WinByGoals:
		r.Win = WinFirstToScore
		r.ScoreTarget = s.WinScore
	case s.WinByTime:
		r.Win = WinTimed
		r.RegulationSeconds = s.Minutes * 60
	default:
		r.Win = WinFriendly
	}

	// Draw resolution (only reachable when level at regulation end). Extra time first --
	// sudden death (until a goal) when golden, otherwise a fixed period -- then penalties.
	// Penalties with no extra time is therefore a "direct" shootout.
	r.OnDraw = nil
	if s.ExtraTime {
		if s.GoldenGoal {
			r.OnDraw = append(r.OnDraw, ContinueGoldenGoal)
			if s.GoldenGoalCapped {
				r.GoldenGoalSeconds = s.ExtraMinutes * 60 // sudden death, but the stage ends at the cap
			} else {
				r.GoldenGoalSeconds = 0 // pure sudden death: until a goal is scored
			}
		} else {
			r.OnDraw = append(r.OnDraw, ContinueExtraTime)
			r.ExtraTimeSeconds = s.ExtraMinutes * 60
		}
	}
	if s.Penalties {
		r.OnDraw = append(r.OnDraw, ContinuePenalties)
		r.Penalties = DefaultPenalties()
	}

	if s.Offside {
		r.OffsideEnabled = true
		if s.OffsideFrac > 0 {
			r.OffsideFrac = s.OffsideFrac
		} else {
			r.OffsideFrac = 2.0 / 3.0
		}
	}
	if s.PenaltyBoxMax > 0 {
		r.PenaltyBoxMaxPlayers = s.PenaltyBoxMax
	}
	if s.PenaltyBoxMaxOpp > 0 {
		r.PenaltyBoxMaxOpponents = s.PenaltyBoxMaxOpp
	}
	if s.GoalAreaMax > 0 {
		r.GoalAreaMaxPlayers = s.GoalAreaMax
	}
	if s.GoalAreaMaxOpp > 0 {
		r.GoalAreaMaxOpponents = s.GoalAreaMaxOpp
	}
	r.GoalAreaKeeperOnly = s.GoalAreaKeeperOnly
	if s.PenaltyBestOf > 0 {
		r.Penalties.BestOf = s.PenaltyBestOf
	}
	r.Enforcement = s.Enforcement
	r.EvictGrace = s.EvictGrace
	if r.Enforcement == EnforceWarnEvict && r.EvictGrace <= 0 {
		r.EvictGrace = DefaultEvictGrace // centralised default for the warn-evict grace
	}
	return r, nil
}

// DefaultEvictGrace is the warn-evict tolerance (seconds) applied when a warn-evict ruleset
// does not specify one -- the single source for the value that used to be a bare literal in
// the flag parser.
const DefaultEvictGrace = 0.5

// Build validates the setup and assembles a complete Config.
func (s MatchSetup) Build() (Config, error) {
	if err := s.Validate(); err != nil {
		return Config{}, err
	}
	g, err := s.Geometry()
	if err != nil {
		return Config{}, err
	}
	r, err := s.Ruleset()
	if err != nil {
		return Config{}, err
	}
	cfg := Default()
	cfg.Geometry = g
	cfg.Ruleset = r
	cfg.Seed = s.Seed
	if s.Tuning != (Tuning{}) { // a seeded setup carries its own tuning; a zero (unset) one keeps the default
		cfg.Tuning = s.Tuning
	}
	return cfg, nil
}
