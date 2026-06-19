package control

import (
	"sort"

	"phootball/internal/geom"
	"phootball/internal/sim"
)

// The formation layer turns (role, team size, field geometry, ball position) into a
// dynamic "ideal slot" for each outfielder. The whole team shape slides toward the ball
// (team shape) and shifts up/down the pitch with possession, but every player keeps a
// distinct lane, so players spread out instead of swarming the ball. Only the elected
// presser leaves its slot to chase.

// idealPosition returns the world-space slot this outfielder should hold this tick.
func idealPosition(p perception, tune aiTuning) geom.Vec {
	f := p.view.Field()
	depth, width := roleSlot(p, tune)
	// Shift the line up when we have the ball, drop it when defending.
	switch {
	case p.carrierMine:
		depth += tune.attackBias
	case p.carrierEnemy || ballInOwnHalf(p):
		depth -= tune.defendBias
	}
	depth = clampFloat(depth, 0.06, 0.94)
	width = clampFloat(width, 0.05, 0.95)

	base := worldFromFrac(p, depth, width)

	// Slide the whole block toward the ball (team shape).
	c := f.CenterSpot()
	base = base.Add(geom.NewVec((p.ball.X-c.X)*tune.ballShiftX, (p.ball.Y-c.Y)*tune.ballShiftY))

	// A little deterministic per-player wander so mirrored formations never line up exactly.
	base = base.Add(geom.NewVec(
		personality(p.me.ID(), 11^p.seed)*tune.slotJitter,
		personality(p.me.ID(), 12^p.seed)*tune.slotJitter,
	))

	base = confineSlot(p, base)
	if kickoffActive(p) {
		base = clampOwnHalf(p, base)
	}
	base = clampZoneRules(p, base)
	return base
}

// roleSlot returns this outfielder's normalized (depth, width) slot from its AUTHORITATIVE
// sim.Role() -- defenders hold a deep band, midfielders the middle, attackers high up -- with
// players sharing a role fanned across the pitch width in a stable, observable order. Reading
// the role directly (rather than re-deriving a line from PlayerID order) makes the four roles
// behaviourally real: a defender stays back and an attacker pushes on, which staggers the team
// into distinct depth lines so a carrier always has a short option at more than one height.
func roleSlot(p perception, tune aiTuning) (depth, width float64) {
	role := p.me.Role()
	depth = roleDepth(role, tune)
	// Fan players sharing this role across the width, ordered by observable ID so every teammate
	// derives the SAME lanes without communicating (the AI<=human boundary: assignments come from
	// observable identity, never hidden state).
	peers := sameRole(p.view.Squad(p.me), role)
	idx := indexOf(peers, p.me.ID())
	if n := len(peers); n > 1 && idx >= 0 {
		width = lerp(tune.widthMin, tune.widthMax, float64(idx)/float64(n-1))
	} else {
		// A lone player in its line: offset off-centre per role so successive single-player lines
		// zig-zag instead of stacking into a dead central column (the lone striker stays central
		// for a shot). The slot jitter in idealPosition further de-aligns mirrored teams.
		width = 0.5 + roleStagger(role)
	}
	return depth, width
}

// roleDepth maps a role to its normalized back-to-front depth band (0 own goal..1 enemy goal).
func roleDepth(role sim.Role, tune aiTuning) float64 {
	switch role {
	case sim.RoleDefender:
		return tune.defenderDepth
	case sim.RoleAttacker:
		return tune.forwardDepth
	default: // midfielder, or a keeper pressed into outfield duty on a tiny team
		return tune.midfielderDepth
	}
}

// roleStagger offsets a lone player of a role off-centre by +-0.2 so successive single-player
// lines zig-zag (def/att to one side, mid to the other) instead of stacking into a dead central
// column. The +-0.2 magnitude and the def/mid alternation reproduce the previous formation's
// lone-line stagger, so introducing role-keyed slots stays shape-preserving.
func roleStagger(role sim.Role) float64 {
	if role == sim.RoleMidfielder {
		return 0.2
	}
	return -0.2 // defenders and attackers to the opposite side from a lone midfielder
}

// sameRole returns the players in the roster that share role, sorted by PlayerID -- a stable,
// observable order every teammate computes identically.
func sameRole(players []sim.ObservedView, role sim.Role) []sim.ObservedView {
	out := make([]sim.ObservedView, 0, len(players))
	for _, q := range players {
		if q.Role() == role {
			out = append(out, q)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID() < out[j].ID() })
	return out
}

// worldFromFrac maps a normalized (depth, width) slot to world coordinates for this
// player's team, accounting for which direction the team attacks.
func worldFromFrac(p perception, depth, width float64) geom.Vec {
	f := p.view.Field()
	var x float64
	if p.me.Side() == sim.SideLeft {
		x = f.Min().X + depth*f.Width()
	} else {
		x = f.Max().X - depth*f.Width()
	}
	y := f.Min().Y + width*f.Height()
	return geom.NewVec(x, y)
}

// confineSlot keeps a slot inside the play area with a small margin so players do not aim
// into the walls.
func confineSlot(p perception, v geom.Vec) geom.Vec {
	f := p.view.Field()
	m := p.me.Radius() + 4
	return clampVec(v, geom.NewVec(f.Min().X+m, f.Min().Y+m), geom.NewVec(f.Max().X-m, f.Max().Y-m))
}

// ballInOwnHalf reports whether the ball is on this player's defensive side of halfway.
func ballInOwnHalf(p perception) bool {
	return (p.ball.X-p.view.Field().CenterSpot().X)*p.attackX < 0
}

// outfielders returns the non-keeper players from a roster, sorted by PlayerID.
//
// ID->slot convention: sim.buildFormation assigns PlayerIDs in a stable formation order (the
// keeper first, then outfielders back-to-front), so sorting the non-keepers by ID gives every
// teammate the SAME slot ordering to assign roles/zones against (see assignRoles, zones.go).
// The order must be derivable from observable identity alone (ID/Role) -- never from hidden
// state -- so all teammates compute identical assignments without communicating.
func outfielders(players []sim.ObservedView) []sim.ObservedView {
	out := make([]sim.ObservedView, 0, len(players))
	for _, q := range players {
		if q.Role() != sim.RoleKeeper {
			out = append(out, q)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID() < out[j].ID() })
	return out
}

func indexOf(list []sim.ObservedView, id int) int {
	for i, q := range list {
		if q.ID() == id {
			return i
		}
	}
	return -1
}
