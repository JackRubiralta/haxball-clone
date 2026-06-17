// Command server runs the authoritative, headless simulation for LAN play. It links
// only sim/physics/netcode (no Ebiten): it steps the one true match, fills every slot
// with AI until clients connect, and broadcasts snapshots. It shuts down cleanly on
// SIGINT/SIGTERM.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"phootball/internal/config"
	"phootball/internal/control"
	"phootball/internal/logging"
	"phootball/internal/netcode"
	"phootball/internal/sim"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(code(run(ctx, os.Args[0], os.Args[1:], os.Stderr)))
}

// code maps a run error to a process exit code: 0 success/help, 2 usage, 1 otherwise.
func code(err error) int {
	switch {
	case err == nil || errors.Is(err, config.ErrHelp):
		return 0
	case errors.Is(err, config.ErrUsage):
		return 2
	default:
		fmt.Fprintln(os.Stderr, "phootball-server:", err)
		return 1
	}
}

func run(ctx context.Context, name string, args []string, stderr io.Writer) error {
	opts, err := config.ParseServer(name, args, stderr)
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

	field := sim.NewFieldFromGeometry(opts.Config.Geometry)
	match := sim.BuildMatchFromConfig(field, opts.TeamSize, opts.Config)

	// One claimable human slot per team (an outfielder if there is one); every player
	// also has an AI fallback that runs until a client claims its slot.
	humanIDs := make([]int, 0, 2)
	for _, t := range match.Teams {
		idx := 0
		if len(t.Players) > 1 {
			idx = 1
		}
		humanIDs = append(humanIDs, t.Players[idx].PlayerID)
	}
	skill, _ := control.SkillFromString(opts.Difficulty)
	bots := make(map[int]netcode.Bot, len(match.Players))
	for _, p := range match.Players {
		bots[p.PlayerID] = control.NewAISkill(p.PlayerID, skill)
	}

	server := netcode.NewServer(opts.Addr, match, bots, humanIDs)
	server.SetLogger(logger)
	server.SetTickRate(opts.TickRate)
	if err := server.Run(ctx); err != nil {
		return fmt.Errorf("server: %w", err)
	}
	return nil
}
