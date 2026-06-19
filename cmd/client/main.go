// Command client is a thin Ebiten front end for LAN play: it sends the local player's
// intent to the server and renders the latest authoritative snapshot, plays the
// snapshot's sound events, and shows the clock, phase, and result. It runs no gameplay
// collisions and exits cleanly on SIGINT/SIGTERM and on the window closing.
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
	"image/color"
	"io"
	"log/slog"
	"os"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/ebitenutil"
	"github.com/hajimehoshi/ebiten/v2/inpututil"

	"phootball/internal/audio"
	"phootball/internal/cliutil"
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
	viewport      render.Viewport // last frame's transform, for cursor->world aim
	showStats     bool            // Tab toggles the live stats panel (from Snapshot.Stats)
}

func (g *Game) Update() error {
	select {
	case <-g.ctx.Done():
		return ebiten.Termination
	default:
	}
	if inpututil.IsKeyJustPressed(ebiten.KeyTab) {
		g.showStats = !g.showStats
	}
	g.human.SetViewport(g.viewport)
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
	snap, ok := g.client.Snapshot()
	if !ok {
		screen.Fill(color.White)
		ebitenutil.DebugPrint(screen, "connecting to server...")
		return
	}
	// Build the field from the geometry once, rebuilding only if it changes.
	if g.field == nil || snap.Geometry != g.geo {
		g.field = sim.NewFieldFromGeometry(snap.Geometry)
		g.geo = snap.Geometry
	}
	g.viewport = render.FrameFromSnapshot(screen, adapt(snap), g.field, g.showStats)
}

// adapt projects a netcode.Snapshot into the render-agnostic render.SnapshotView. It lives here
// (the caller) so the render package never imports netcode.
func adapt(snap netcode.Snapshot) render.SnapshotView {
	ents := make([]render.SnapshotEntity, len(snap.Entities))
	for i, e := range snap.Entities {
		ents[i] = render.SnapshotEntity{
			IsBall:      e.Kind == netcode.KindBall,
			Position:    e.Position,
			Facing:      e.Facing,
			Radius:      e.Radius,
			Color:       e.Color,
			Number:      e.Number,
			ShootCharge: e.ShootCharge,
			TrapCharge:  e.TrapCharge,
		}
	}
	return render.SnapshotView{
		Geometry:             snap.Geometry,
		LeftName:             snap.LeftName,
		RightName:            snap.RightName,
		LeftColor:            snap.LeftColor,
		RightColor:           snap.RightColor,
		LeftScore:            snap.LeftScore,
		RightScore:           snap.RightScore,
		ClockSeconds:         snap.ClockSeconds,
		PhaseLabel:           snap.PhaseLabel,
		InShootout:           snap.InShootout,
		PenLeftGoals:         snap.PenLeftGoals,
		PenLeftTaken:         snap.PenLeftTaken,
		PenRightGoals:        snap.PenRightGoals,
		PenRightTaken:        snap.PenRightTaken,
		OffsideEnabled:       snap.OffsideEnabled,
		OffsideFrac:          snap.OffsideFrac,
		PenaltyBoxMaxPlayers: snap.PenaltyBoxMaxPlayers,
		GoalAreaMaxPlayers:   snap.GoalAreaMaxPlayers,
		Celebrating:          snap.Celebrating,
		GoalText:             snap.GoalText,
		WinnerText:           snap.WinnerText,
		Finished:             snap.Finished,
		Paused:               snap.Paused,
		GoalTint:             goalTint,
		Entities:             ents,
		Stats:                snap.Stats,
		// HaveSelf stays false here: the standalone client has no self-indicator (the snapshot
		// EntityState carries no PlayerID yet -- wired in the multiplayer phase).
	}
}

// goalTint is a neutral celebration tint for the network client, which does not receive
// the scoring side in the snapshot.
var goalTint = color.RGBA{240, 244, 240, 255}

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
	os.Exit(cliutil.Code(run(ctx, os.Args[0], os.Args[1:], os.Stderr), "phootball-client", os.Stderr))
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
