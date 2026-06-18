package sim

import (
	"math"
	"sort"

	"phootball/internal/config"
	"phootball/internal/geom"
)

// boxLineClearance keeps a clamped / walled-out player's body just OUTSIDE a boundary marking
// rather than flush on it. The pitch markings are drawn PitchLineWidth wide, centred on the
// geometric boundary, so a player clamped edge-flush would cover the near half of the line. This
// is that half-width -- single-sourced from the SAME PitchLineWidth the renderer draws the line at
// (and the box geometry the physics uses), so the player's edge rests exactly at the line's outer
// edge and never overlaps it for the current markings. Shared by every positional-rule wall (box
// keep-out + offside) for a uniform look. (The arena boundary in Field.ConfinePlayer has the same
// flush clamp and could use this too -- left untouched here to avoid changing core arena physics.)
const boxLineClearance = PitchLineWidth / 2

// ZoneRect is an axis-aligned rectangle in world coordinates used by the positional
// rules. It mirrors config.Rect but lives in the simulation so the rules can be written
// against sim types.
type ZoneRect struct {
	Min, Max geom.Vec
}

// empty reports a degenerate rect (a disabled box).
func (z ZoneRect) empty() bool { return z.Min.X >= z.Max.X || z.Min.Y >= z.Max.Y }

// overlapsCircle reports whether a circle (centre c, radius rad) overlaps the rectangle -- i.e.
// ANY part of the circle is inside the box, not just its centre. It measures the distance from c
// to the nearest point of the rect (the centre clamped to the rect), so corners are rounded
// correctly. This is how box occupancy is counted: a player is "in the box" the moment its body
// touches it.
func (z ZoneRect) overlapsCircle(c geom.Vec, rad float64) bool {
	nx := c.X
	if nx < z.Min.X {
		nx = z.Min.X
	} else if nx > z.Max.X {
		nx = z.Max.X
	}
	ny := c.Y
	if ny < z.Min.Y {
		ny = z.Min.Y
	} else if ny > z.Max.Y {
		ny = z.Max.Y
	}
	dx, dy := c.X-nx, c.Y-ny
	return dx*dx+dy*dy < rad*rad
}

// pendingPush is a resolved wall correction: the player's clamped position and the
// velocity with its into-wall component reflected. It is computed before being applied
// so the warn-evict grace can gate it.
type pendingPush struct {
	pos geom.Vec
	vel geom.Vec
}

// enforceZoneRules applies the configured positional rules each tick as PHYSICAL WALLS,
// exactly like the arena boundary in Field.ConfinePlayer: a player is stopped at the
// boundary (its edge clamped flush) and slides along it, with only the into-wall
// velocity component reflected -- never teleported. The offside line is a vertical wall
// for the attacking team; a full box is a solid keep-out for surplus players, with the
// defending team and the opponent each capped separately. All are off by default. Nothing here touches the ball or routes a player through
// physics.Resolve, and it is skipped during a goal celebration. Because it runs every
// tick after integration, a player never penetrates by more than one frame of motion.
func enforceZoneRules(m *Match, deltaTime float64) {
	if m.celebrate > 0 {
		return
	}
	r := m.Rules
	penActive := r.PenaltyBoxMaxPlayers > 0 || r.PenaltyBoxMaxOpponents > 0
	gkActive := r.GoalAreaKeeperOnly || r.GoalAreaMaxPlayers > 0 || r.GoalAreaMaxOpponents > 0
	if !r.OffsideEnabled && !penActive && !gkActive {
		return
	}

	carrier := m.ballCarrier()
	possessing := SideNone
	if carrier != nil {
		possessing = carrier.Team.Side
	}

	pending := make(map[int]pendingPush)

	// Box caps (no ball-carrier exemption: a wall is a wall). Each team's own area limits BOTH
	// that team's players (defenders) AND the opponent's (attackers), each with its own cap, as a
	// barrier. Penalty area first (outer), then goal area (inner, stricter).
	for _, t := range m.Teams {
		opp := m.opponentTeam(t)
		if penActive {
			box := m.Field.PenaltyAreaBox(t.Side)
			m.enforceBoxCap(box, t.Side, t, r.PenaltyBoxMaxPlayers, pending)
			m.enforceBoxCap(box, t.Side, opp, r.PenaltyBoxMaxOpponents, pending)
		}
		if gkActive {
			box := m.Field.GoalAreaBox(t.Side)
			if r.GoalAreaKeeperOnly {
				// Keeper-only: admit ONLY the box owner's keeper; wall out every other player
				// (own team and opponents). A keeper-only goal area has no opponents either.
				m.enforceBoxKeeperOnly(box, t.Side, t, pending)
				m.enforceBoxKeeperOnly(box, t.Side, opp, pending)
			} else {
				m.enforceBoxCap(box, t.Side, t, r.GoalAreaMaxPlayers, pending)
				m.enforceBoxCap(box, t.Side, opp, r.GoalAreaMaxOpponents, pending)
			}
		}
	}

	// Offside wall (a box wall already claimed takes priority).
	if r.OffsideEnabled {
		for _, p := range m.Players {
			if _, taken := pending[p.PlayerID]; taken {
				continue
			}
			if push, ok := m.offsidePush(p, carrier, possessing, r); ok {
				pending[p.PlayerID] = push
			}
		}
	}

	// Apply, honouring the warn-evict grace via the shared dwell timer.
	for _, p := range m.Players {
		push, violating := pending[p.PlayerID]
		if !violating {
			p.evictDwell = 0
			continue
		}
		p.evictDwell += deltaTime
		if r.Enforcement == config.EnforceWarnEvict && p.evictDwell < r.EvictGrace {
			continue
		}
		p.Position = push.pos
		p.Velocity = push.vel
	}
}

