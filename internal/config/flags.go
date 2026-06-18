package config

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
)

// Version is the build version string printed by the -version flag.
const Version = "phootball 0.2.0"

// ErrUsage marks a parse/validation failure the user should see as a usage error (a bad
// flag, an unknown preset, an out-of-range value). Commands map it to exit code 2.
var ErrUsage = errors.New("usage error")

// ErrHelp is returned when -h/-help was requested and usage has already been printed.
// Commands map it to a clean exit (code 0).
var ErrHelp = flag.ErrHelp

func usagef(format string, a ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrUsage}, a...)...)
}

// Logging is the parsed logging configuration shared by every command.
type Logging struct {
	Level  string
	Format string
}

func (l Logging) validate() error {
	switch strings.ToLower(strings.TrimSpace(l.Level)) {
	case "", "debug", "info", "warn", "warning", "error":
	default:
		return usagef("unknown log level %q (want debug, info, warn, or error)", l.Level)
	}
	switch strings.ToLower(strings.TrimSpace(l.Format)) {
	case "", "text", "json":
	default:
		return usagef("unknown log format %q (want text or json)", l.Format)
	}
	return nil
}

func bindLogging(fs *flag.FlagSet) (*Logging, *bool) {
	l := &Logging{}
	fs.StringVar(&l.Level, "log-level", "info", "log level: debug, info, warn, error")
	fs.StringVar(&l.Format, "log-format", "text", "log format: text or json")
	v := fs.Bool("version", false, "print version and exit")
	return l, v
}

// geomFlags collects the optional pitch-dimension overrides. A zero value keeps the
// preset's value, so only the dimensions a user names are changed.
type geomFlags struct {
	field        string
	playW, playH float64
	goalW, goalD float64
	penArea      bool
	penW, penD   float64
	goalArea     bool
	gaW, gaD     float64
}

func bindGeometry(fs *flag.FlagSet) *geomFlags {
	gf := &geomFlags{}
	fs.StringVar(&gf.field, "field", "standard", "pitch preset: standard, small, large")
	fs.Float64Var(&gf.playW, "play-width", 0, "override play-area width in world units (0 keeps the preset)")
	fs.Float64Var(&gf.playH, "play-height", 0, "override play-area height (0 keeps the preset)")
	fs.Float64Var(&gf.goalW, "goal-width", 0, "override goal mouth width (0 keeps the preset)")
	fs.Float64Var(&gf.goalD, "goal-depth", 0, "override goal pocket depth (0 keeps the preset)")
	fs.BoolVar(&gf.penArea, "penalty-area", true, "draw and enforce the penalty area")
	fs.Float64Var(&gf.penW, "penalty-width", 0, "override penalty-area width (0 keeps the preset)")
	fs.Float64Var(&gf.penD, "penalty-depth", 0, "override penalty-area depth (0 keeps the preset)")
	fs.BoolVar(&gf.goalArea, "goal-area", true, "draw and enforce the goal area")
	fs.Float64Var(&gf.gaW, "goalarea-width", 0, "override goal-area width (0 keeps the preset)")
	fs.Float64Var(&gf.gaD, "goalarea-depth", 0, "override goal-area depth (0 keeps the preset)")
	return gf
}

// fill writes the geometry flags into a MatchSetup (the single mapping).
func (gf *geomFlags) fill(s *MatchSetup) error {
	for _, o := range []struct {
		name string
		v    float64
	}{
		{"play-width", gf.playW}, {"play-height", gf.playH},
		{"goal-width", gf.goalW}, {"goal-depth", gf.goalD},
		{"penalty-width", gf.penW}, {"penalty-depth", gf.penD},
		{"goalarea-width", gf.gaW}, {"goalarea-depth", gf.gaD},
	} {
		if o.v < 0 {
			return usagef("%s must be positive", o.name)
		}
	}
	s.Field = gf.field
	s.PlayWidth, s.PlayHeight = gf.playW, gf.playH
	s.GoalWidth, s.GoalDepth = gf.goalW, gf.goalD
	s.PenaltyArea, s.PenaltyWidth, s.PenaltyDepth = gf.penArea, gf.penW, gf.penD
	s.GoalArea, s.GoalAreaWidth, s.GoalAreaDepth = gf.goalArea, gf.gaW, gf.gaD
	return nil
}

