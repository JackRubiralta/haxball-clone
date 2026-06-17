package sim

import (
	"phootball/internal/config"
	"phootball/internal/geom"
)

// ZoneRect is an axis-aligned rectangle in world coordinates used by the positional
// rules. It mirrors config.Rect but lives in the simulation so the rules can be written
// against sim types.
type ZoneRect struct {
	Min, Max geom.Vec
}

// ContainsPoint reports whether p lies within the rectangle (edges inclusive).
func (z ZoneRect) ContainsPoint(p geom.Vec) bool {
	return p.X >= z.Min.X && p.X <= z.Max.X && p.Y >= z.Min.Y && p.Y <= z.Max.Y
}

// empty reports a degenerate rect (a disabled box).
func (z ZoneRect) empty() bool { return z.Min.X >= z.Max.X || z.Min.Y >= z.Max.Y }

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
// for the attacking team; a full box is a solid keep-out for surplus same-team players.
// Both are off by default. Nothing here touches the ball or routes a player through
// physics.Resolve, and it is skipped during a goal celebration. Because it runs every
// tick after integration, a player never penetrates by more than one frame of motion.
func enforceZoneRules(m *Match, deltaTime float64) {
	if m.celebrate > 0 {
		return
	}
	r := m.Rules
	penActive := r.PenaltyBoxMaxPlayers > 0
	gkActive := r.GoalAreaMaxPlayers > 0
	if !r.OffsideEnabled && !penActive && !gkActive {
		return
	}

	carrier := m.ballCarrier()
	possessing := SideNone
	if carrier != nil {
		possessing = carrier.Team.Side
	}

	pending := make(map[int]pendingPush)

	// Box keep-out (no ball-carrier exemption: a wall is a wall). Penalty area first
	// (outer), then goal area (inner, stricter).
	for _, t := range m.Teams {
		if penActive {
			m.markBoxSurplus(t, m.Field.PenaltyAreaBox(t.Side), r.PenaltyBoxMaxPlayers, pending)
		}
		if gkActive {
			m.markBoxSurplus(t, m.Field.GoalAreaBox(t.Side), r.GoalAreaMaxPlayers, pending)
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
	rad := p.Radius()
	if p.Team.Side == SideLeft { // attacks toward +X
		if m.Ball.Position.X > lineX || p.Position.X+rad <= lineX {
			return pendingPush{}, false
		}
		vel := p.Velocity
		if vel.X > 0 {
			vel.X = -playerWallRestitution * vel.X
		}
		return pendingPush{pos: geom.NewVec(lineX-rad, p.Position.Y), vel: vel}, true
	}
	// SideRight attacks toward -X.
	if m.Ball.Position.X < lineX || p.Position.X-rad >= lineX {
		return pendingPush{}, false
	}
	vel := p.Velocity
	if vel.X < 0 {
		vel.X = -playerWallRestitution * vel.X
	}
	return pendingPush{pos: geom.NewVec(lineX+rad, p.Position.Y), vel: vel}, true
}

// markBoxSurplus walls every surplus same-team player out of a full box. Occupancy is
// counted by centre, in stable slice order (the keeper is index 0), so the first `max`
// keep their place and the box being full keeps every other same-team player's circle
// outside it -- blocking entry as well as evicting an over-capacity straggler.
func (m *Match) markBoxSurplus(t *Team, box ZoneRect, max int, pending map[int]pendingPush) {
	if box.empty() {
		return
	}
	allowed := make(map[int]bool, max)
	inside := 0
	for _, p := range t.Players {
		if box.ContainsPoint(p.Position) {
			inside++
			if len(allowed) < max {
				allowed[p.PlayerID] = true
			}
		}
	}
	if inside < max {
		return // room remains
	}
	leftSide := t.Side == SideLeft
	for _, p := range t.Players {
		if allowed[p.PlayerID] {
			continue
		}
		if push, ok := boxKeepOut(p, box, leftSide); ok {
			pending[p.PlayerID] = push
		}
	}
}

// boxKeepOut keeps a player's circle outside the box, pushing it out the
// least-penetration PITCH-FACING face (the goal-line face is the arena boundary, owned
// by ConfinePlayer, and is never an exit). Only the chosen axis's into-wall velocity is
// reflected, so the player slides along the face.
func boxKeepOut(p *Player, box ZoneRect, leftSide bool) (pendingPush, bool) {
	rad := p.Radius()
	c := p.Position
	// Fully outside the box expanded by the radius -> circle does not overlap.
	if c.X <= box.Min.X-rad || c.X >= box.Max.X+rad || c.Y <= box.Min.Y-rad || c.Y >= box.Max.Y+rad {
		return pendingPush{}, false
	}

	// Candidate faces: top, bottom, and the single inner-X (pitch-facing) face.
	upPen := c.Y - (box.Min.Y - rad)   // distance to clear out the top
	downPen := (box.Max.Y + rad) - c.Y // distance to clear out the bottom
	var xPen float64
	var xTarget float64
	if leftSide {
		xTarget = box.Max.X + rad // push right, into the pitch
		xPen = xTarget - c.X
	} else {
		xTarget = box.Min.X - rad // push left, into the pitch
		xPen = c.X - xTarget
	}

	vel := p.Velocity
	switch {
	case xPen <= upPen && xPen <= downPen:
		if leftSide && vel.X < 0 {
			vel.X = -playerWallRestitution * vel.X
		} else if !leftSide && vel.X > 0 {
			vel.X = -playerWallRestitution * vel.X
		}
		return pendingPush{pos: geom.NewVec(xTarget, c.Y), vel: vel}, true
	case upPen <= downPen:
		if vel.Y > 0 {
			vel.Y = -playerWallRestitution * vel.Y
		}
		return pendingPush{pos: geom.NewVec(c.X, box.Min.Y-rad), vel: vel}, true
	default:
		if vel.Y < 0 {
			vel.Y = -playerWallRestitution * vel.Y
		}
		return pendingPush{pos: geom.NewVec(c.X, box.Max.Y+rad), vel: vel}, true
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
