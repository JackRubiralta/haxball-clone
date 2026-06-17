// Package config holds the plain-data tuning for a match: the pitch Geometry, the
// match Ruleset, the physics Tuning, and the RNG seed. It is the single source of
// truth that flows from the command line and the menu into the simulation, renderer,
// and netcode, so nothing has to redefine pitch dimensions or rules on its own. It
// imports only internal/geom, so it stays headless and deterministic and can be linked
// into the server without pulling in any graphics.
package config

// Config is the full description of a match setup.
type Config struct {
	Geometry Geometry
	Ruleset  Ruleset
	Tuning   Tuning
	Seed     int64
}

// Default returns the configuration that reproduces the original game exactly: the
// standard pitch, a friendly (never-ending) ruleset, the baseline physics tuning, and
// a fixed seed for reproducible coin tosses.
func Default() Config {
	return Config{
		Geometry: StandardGeometry(),
		Ruleset:  DefaultRuleset(),
		Tuning:   DefaultTuning(),
		Seed:     1,
	}
}