// ruleFlags collects the match-rule flags shared by the game and server. The win
// conditions are orthogonal (goals and/or time); the draw resolution is a chain of extra
// time (sudden death when golden) then penalties.
type ruleFlags struct {
	winByGoals     bool
	winScore       int
	winByTime      bool
	minutes        float64
	extraTime      bool
	extraMinutes   float64
	goldenGoal     bool
	goldenCapped   bool
	penalties      bool
	penBestOf      int
	offsideFrac    float64
	penBoxMax      int
	penBoxMaxOpp   int
	goalAreaMax    int
	goalAreaMaxOpp int
	goalAreaKeeper bool
	gkBoxMax       int
	zoneEnforce    string
}

func bindRules(fs *flag.FlagSet) *ruleFlags {
	rf := &ruleFlags{}
	fs.BoolVar(&rf.winByGoals, "win-goals", false, "win by reaching a goal target (first to -win-score)")
	fs.IntVar(&rf.winScore, "win-score", 3, "goals needed to win (with -win-goals)")
	fs.BoolVar(&rf.winByTime, "time-limit", false, "win by the clock: lead when regulation expires")
	fs.Float64Var(&rf.minutes, "minutes", 3, "regulation length in minutes (with -time-limit)")
	fs.BoolVar(&rf.extraTime, "extra-time", false, "if drawn at regulation, play extra time")
	fs.Float64Var(&rf.extraMinutes, "extra-minutes", 1, "length of extra time in minutes (ignored with -golden-goal)")
	fs.BoolVar(&rf.goldenGoal, "golden-goal", false, "make extra time sudden death: the next goal wins")
	fs.BoolVar(&rf.goldenCapped, "golden-goal-capped", false, "cap golden-goal sudden death at -extra-minutes (with -golden-goal)")
	fs.BoolVar(&rf.penalties, "penalties", false, "if still drawn, decide on a penalty shootout (direct when -extra-time is off)")
	fs.IntVar(&rf.penBestOf, "penalties-best-of", 0, "kicks per side in a shootout (0 = the default of 5)")
	fs.Float64Var(&rf.offsideFrac, "offside-frac", 0, "anti-camp line as a fraction of the pitch from a team's own goal (0 = off, e.g. 0.667)")
	fs.IntVar(&rf.penBoxMax, "penalty-box-max", 0, "max DEFENDING players allowed in their penalty area (0 = off)")
	fs.IntVar(&rf.penBoxMaxOpp, "penalty-box-max-opp", 0, "max OPPONENT (attacking) players allowed in a penalty area (0 = off)")
	fs.IntVar(&rf.goalAreaMax, "goalarea-box-max", 0, "max DEFENDING players allowed in their goal area (0 = off)")
	fs.IntVar(&rf.goalAreaMaxOpp, "goalarea-box-max-opp", 0, "max OPPONENT (attacking) players allowed in a goal area (0 = off)")
	fs.BoolVar(&rf.goalAreaKeeper, "goalarea-keeper-only", false, "goal area is keeper-only: only the box owner's keeper may enter (overrides the numeric goal-area caps)")
	fs.IntVar(&rf.gkBoxMax, "gk-box-max", 0, "deprecated alias for -goalarea-box-max")
	fs.StringVar(&rf.zoneEnforce, "zone-enforce", "clamp", "positional-rule enforcement: clamp or evict")
	return rf
}