// offsidePush clamps an attacker's leading edge to the offside line and reflects the
// into-line X velocity, leaving Y untouched so the player slides along the line. The
// ball carrier, the team in possession, and play that has already moved past the line
// are exempt.
func (m *Match) offsidePush(p *Player, carrier *Player, possessing Side, r config.Ruleset) (pendingPush, bool) {
	if p == carrier || possessing == p.Team.Side {
		return pendingPush{}, false
	}
	frac := r.OffsideFrac
	if frac <= 0 {
		frac = 2.0 / 3.0
	}
	lineX := m.Field.OffsideLineX(p.Team.Side, frac)
	// margin = body radius + the line half-width, so the held player rests just clear of the
	// offside marking rather than overlapping it (same clearance as the box walls).
	margin := p.Radius() + boxLineClearance
	if p.Team.Side == SideLeft { // attacks toward +X
		if m.Ball.Position.X > lineX || p.Position.X+margin <= lineX {
			return pendingPush{}, false
		}
		vel := p.Velocity
		if vel.X > 0 {
			vel.X = -playerWallRestitution * vel.X
		}
		return pendingPush{pos: geom.NewVec(lineX-margin, p.Position.Y), vel: vel}, true
	}
	// SideRight attacks toward -X.
	if m.Ball.Position.X < lineX || p.Position.X-margin >= lineX {
		return pendingPush{}, false
	}
	vel := p.Velocity
	if vel.X < 0 {
		vel.X = -playerWallRestitution * vel.X
	}
	return pendingPush{pos: geom.NewVec(lineX+margin, p.Position.Y), vel: vel}, true
}

// opponentTeam returns the other team (the one t does not belong to).
func (m *Match) opponentTeam(t *Team) *Team {
	if m.Teams[0] == t {
		return m.Teams[1]
	}
	return m.Teams[0]
}

// enforceBoxCap keeps at most `max` of `team`'s players in `box`, treating the box as a BARRIER.
// A player counts as in the box if ANY part of its circle overlaps it (overlapsCircle, not just
// the centre). Of the players in the box, the ones KEPT are the box-owner's keeper (always, it
// lives there) plus the MOST-ESTABLISHED (deepest inside) up to the cap; every surplus player has
// its circle walled out through the nearest pitch-facing face. Ranking by how deep a player sits
// inside -- not by roster order -- means a player only just poking in is the one blocked, never a
// settled occupant: pushing in can no longer eject someone who was already established inside.
// boxSide is the goal the box guards (selects the pitch-facing exit; the goal-line face is the
// arena boundary, never an exit). max <= 0 disables this cap. The same box is capped independently
// for each team, so the defending side and the attacking side get their own limits.
func (m *Match) enforceBoxCap(box ZoneRect, boxSide Side, team *Team, max int, pending map[int]pendingPush) {
	if max <= 0 || box.empty() {
		return
	}
	leftSide := boxSide == SideLeft
	ownerKeeper := team.Side == boxSide // the box belongs to this team -> its keeper always keeps its place

	type occupant struct {
		p      *Player
		depth  float64
		keeper bool
	}
	occ := make([]occupant, 0, len(team.Players))
	for _, p := range team.Players {
		if !box.overlapsCircle(p.Position, p.Radius()) {
			continue // body not in the box
		}
		occ = append(occ, occupant{p, boxInsideness(p, box, leftSide), ownerKeeper && p.Role == RoleGoalkeeper})
	}

	// Keep the owner's keeper first, then the deepest-inside up to the cap; wall out the rest.
	// Stable sort so roster order breaks ties deterministically.
	sort.SliceStable(occ, func(i, j int) bool {
		if occ[i].keeper != occ[j].keeper {
			return occ[i].keeper
		}
		return occ[i].depth > occ[j].depth
	})
	for i := range occ {
		if i < max {
			continue // within the cap -> keeps its place
		}
		if push, ok := boxKeepOut(occ[i].p, box, leftSide); ok { // surplus -> walled out
			pending[occ[i].p.PlayerID] = push
		}
	}
}

