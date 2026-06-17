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
	penW, penD   float64
	gaW, gaD     float64
}

func bindGeometry(fs *flag.FlagSet) *geomFlags {
	gf := &geomFlags{}
	fs.StringVar(&gf.field, "field", "standard", "pitch preset: standard, small, large")
	fs.Float64Var(&gf.playW, "play-width", 0, "override play-area width in world units (0 keeps the preset)")
	fs.Float64Var(&gf.playH, "play-height", 0, "override play-area height (0 keeps the preset)")
	fs.Float64Var(&gf.goalW, "goal-width", 0, "override goal mouth width (0 keeps the preset)")
	fs.Float64Var(&gf.goalD, "goal-depth", 0, "override goal pocket depth (0 keeps the preset)")
	fs.Float64Var(&gf.penW, "penalty-width", 0, "override penalty-area width (0 keeps the preset)")
	fs.Float64Var(&gf.penD, "penalty-depth", 0, "override penalty-area depth (0 keeps the preset)")
	fs.Float64Var(&gf.gaW, "goalarea-width", 0, "override goal-area width (0 keeps the preset)")
	fs.Float64Var(&gf.gaD, "goalarea-depth", 0, "override goal-area depth (0 keeps the preset)")
	return gf
}

func (gf *geomFlags) geometry() (Geometry, error) {
	g, ok := PresetByName(gf.field)
	if !ok {
		return Geometry{}, usagef("unknown field preset %q (want standard, small, or large)", gf.field)
	}
	overrides := []struct {
		name string
		v    float64
		dst  *float64
	}{
		{"play-width", gf.playW, &g.PlayWidth},
		{"play-height", gf.playH, &g.PlayHeight},
		{"goal-width", gf.goalW, &g.GoalMouthWidth},
		{"goal-depth", gf.goalD, &g.GoalPocketDepth},
		{"penalty-width", gf.penW, &g.PenaltyWidth},
		{"penalty-depth", gf.penD, &g.PenaltyDepth},
		{"goalarea-width", gf.gaW, &g.GoalAreaWidth},
		{"goalarea-depth", gf.gaD, &g.GoalAreaDepth},
	}
	for _, o := range overrides {
		if o.v < 0 {
			return Geometry{}, usagef("%s must be positive", o.name)
		}
		if o.v > 0 {
			*o.dst = o.v
		}
	}
	// Normalize grows the logical surface to fit if an override enlarged the play area.
	return g.Normalize(), nil
}

// ruleFlags collects the match-mode flags shared by the game and server.
type ruleFlags struct {
	mode        string
	minutes     float64
	winScore    int
	extraTime   bool
	goldenGoal  bool
	penalties   bool
	directPens  bool
	offsideFrac float64
	gkBoxMax    int
	zoneEnforce string
}

func bindRules(fs *flag.FlagSet) *ruleFlags {
	rf := &ruleFlags{}
	fs.StringVar(&rf.mode, "mode", "friendly", "match mode: friendly, quick, timed, cup, golden")
	fs.Float64Var(&rf.minutes, "minutes", 3, "regulation length in minutes (timed and cup modes)")
	fs.IntVar(&rf.winScore, "win-score", 3, "goals needed to win (quick mode)")
	fs.BoolVar(&rf.extraTime, "extra-time", false, "if a timed match is drawn, play extra time")
	fs.BoolVar(&rf.goldenGoal, "golden-goal", false, "if a timed match is drawn, play sudden death")
	fs.BoolVar(&rf.penalties, "penalties", false, "if a timed match is drawn, decide it on penalties")
	fs.BoolVar(&rf.directPens, "direct-pens", false, "if a timed match is drawn, go straight to penalties")
	fs.Float64Var(&rf.offsideFrac, "offside-frac", 0, "anti-camp line as a fraction of the pitch from a team's own goal (0 = off, e.g. 0.667)")
	fs.IntVar(&rf.gkBoxMax, "gk-box-max", 0, "max players allowed in a team's goal area at once (0 = off)")
	fs.StringVar(&rf.zoneEnforce, "zone-enforce", "clamp", "positional-rule enforcement: clamp or evict")
	return rf
}