// fill writes the rule flags into a MatchSetup (the single mapping).
func (rf *ruleFlags) fill(s *MatchSetup) error {
	if rf.winByGoals && rf.winScore < 1 {
		return usagef("win-score must be at least 1")
	}
	if rf.winByTime && rf.minutes <= 0 {
		return usagef("minutes must be positive with -time-limit")
	}
	if rf.extraTime && (!rf.goldenGoal || rf.goldenCapped) && rf.extraMinutes <= 0 {
		return usagef("extra-minutes must be positive with -extra-time (or a capped -golden-goal)")
	}
	if rf.offsideFrac < 0 || rf.offsideFrac > 1 {
		return usagef("offside-frac must be between 0 and 1")
	}
	if rf.penBoxMax < 0 || rf.goalAreaMax < 0 || rf.gkBoxMax < 0 || rf.penBoxMaxOpp < 0 || rf.goalAreaMaxOpp < 0 {
		return usagef("box-max players must not be negative")
	}
	if rf.penBestOf < 0 {
		return usagef("penalties-best-of must not be negative")
	}
	switch strings.ToLower(strings.TrimSpace(rf.zoneEnforce)) {
	case "", "clamp":
		s.Enforcement = EnforceClamp
	case "evict":
		s.Enforcement = EnforceWarnEvict
		s.EvictGrace = 0.5
	default:
		return usagef("unknown zone-enforce %q (want clamp or evict)", rf.zoneEnforce)
	}
	s.WinByGoals, s.WinScore = rf.winByGoals, rf.winScore
	s.WinByTime, s.Minutes = rf.winByTime, rf.minutes
	s.ExtraTime, s.ExtraMinutes = rf.extraTime, rf.extraMinutes
	s.GoldenGoal, s.GoldenGoalCapped = rf.goldenGoal, rf.goldenCapped
	s.Penalties, s.PenaltyBestOf = rf.penalties, rf.penBestOf
	if rf.offsideFrac > 0 {
		s.Offside, s.OffsideFrac = true, rf.offsideFrac
	}
	s.PenaltyBoxMax = rf.penBoxMax
	s.PenaltyBoxMaxOpp = rf.penBoxMaxOpp
	s.GoalAreaMax = rf.goalAreaMax
	s.GoalAreaMaxOpp = rf.goalAreaMaxOpp
	s.GoalAreaKeeperOnly = rf.goalAreaKeeper
	if s.GoalAreaMax == 0 && rf.gkBoxMax > 0 {
		s.GoalAreaMax = rf.gkBoxMax // deprecated -gk-box-max alias
	}
	return nil
}

// validDifficulty reports whether s names a known AI difficulty tier (matching
// control.SkillFromString). Empty means "use the default tier".
func validDifficulty(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "default", "easy", "normal", "medium", "hard", "pro", "impossible", "perfect":
		return true
	default:
		return false
	}
}

func parseError(err error) error {
	if errors.Is(err, flag.ErrHelp) {
		return ErrHelp // usage already printed; the command exits cleanly
	}
	return fmt.Errorf("%w: %v", ErrUsage, err)
}

// GameOptions is the parsed configuration for the local game command.
type GameOptions struct {
	Config     Config
	Logging    Logging
	Version    bool
	TeamSize   int
	HomeSize   int // resolved home (Blue) roster size (falls back to TeamSize)
	AwaySize   int // resolved away (Red) roster size (falls back to TeamSize)
	AIBoth     bool
	Solo       bool
	Duo        bool
	Zoom       float64
	Camera     string
	Mute       bool
	Volume     float64
	Difficulty string // AI difficulty tier name (see control.SkillFromString)
}

