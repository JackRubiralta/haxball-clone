// Command client is a thin Ebiten front end for LAN play: it sends the local player's
// intent to the server and renders the latest authoritative snapshot, plays the
// snapshot's sound events, and shows the clock, phase, and result. It runs no gameplay
// collisions and exits cleanly on SIGINT/SIGTERM and on the window closing.
package main

import (
	"context"
	"errors"
	"fmt"
	"image/color"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/ebitenutil"

	"phootball/internal/audio"
	"phootball/internal/config"
	"phootball/internal/input"
	"phootball/internal/logging"
	"phootball/internal/netcode"
	"phootball/internal/render"
	"phootball/internal/sim"
)

// Game sends intents, plays snapshot sounds, and draws server snapshots.
type Game struct {
	ctx    context.Context
	client *netcode.Client
	human  *input.Human
	audio  *audio.Manager

	field         *sim.Field      // cached; rebuilt only when the geometry changes
	geo           config.Geometry // geometry the cached field was built from
	lastSoundTick uint64          // last tick whose sounds were played (dedupe)
}

func (g *Game) Update() error {
	select {
	case <-g.ctx.Done():
		return ebiten.Termination
	default:
	}
	if err := g.client.Send(g.human.Intent(nil)); err != nil {
		return err
	}
	if snap, ok := g.client.Snapshot(); ok && g.audio != nil && snap.Tick != g.lastSoundTick {
		g.audio.Dispatch(snap.Sounds)
		g.lastSoundTick = snap.Tick
	}
	return nil
}

func (g *Game) Draw(screen *ebiten.Image) {
	screen.Fill(color.White)
	snap, ok := g.client.Snapshot()
	if !ok {
		ebitenutil.DebugPrint(screen, "connecting to server...")
		return
	}

	// Build the field from the geometry once, rebuilding only if it changes.
	if g.field == nil || snap.Geometry != g.geo {
		g.field = sim.NewFieldFromGeometry(snap.Geometry)
		g.geo = snap.Geometry
	}

	render.Field(screen, g.field, snap.LeftColor, snap.RightColor)
	for _, e := range snap.Entities {
		if e.Kind == netcode.KindBall {
			render.BallAt(screen, e.Position, e.Radius)
		} else {
			render.PlayerAt(screen, e.Position, e.Facing, e.Radius, e.Color, e.Number, e.ShootCharge, e.TrapCharge)
		}
	}
	render.ZoneIndicators(screen, g.field, config.Ruleset{
		OffsideEnabled:       snap.OffsideEnabled,
		OffsideFrac:          snap.OffsideFrac,
		PenaltyBoxMaxPlayers: snap.PenaltyBoxMaxPlayers,
		GoalAreaMaxPlayers:   snap.GoalAreaMaxPlayers,
	})
	render.ScoreboardWithClock(screen, snap.LeftName, snap.LeftScore, snap.RightName, snap.RightScore,
		snap.ClockSeconds, snap.PhaseLabel)
	if snap.InShootout {
		render.ShootoutPanel(screen, snap.LeftName, snap.PenLeftGoals, snap.PenLeftTaken,
			snap.RightName, snap.PenRightGoals, snap.PenRightTaken)
	}
	switch {
	case snap.Finished:
		render.CenterBanner(screen, snap.WinnerText)
	case snap.Celebrating:
		render.CenterBanner(screen, bannerOr(snap.GoalText, "G O A L !"))
	}
}

func bannerOr(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

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
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(code(run(ctx, os.Args[0], os.Args[1:], os.Stderr)))
}

func code(err error) int {
	switch {
	case err == nil || errors.Is(err, config.ErrHelp):
		return 0
	case errors.Is(err, config.ErrUsage):
		return 2
	default:
		fmt.Fprintln(os.Stderr, "phootball-client:", err)
		return 1
	}
}

func run(ctx context.Context, name string, args []string, stderr io.Writer) error {
	opts, err := config.ParseClient(name, args, stderr)
	if err != nil {
		return err
	}
	if opts.Version {
		fmt.Fprintln(stderr, config.Version)
		return nil
	}
	logger, err := logging.New(stderr, opts.Logging.Level, opts.Logging.Format)
	if err != nil {
		return fmt.Errorf("logging: %w", err)
	}
	slog.SetDefault(logger)

	client, err := netcode.Dial(opts.Addr)
	if err != nil {
		return fmt.Errorf("dial %s: %w", opts.Addr, err)
	}
	defer client.Close()
	go func() {
		<-ctx.Done()
		client.Close()
	}()

	g := &Game{
		ctx:    ctx,
		client: client,
		human:  input.NewHuman(),
		audio:  audio.New(audio.Settings{Volume: opts.Volume, Muted: opts.Mute}),
	}
	ebiten.SetWindowSize(1200, 816)
	ebiten.SetWindowResizingMode(ebiten.WindowResizingModeEnabled)
	ebiten.SetWindowTitle("phootball (client)")
	if err := ebiten.RunGame(g); err != nil && !errors.Is(err, ebiten.Termination) {
		return err
	}
	return nil
}