func (rf *ruleFlags) ruleset() (Ruleset, error) {
	if rf.minutes < 0 {
		return Ruleset{}, usagef("minutes must not be negative")
	}
	if rf.winScore < 1 {
		return Ruleset{}, usagef("win-score must be at least 1")
	}
	r, err := RulesetForMode(rf.mode, rf.minutes, rf.winScore)
	if err != nil {
		return Ruleset{}, usagef("%v", err)
	}

	// Draw-decider toggles assemble the OnDraw chain in football order. -direct-pens
	// forces a straight shootout; otherwise any set toggle replaces the chain.
	switch {
	case rf.directPens:
		r.OnDraw = []Continuation{ContinuePenalties}
		r.Penalties = DefaultPenalties()
	case rf.extraTime || rf.goldenGoal || rf.penalties:
		r.OnDraw = nil
		if rf.extraTime {
			r.OnDraw = append(r.OnDraw, ContinueExtraTime)
			if r.ExtraTimeSeconds == 0 {
				r.ExtraTimeSeconds = (rf.minutes * 60) / 3
			}
		}
		if rf.goldenGoal {
			r.OnDraw = append(r.OnDraw, ContinueGoldenGoal)
		}
		if rf.penalties {
			r.OnDraw = append(r.OnDraw, ContinuePenalties)
			r.Penalties = DefaultPenalties()
		}
	}

	// Positional rules.
	if rf.offsideFrac < 0 || rf.offsideFrac > 1 {
		return Ruleset{}, usagef("offside-frac must be between 0 and 1")
	}
	if rf.gkBoxMax < 0 {
		return Ruleset{}, usagef("gk-box-max must not be negative")
	}
	if rf.offsideFrac > 0 {
		r.OffsideEnabled = true
		r.OffsideFrac = rf.offsideFrac
	}
	if rf.gkBoxMax > 0 {
		r.GKBoxEnabled = true
		r.GKBoxMax = rf.gkBoxMax
	}
	switch strings.ToLower(strings.TrimSpace(rf.zoneEnforce)) {
	case "", "clamp":
		r.Enforcement = EnforceClamp
	case "evict":
		r.Enforcement = EnforceWarnEvict
		if r.EvictGrace == 0 {
			r.EvictGrace = 0.5
		}
	default:
		return Ruleset{}, usagef("unknown zone-enforce %q (want clamp or evict)", rf.zoneEnforce)
	}
	return r, nil
}

func parseError(err error) error {
	if errors.Is(err, flag.ErrHelp) {
		return ErrHelp // usage already printed; the command exits cleanly
	}
	return fmt.Errorf("%w: %v", ErrUsage, err)
}

// GameOptions is the parsed configuration for the local game command.
type GameOptions struct {
	Config   Config
	Logging  Logging
	Version  bool
	TeamSize int
	AIBoth   bool
	Solo     bool
	Duo      bool
	Zoom     float64
	Camera   string
	Mute     bool
	Volume   float64
}

// ParseGame parses the local game command's flags.
func ParseGame(name string, args []string, stderr io.Writer) (GameOptions, error) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	logging, version := bindLogging(fs)
	gf := bindGeometry(fs)
	rf := bindRules(fs)
	seed := fs.Int64("seed", 1, "deterministic RNG seed (coin tosses, kickoff side)")
	teamSize := fs.Int("team-size", 3, "players per team")
	aiBoth := fs.Bool("ai-both", false, "AI controls both teams (spectate)")
	solo := fs.Bool("solo", false, "single human player + ball only, no opponents (for testing)")
	duo := fs.Bool("duo", false, "two players you switch control of with 1 and 2 (for testing)")
	zoom := fs.Float64("zoom", 1, "camera zoom (used with -camera ball; mouse wheel adjusts in game)")
	camera := fs.String("camera", "fit", "camera mode: fit (whole pitch) or ball (follow)")
	mute := fs.Bool("mute", false, "silence all sound")
	volume := fs.Float64("volume", 0.8, "master volume 0..1")
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
	if *zoom <= 0 {
		return GameOptions{}, usagef("zoom must be positive")
	}
	switch *camera {
	case "fit", "ball", "follow":
	default:
		return GameOptions{}, usagef("unknown camera %q (want fit or ball)", *camera)
	}
	if *volume < 0 || *volume > 1 {
		return GameOptions{}, usagef("volume must be between 0 and 1")
	}
	g, err := gf.geometry()
	if err != nil {
		return GameOptions{}, err
	}
	r, err := rf.ruleset()
	if err != nil {
		return GameOptions{}, err
	}
	cfg := Default()
	cfg.Geometry = g
	cfg.Ruleset = r
	cfg.Seed = *seed
	return GameOptions{
		Config:   cfg,
		Logging:  *logging,
		TeamSize: *teamSize,
		AIBoth:   *aiBoth,
		Solo:     *solo,
		Duo:      *duo,
		Zoom:     *zoom,
		Camera:   *camera,
		Mute:     *mute,
		Volume:   *volume,
	}, nil
}

// ServerOptions is the parsed configuration for the headless server command.
type ServerOptions struct {
	Config   Config
	Logging  Logging
	Version  bool
	Addr     string
	TeamSize int
	TickRate float64
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
	teamSize := fs.Int("team-size", 3, "players per team")
	tickRate := fs.Float64("tick-rate", 60, "simulation ticks per second (1..240)")
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
	if *tickRate < 1 || *tickRate > 240 {
		return ServerOptions{}, usagef("tick-rate must be between 1 and 240")
	}
	if strings.TrimSpace(*addr) == "" {
		return ServerOptions{}, usagef("addr must not be empty")
	}
	g, err := gf.geometry()
	if err != nil {
		return ServerOptions{}, err
	}
	r, err := rf.ruleset()
	if err != nil {
		return ServerOptions{}, err
	}
	cfg := Default()
	cfg.Geometry = g
	cfg.Ruleset = r
	cfg.Seed = *seed
	return ServerOptions{
		Config:   cfg,
		Logging:  *logging,
		Addr:     *addr,
		TeamSize: *teamSize,
		TickRate: *tickRate,
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