// ParseGame parses the local game command's flags.
func ParseGame(name string, args []string, stderr io.Writer) (GameOptions, error) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	logging, version := bindLogging(fs)
	gf := bindGeometry(fs)
	rf := bindRules(fs)
	seed := fs.Int64("seed", 1, "deterministic RNG seed (coin tosses, kickoff side)")
	teamSize := fs.Int("team-size", 3, "players per team (seeds both teams unless -home-size/-away-size are set)")
	homeSize := fs.Int("home-size", 0, "players on the home (Blue) team (0 = use -team-size)")
	awaySize := fs.Int("away-size", 0, "players on the away (Red) team (0 = use -team-size)")
	aiBoth := fs.Bool("ai-both", false, "AI controls both teams (spectate)")
	solo := fs.Bool("solo", false, "single human player + ball only, no opponents (for testing)")
	duo := fs.Bool("duo", false, "two players you switch control of with 1 and 2 (for testing)")
	zoom := fs.Float64("zoom", 1, "camera zoom (used with -camera ball/player; mouse wheel adjusts in game)")
	camera := fs.String("camera", "ball", "camera mode: ball (follow), player (follow you), or fit (whole pitch)")
	mute := fs.Bool("mute", false, "silence all sound")
	volume := fs.Float64("volume", 0.8, "master volume 0..1")
	difficulty := fs.String("difficulty", "hard", "AI difficulty: easy, normal, hard, or impossible")
	if err := fs.Parse(args); err != nil {
		return GameOptions{}, parseError(err)
	}
	if *version {
		return GameOptions{Version: true}, nil
	}
	if err := logging.validate(); err != nil {
		return GameOptions{}, err
	}
	if *teamSize < 1 || *teamSize > 11 {
		return GameOptions{}, usagef("team-size must be between 1 and 11")
	}
	if *homeSize < 0 || *homeSize > 11 {
		return GameOptions{}, usagef("home-size must be between 0 and 11")
	}
	if *awaySize < 0 || *awaySize > 11 {
		return GameOptions{}, usagef("away-size must be between 0 and 11")
	}
	if *zoom <= 0 {
		return GameOptions{}, usagef("zoom must be positive")
	}
	switch *camera {
	case "fit", "ball", "follow", "player", "active":
	default:
		return GameOptions{}, usagef("unknown camera %q (want ball, player, or fit)", *camera)
	}
	if *volume < 0 || *volume > 1 {
		return GameOptions{}, usagef("volume must be between 0 and 1")
	}
	if !validDifficulty(*difficulty) {
		return GameOptions{}, usagef("unknown difficulty %q (want easy, normal, hard, or impossible)", *difficulty)
	}
	setup := MatchSetup{TeamSize: *teamSize, HomeSize: *homeSize, AwaySize: *awaySize, Seed: *seed}
	if err := gf.fill(&setup); err != nil {
		return GameOptions{}, err
	}
	if err := rf.fill(&setup); err != nil {
		return GameOptions{}, err
	}
	cfg, err := setup.Build()
	if err != nil {
		return GameOptions{}, usagef("%v", err)
	}
	homeResolved, awayResolved := setup.sizes()
	return GameOptions{
		Config:     cfg,
		Logging:    *logging,
		TeamSize:   *teamSize,
		HomeSize:   homeResolved,
		AwaySize:   awayResolved,
		AIBoth:     *aiBoth,
		Solo:       *solo,
		Duo:        *duo,
		Zoom:       *zoom,
		Camera:     *camera,
		Mute:       *mute,
		Volume:     *volume,
		Difficulty: *difficulty,
	}, nil
}

// ServerOptions is the parsed configuration for the headless server command.
type ServerOptions struct {
	Config     Config
	Logging    Logging
	Version    bool
	Addr       string
	TeamSize   int
	HomeSize   int // resolved home (Blue) roster size (falls back to TeamSize)
	AwaySize   int // resolved away (Red) roster size (falls back to TeamSize)
	TickRate   float64
	Difficulty string // AI difficulty tier name (see control.SkillFromString)
}

