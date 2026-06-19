// Package sim is the headless gameplay layer: entities, the ball/dribble/shoot rules,
// scoring, and the deterministic Match.Step that advances one tick. It imports physics,
// geom and config (the plain-data tuning), but never Ebiten, so the authoritative server
// runs the same simulation as the local client.
package sim

// clampUnit clamps a value to [0, 1]. (The tuning model has its own copy in config; this is
// the one the team-possession machinery in match.go uses.)
func clampUnit(v float64) float64 {
	if v > 1 {
		return 1
	}
	if v < 0 {
		return 0
	}
	return v
}
