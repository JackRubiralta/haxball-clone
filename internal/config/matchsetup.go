package config

import "fmt"

// MatchSetup is the flat, plain-data description of every match option a player can
// choose: team size, pitch and goal and box dimensions (and whether each box exists),
// the positional rules, and the win/draw conditions. It is the single mapping the
// command line and the pre-match lobby both feed, so the two never drift. Zero-valued
// dimension overrides mean "keep the field preset's value".
type MatchSetup struct {
	TeamSize int
	Field    string // preset name (standard, small, large)

	// Geometry overrides (0 = inherit the preset).
	PlayWidth, PlayHeight float64
	GoalWidth, GoalDepth  float64

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

	// Win / draw conditions.
	Mode       string
	Minutes    float64
	WinScore   int
	ExtraTime  bool
	GoldenGoal bool
	Penalties  bool
	DirectPens bool

	Seed int64
}

// DefaultMatchSetup reproduces the original default match (standard pitch, friendly
// rules, both boxes, no positional limits), so DefaultMatchSetup().Build() equals the
// legacy Default() config.
func DefaultMatchSetup() MatchSetup {
	return MatchSetup{
		TeamSize:    3,
		Field:       "standard",
		PenaltyArea: true,
		GoalArea:    true,
		Mode:        "friendly",
		Minutes:     3,
		WinScore:    3,
		OffsideFrac: 2.0 / 3.0,
		Seed:        1,
	}
}

// Validate checks the option ranges.
func (s MatchSetup) Validate() error {
	if _, ok := PresetByName(s.Field); !ok {
		return fmt.Errorf("unknown field preset %q (want standard, small, or large)", s.Field)
	}
	if s.TeamSize < 1 || s.TeamSize > 11 {
		return fmt.Errorf("team size must be between 1 and 11")
	}
	if s.OffsideFrac < 0 || s.OffsideFrac > 1 {
		return fmt.Errorf("offside fraction must be between 0 and 1")
	}
	if s.PenaltyBoxMax < 0 || s.GoalAreaMax < 0 || s.PenaltyBoxMaxOpp < 0 || s.GoalAreaMaxOpp < 0 {
		return fmt.Errorf("box max players must not be negative")
	}
	if s.Minutes < 0 {
		return fmt.Errorf("minutes must not be negative")
	}
	if s.WinScore < 1 {
		return fmt.Errorf("win score must be at least 1")
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

// Ruleset builds the match ruleset: the base mode, the draw-decider chain (same order as
// the CLI), the offside line, and the per-box caps. A cap on a non-existent box is
// harmless -- enforcement no-ops when the box has no geometry.
func (s MatchSetup) Ruleset() (Ruleset, error) {
	r, err := RulesetForMode(s.Mode, s.Minutes, s.WinScore)
	if err != nil {
		return Ruleset{}, err
	}
	switch {
	case s.DirectPens:
		r.OnDraw = []Continuation{ContinuePenalties}
		r.Penalties = DefaultPenalties()
	case s.ExtraTime || s.GoldenGoal || s.Penalties:
		r.OnDraw = nil
		if s.ExtraTime {
			r.OnDraw = append(r.OnDraw, ContinueExtraTime)
			if r.ExtraTimeSeconds == 0 {
				r.ExtraTimeSeconds = (s.Minutes * 60) / 3
			}
		}
		if s.GoldenGoal {
			r.OnDraw = append(r.OnDraw, ContinueGoldenGoal)
		}
		if s.Penalties {
			r.OnDraw = append(r.OnDraw, ContinuePenalties)
			r.Penalties = DefaultPenalties()
		}
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
	r.Enforcement = s.Enforcement
	r.EvictGrace = s.EvictGrace
	return r, nil
}

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
	return cfg, nil
}
