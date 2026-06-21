// Command game runs phootball as a single local process. It opens on a menu (mode
// selection, settings) and runs the chosen match, all driven by the menu state machine.
// The fast-path flags (-solo, -duo, -ai-both) jump straight into a match. It exits
// cleanly on SIGINT/SIGTERM and on the window closing.
package main

// Kept in its own first import group so it initializes BEFORE Ebiten's package
// tree: it mutes xgb's logger before Ebiten's internal/ui init() opens the X11
// connection that would otherwise print harmless "Could not get authority info"
// warnings on Wayland. No-op on non-Linux. See package x11quiet.
import _ "phootball/internal/x11quiet"

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/hajimehoshi/ebiten/v2"

	"phootball/internal/aifactory"
	"phootball/internal/cliutil"
	"phootball/internal/config"
	"phootball/internal/control"
	"phootball/internal/input"
	"phootball/internal/logging"
	"phootball/internal/menu"
	"phootball/internal/sim"
)

// Game adapts the menu state machine to Ebiten.
type Game struct{ app *menu.App }

func (g *Game) Update() error             { return g.app.Update() }
func (g *Game) Draw(screen *ebiten.Image) { g.app.Draw(screen) }

// Layout renders at the display's physical pixel resolution so shapes stay crisp on
// high-DPI / 4K screens. The render package scales the fixed world to fill it.
func (g *Game) Layout(outsideWidth, outsideHeight int) (int, int) {
	s := ebiten.DeviceScaleFactor()
	if s <= 0 {
		s = 1
	}
	return int(float64(outsideWidth) * s), int(float64(outsideHeight) * s)
}

func main() {
	ctx, stop := cliutil.SignalContext()
	defer stop()
	os.Exit(cliutil.Code(run(ctx, os.Args[0], os.Args[1:], os.Stderr), "phootball", os.Stderr))
}

func run(ctx context.Context, name string, args []string, stderr io.Writer) error {
	opts, err := config.ParseGame(name, args, stderr)
	if err != nil {
		return err
	}
	if opts.Version {
		fmt.Fprintln(stderr, config.Version)
		return nil
	}
	if err := cliutil.CheckDifficulty(opts.Difficulty); err != nil {
		return err
	}
	if opts.NeuralWeights != "" {
		aifactory.SetWeightsPath(opts.NeuralWeights) // play against a training checkpoint
	}
	logger, err := logging.New(stderr, opts.Logging.Level, opts.Logging.Format)
	if err != nil {
		return fmt.Errorf("logging: %w", err)
	}
	slog.SetDefault(logger)

	// Persisted user config is the base; an EXPLICITLY-passed flag overrides it, an un-passed
	// flag leaves the saved value alone (so a double-clicked exe keeps the user's prefs).
	uc := menu.LoadUserConfig()
	app := buildApp(ctx, opts, uc)
	p := app.Prefs()
	cam, zoom := p.CameraMode, p.Zoom
	if opts.Set["camera"] {
		cam = opts.Camera
	}
	if opts.Set["zoom"] {
		zoom = opts.Zoom
	}
	app.ConfigureCamera(cam, zoom)
	vol, muted := p.Volume, p.Muted
	if opts.Set["volume"] {
		vol = opts.Volume
	}
	if opts.Set["mute"] {
		muted = opts.Mute
	}
	app.ConfigureAudio(vol, muted)
	return runGame(&Game{app: app}, "phootball")
}

// buildApp opens the menu (seeded from the persisted UserConfig), or jumps straight into a
// match for the fast-path flags. The fast paths build from the CLI config and don't persist a
// setup; only the menu path carries the saved settings + tuning.
func buildApp(ctx context.Context, opts config.GameOptions, uc menu.UserConfig) *menu.App {
	switch {
	case opts.Solo:
		field := sim.NewFieldFromGeometry(opts.Config.Geometry)
		m := sim.BuildSolo(field)
		ctrls := map[int]control.Controller{}
		for _, p := range m.Players {
			ctrls[p.PlayerID] = input.NewHuman()
		}
		return menu.NewPlayingApp(ctx, m, ctrls, true)
	case opts.Duo:
		field := sim.NewFieldFromGeometry(opts.Config.Geometry)
		return menu.NewDuoApp(ctx, sim.BuildDuo(field))
	case opts.AIBoth:
		m, ctrls := vsAI(opts, false)
		return menu.NewPlayingApp(ctx, m, ctrls, false)
	default:
		// Start from the saved settings; apply CLI seeds only when explicitly passed (an
		// un-passed flag must not clobber the user's saved lobby). The pitch/rules are chosen
		// in the lobby, so only team size, seed, and AI difficulty are seeded from the CLI.
		if opts.Set["team-size"] {
			uc.Settings.TeamSize = opts.TeamSize
		}
		if opts.Set["seed"] {
			uc.Settings.Seed = opts.Config.Seed
		}
		if opts.Set["team-size"] || opts.Set["difficulty"] {
			diff := opts.Difficulty
			if !opts.Set["difficulty"] {
				diff = uc.Settings.Teams[0].Difficulty // keep the saved difficulty
			}
			uc.Settings.SeedCLI(uc.Settings.TeamSize, diff)
		}
		return menu.NewApp(ctx, uc)
	}
}

// vsAI builds a match against AI from the parsed CLI config, with a local human on the
// blue team unless human is false.
func vsAI(opts config.GameOptions, human bool) (*sim.Match, map[int]control.Controller) {
	field := sim.NewFieldFromGeometry(opts.Config.Geometry)
	m := sim.BuildMatchFromConfig(field, opts.TeamSize, opts.Config)
	skill, _ := control.SkillFromString(opts.Difficulty)
	ctrls := map[int]control.Controller{}
	humanID := -1
	if human && len(m.Teams[0].Players) > 0 {
		humanID = m.Teams[0].Players[0].PlayerID
		if len(m.Teams[0].Players) > 1 {
			humanID = m.Teams[0].Players[1].PlayerID
		}
	}
	for _, p := range m.Players {
		if p.PlayerID == humanID {
			ctrls[p.PlayerID] = input.NewHuman()
		} else {
			ctrls[p.PlayerID] = aifactory.New(p.PlayerID, skill)
		}
	}
	return m, ctrls
}

// runGame opens the window and runs the Ebiten loop, treating a clean termination as
// success.
func runGame(g *Game, title string) error {
	ebiten.SetWindowSize(1200, 816)
	ebiten.SetWindowResizingMode(ebiten.WindowResizingModeEnabled)
	ebiten.SetWindowTitle(title)
	if err := ebiten.RunGame(g); err != nil && !errors.Is(err, ebiten.Termination) {
		return err
	}
	return nil
}
