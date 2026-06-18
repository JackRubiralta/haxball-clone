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

type slot struct{ depth, width float64 } // normalized: depth 0=own goal..1=enemy goal

// idealPosition returns the world-space slot this outfielder should hold this tick.
func idealPosition(p perception, tune aiTuning) geom.Vec {
	f := p.view.Field()
	out := outfielders(p.view.Squad(p.me))
	idx := indexOf(out, p.me.ID())
	if idx < 0 {
		idx, out = 0, []sim.ObservedView{p.me} // keeper acting as a field player (tiny teams)
	}
	slots := formationSlots(len(out), tune)
	s := slots[idx]

	depth, width := s.depth, s.width
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

// formationSlots lays out k outfielders across defence/midfield/forward lines, spreading
// each line evenly across the pitch width. Lower indices fill the back line first, so the
// stable PlayerID sort maps low ids to defenders.
func formationSlots(k int, tune aiTuning) []slot {
	if k <= 0 {
		return nil
	}
	if k == 1 {
		return []slot{{depth: 0.45, width: 0.5}}
	}
	lines := formationLines(k)
	nonEmpty := 0
	for _, c := range lines {
		if c > 0 {
			nonEmpty++
		}
	}
	out := make([]slot, 0, k)
	li, single := 0, 0
	for _, c := range lines {
		if c == 0 {
			continue
		}
		depth := tune.defenderDepth
		if nonEmpty > 1 {
			depth = lerp(tune.defenderDepth, tune.forwardDepth, float64(li)/float64(nonEmpty-1))
		}
		for i := 0; i < c; i++ {
			width := 0.5
			if c > 1 {
				width = lerp(tune.widthMin, tune.widthMax, float64(i)/float64(c-1))
			} else {
				// A lone player in a line: stagger it off-centre, alternating sides line to
				// line, so successive single-player lines zig-zag instead of stacking into a
				// dead central column.
				if single%2 == 0 {
					width = 0.5 - 0.2
				} else {
					width = 0.5 + 0.2
				}
				single++
			}
			out = append(out, slot{depth: depth, width: width})
		}
		li++
	}
	return out
}

// formationLines splits k outfielders into back/middle/front line counts. It biases
// toward a solid back line and a lone-or-paired forward line, mirroring real small-sided
// shapes (e.g. 4 -> 1-2-1 diamond, 5 -> 2-2-1, 10 -> 3-5-2).
func formationLines(k int) []int {
	if k <= 0 {
		return nil
	}
	if k == 2 {
		return []int{1, 0, 1}
	}
	def := (k + 1) / 3
	fwd := k / 4
	if fwd < 1 {
		fwd = 1
	}
	mid := k - def - fwd
	for mid < 0 { // shrink the forward line, then the back line, until it fits
		if fwd > 1 {
			fwd--
		} else {
			def--
		}
		mid = k - def - fwd
	}
	return []int{def, mid, fwd}
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
		if q.Role() != sim.RoleGoalkeeper {
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
