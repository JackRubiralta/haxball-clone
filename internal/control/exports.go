package control

import (
	"phootball/internal/geom"
	"phootball/internal/sim"
)

// This file exposes a small, behavior-preserving surface of the rule-based AI's
// human-reachability guards so the neural controller (internal/control/neural, a separate
// package) can reuse them without an import cycle. None of this changes how the rule AI plays;
// the AI's own methods delegate to the same logic.

// AimProjectDist is how far an Aim point is projected from the player (the sim faces toward
// the aim point, so the distance only needs to be "far"). Mirrors the AI's internal constant.
const AimProjectDist = aimProjectDist

// AimCapGap is the surface gap to the ball beyond which off-ball aim is turn-rate limited.
const AimCapGap = aimCapGap

// DefaultMaxTurnRad is the AI's maximum facing change per decision (the anti-snap-turn cap),
// taken from the default AI tuning so the neural controller is held to the identical limit.
var DefaultMaxTurnRad = defaultAITuning().maxTurnRad

// EnforceAbilityExclusivity clamps an intent so at most one of Trap > Push > Shoot is active,
// exactly as the human controller and the rule AI do (a player has three mutually-exclusive
// mouse buttons). A higher-priority ability cancels a live shot charge (dropped, not fired).
func EnforceAbilityExclusivity(in sim.Intent) sim.Intent { return enforceAbilityExclusivity(in) }

// CapAim caps how fast a controller may re-orient: it rotates facing toward the aimed
// direction by at most maxTurnRad and re-projects far, so the disk can never snap-turn. It is
// the free-function form of the AI's capAim, parameterized on the player's position/facing so
// any controller can reuse it.
func CapAim(in sim.Intent, pos, facing geom.Vec, maxTurnRad float64) sim.Intent {
	if in.Aim == (geom.Vec{}) {
		return in
	}
	desired := geom.Unit(in.Aim.Sub(pos))
	if facing == (geom.Vec{}) || desired == (geom.Vec{}) {
		return in
	}
	capped := rotateToward(facing, desired, maxTurnRad)
	in.Aim = pos.Add(capped.Scale(aimProjectDist))
	return in
}