// enforceBoxKeeperOnly is the keeper-only goal-area mode: of `team`'s players overlapping `box`,
// ONLY the box owner's keeper is admitted; every other player (and every player of a team that is
// not the box owner -- i.e. the opponent) is walled out at the line, exactly like enforceBoxCap's
// surplus wall. It is never a teleport: each surplus player is clamped one frame just outside the
// nearest pitch-facing face with its into-wall velocity reflected (boxKeepOut). boxSide is the goal
// the box guards.
func (m *Match) enforceBoxKeeperOnly(box ZoneRect, boxSide Side, team *Team, pending map[int]pendingPush) {
	if box.empty() {
		return
	}
	leftSide := boxSide == SideLeft
	owner := team.Side == boxSide
	for _, p := range team.Players {
		if !box.overlapsCircle(p.Position, p.Radius()) {
			continue // body not in the box
		}
		if owner && p.Role == RoleGoalkeeper {
			continue // the owner's keeper is the only admitted player
		}
		if push, ok := boxKeepOut(p, box, leftSide); ok {
			pending[p.PlayerID] = push
		}
	}
}

// boxInsideness measures how deep a player's circle sits inside the box: the distance from the
// nearest face it would be pushed out through (the same faces boxKeepOut uses). Larger = more
// settled inside; a player only just overlapping a face scores near zero. The cap keeps the
// deepest occupants so a player merely poking in is the one walled out.
func boxInsideness(p *Player, box ZoneRect, leftSide bool) float64 {
	rad := p.Radius()
	c := p.Position
	upPen := c.Y - (box.Min.Y - rad)
	downPen := (box.Max.Y + rad) - c.Y
	var xPen float64
	if leftSide {
		xPen = (box.Max.X + rad) - c.X
	} else {
		xPen = c.X - (box.Min.X - rad)
	}
	return math.Min(xPen, math.Min(upPen, downPen))
}

// boxKeepOut keeps a player's circle outside the box, pushing it out the
// least-penetration PITCH-FACING face (the goal-line face is the arena boundary, owned
// by ConfinePlayer, and is never an exit). The chosen axis's into-wall velocity is ZEROED
// (no bounce off the goal/penalty box), so the player stops dead at the line and slides
// along the face; the along-face component is left untouched.
func boxKeepOut(p *Player, box ZoneRect, leftSide bool) (pendingPush, bool) {
	// margin clears the player's body PLUS the boundary line's half-width, so the walled-out
	// player rests just outside the marking instead of overlapping it.
	margin := p.Radius() + boxLineClearance
	c := p.Position
	// Fully outside the box expanded by the margin -> nothing to push.
	if c.X <= box.Min.X-margin || c.X >= box.Max.X+margin || c.Y <= box.Min.Y-margin || c.Y >= box.Max.Y+margin {
		return pendingPush{}, false
	}

	// Candidate faces: top, bottom, and the single inner-X (pitch-facing) face.
	upPen := c.Y - (box.Min.Y - margin)   // distance to clear out the top
	downPen := (box.Max.Y + margin) - c.Y // distance to clear out the bottom
	var xPen float64
	var xTarget float64
	if leftSide {
		xTarget = box.Max.X + margin // push right, into the pitch
		xPen = xTarget - c.X
	} else {
		xTarget = box.Min.X - margin // push left, into the pitch
		xPen = c.X - xTarget
	}

	// Zero bounce: KILL the into-wall velocity component (set to 0) rather than reflecting it, so the
	// player stops dead at the box line instead of rebounding off it. The other (along-the-face)
	// component is left untouched, so the player still slides freely along the line.
	vel := p.Velocity
	switch {
	case xPen <= upPen && xPen <= downPen:
		if (leftSide && vel.X < 0) || (!leftSide && vel.X > 0) {
			vel.X = 0
		}
		return pendingPush{pos: geom.NewVec(xTarget, c.Y), vel: vel}, true
	case upPen <= downPen:
		if vel.Y > 0 {
			vel.Y = 0
		}
		return pendingPush{pos: geom.NewVec(c.X, box.Min.Y-margin), vel: vel}, true
	default:
		if vel.Y < 0 {
			vel.Y = 0
		}
		return pendingPush{pos: geom.NewVec(c.X, box.Max.Y+margin), vel: vel}, true
	}
}

// ballCarrier returns the player currently in firm possession of the ball, or nil.
func (m *Match) ballCarrier() *Player {
	for _, p := range m.Players {
		if p.possession < 0.5 {
			continue
		}
		if geom.Dist(p.Position, m.Ball.Position)-p.Radius()-m.Ball.Radius() < p.Stats.TouchRange {
			return p
		}
	}
	return nil
}
