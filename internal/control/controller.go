// Package control turns a source of input -- a local human, an AI, or (on the
// server) a remote client -- into the per-tick sim.Intent that drives a player. The
// simulation never learns where an intent came from, which is what lets the same
// Match.Step run locally and on an authoritative server.
package control

import "phootball/internal/sim"

// Controller produces one Intent per tick for a single player. view exposes the
// current match state so an AI can react to it; a human controller ignores it and
// reads the keyboard and mouse instead.
type Controller interface {
	Intent(view *sim.Match) sim.Intent
}