// ParseServer parses the server command's flags.
func ParseServer(name string, args []string, stderr io.Writer) (ServerOptions, error) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	logging, version := bindLogging(fs)
	gf := bindGeometry(fs)
	rf := bindRules(fs)
	seed := fs.Int64("seed", 1, "deterministic RNG seed (coin tosses, kickoff side)")
	addr := fs.String("addr", ":4000", "listen address")
	teamSize := fs.Int("team-size", 3, "players per team (seeds both teams unless -home-size/-away-size are set)")
	homeSize := fs.Int("home-size", 0, "players on the home (Blue) team (0 = use -team-size)")
	awaySize := fs.Int("away-size", 0, "players on the away (Red) team (0 = use -team-size)")
	tickRate := fs.Float64("tick-rate", 60, "simulation ticks per second (1..240)")
	difficulty := fs.String("difficulty", "hard", "AI difficulty: easy, normal, hard, or impossible")
	if err := fs.Parse(args); err != nil {
		return ServerOptions{}, parseError(err)
	}
	if *version {
		return ServerOptions{Version: true}, nil
	}
	if err := logging.validate(); err != nil {
		return ServerOptions{}, err
	}
	if *teamSize < 1 || *teamSize > 11 {
		return ServerOptions{}, usagef("team-size must be between 1 and 11")
	}
	if *homeSize < 0 || *homeSize > 11 {
		return ServerOptions{}, usagef("home-size must be between 0 and 11")
	}
	if *awaySize < 0 || *awaySize > 11 {
		return ServerOptions{}, usagef("away-size must be between 0 and 11")
	}
	if *tickRate < 1 || *tickRate > 240 {
		return ServerOptions{}, usagef("tick-rate must be between 1 and 240")
	}
	if strings.TrimSpace(*addr) == "" {
		return ServerOptions{}, usagef("addr must not be empty")
	}
	if !validDifficulty(*difficulty) {
		return ServerOptions{}, usagef("unknown difficulty %q (want easy, normal, hard, or impossible)", *difficulty)
	}
	setup := MatchSetup{TeamSize: *teamSize, HomeSize: *homeSize, AwaySize: *awaySize, Seed: *seed}
	if err := gf.fill(&setup); err != nil {
		return ServerOptions{}, err
	}
	if err := rf.fill(&setup); err != nil {
		return ServerOptions{}, err
	}
	cfg, err := setup.Build()
	if err != nil {
		return ServerOptions{}, usagef("%v", err)
	}
	homeResolved, awayResolved := setup.sizes()
	return ServerOptions{
		Config:     cfg,
		Logging:    *logging,
		Addr:       *addr,
		TeamSize:   *teamSize,
		HomeSize:   homeResolved,
		AwaySize:   awayResolved,
		TickRate:   *tickRate,
		Difficulty: *difficulty,
	}, nil
}

// ClientOptions is the parsed configuration for the network client command.
type ClientOptions struct {
	Logging Logging
	Version bool
	Addr    string
	Mute    bool
	Volume  float64
}

// ParseClient parses the client command's flags.
func ParseClient(name string, args []string, stderr io.Writer) (ClientOptions, error) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	logging, version := bindLogging(fs)
	addr := fs.String("addr", "localhost:4000", "server address")
	mute := fs.Bool("mute", false, "silence all sound")
	volume := fs.Float64("volume", 0.8, "master volume 0..1")
	if err := fs.Parse(args); err != nil {
		return ClientOptions{}, parseError(err)
	}
	if *version {
		return ClientOptions{Version: true}, nil
	}
	if err := logging.validate(); err != nil {
		return ClientOptions{}, err
	}
	if strings.TrimSpace(*addr) == "" {
		return ClientOptions{}, usagef("addr must not be empty")
	}
	if *volume < 0 || *volume > 1 {
		return ClientOptions{}, usagef("volume must be between 0 and 1")
	}
	return ClientOptions{Logging: *logging, Addr: *addr, Mute: *mute, Volume: *volume}, nil
}
