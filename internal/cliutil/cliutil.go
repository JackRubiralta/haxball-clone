// Package cliutil holds the small entry-point helpers shared by every cmd/* main: the
// error->exit-code mapper and the signal-cancelled context. Keeping them here removes the
// near-identical copies that used to live in each command.
package cliutil

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"phootball/internal/config"
	"phootball/internal/control"
)

// Code maps a run error to a process exit code: 0 for success or a help request
// (config.ErrHelp), 2 for a usage error (config.ErrUsage), and 1 otherwise -- in which case
// it prints "<prefix>: <err>" to stderr. It is the single exit mapper every command shares.
func Code(err error, prefix string, stderr io.Writer) int {
	switch {
	case err == nil || errors.Is(err, config.ErrHelp):
		return 0
	case errors.Is(err, config.ErrUsage):
		return 2
	default:
		fmt.Fprintln(stderr, prefix+":", err)
		return 1
	}
}

// SignalContext returns a context cancelled on SIGINT/SIGTERM and a stop func to release the
// signal handler (call it with defer). Every command uses it for clean shutdown.
func SignalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

// CheckDifficulty validates a -difficulty flag value against control's canonical tier names
// (the single source of truth) and returns a config.ErrUsage-wrapped error if it is unknown.
// It lives here rather than in config because config cannot import control (import cycle).
func CheckDifficulty(d string) error {
	if control.ValidSkill(d) {
		return nil
	}
	return fmt.Errorf("unknown difficulty %q (want %s): %w", d, strings.Join(control.SkillNames(), ", "), config.ErrUsage)
}
