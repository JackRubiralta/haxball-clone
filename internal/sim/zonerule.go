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

// enforceZoneRules applies the configured positional rules each tick: anti-camp offside
// and keeper-box occupancy. Both are off by default. Enforcement is a soft clamp on the
// X axis (mirroring ConfinePlayer): the player is pushed back to the boundary and the
// offending velocity component is reflected with playerWallRestitution. Nothing here
// ever touches the ball or routes a player through physics.Resolve. It runs after
// collisions and before goal detection, and is skipped during a goal celebration.
func enforceZoneRules(m *Match, deltaTime float64) {
	if m.celebrate > 0 {
		return
	}
	r := m.Rules
	gkActive := r.GKBoxEnabled && r.GKBoxMax > 0
	if !r.OffsideEnabled && !gkActive {
		return
	}

	// The ball carrier and its team are exempt from the offside line.
	carrier := m.ballCarrier()
	possessing := SideNone
	if carrier != nil {
		possessing = carrier.Team.Side
	}

	// Keeper-box surplus: mark players beyond the allowed count in their own goal area,
	// in stable slice order (the keeper, index 0, is kept).
	surplus := make(map[int]bool)
	if gkActive {
		for _, t := range m.Teams {
			box := m.Field.GoalAreaBox(t.Side)
			count := 0
			for _, p := range t.Players {
				if box.ContainsPoint(p.Position) {
					count++
					if count > r.GKBoxMax {
						surplus[p.PlayerID] = true
					}
				}
			}
		}
	}

	for _, p := range m.Players {
		limitX, keepBelow, violates := m.zoneClamp(p, carrier, possessing, surplus[p.PlayerID])
		if !violates {
			p.evictDwell = 0
			continue
		}
		p.evictDwell += deltaTime
		if r.Enforcement == config.EnforceWarnEvict && p.evictDwell < r.EvictGrace {
			continue
		}
		clampPlayerX(p, limitX, keepBelow)
	}
}

// zoneClamp returns the X limit a player must respect this tick, the side of it the
// player must stay on (keepBelow = stay at X <= limit), and whether the player is
// currently violating a rule. Keeper-box surplus takes priority over offside.
func (m *Match) zoneClamp(p *Player, carrier *Player, possessing Side, surplus bool) (limitX float64, keepBelow bool, violates bool) {
	if surplus {
		box := m.Field.GoalAreaBox(p.Team.Side)
		if p.Team.Side == SideLeft {
			return box.Max.X + p.Radius(), false, true // edge fully out the front (toward the pitch)
		}
		return box.Min.X - p.Radius(), true, true
	}

	if m.Rules.OffsideEnabled {
		frac := m.Rules.OffsideFrac
		if frac <= 0 {
			frac = 2.0 / 3.0
		}
		lineX := m.Field.OffsideLineX(p.Team.Side, frac)
		attackRight := p.Team.Side == SideLeft
		beyond := (attackRight && p.Position.X > lineX) || (!attackRight && p.Position.X < lineX)
		if beyond && p != carrier && possessing != p.Team.Side {
			ballUp := (attackRight && m.Ball.Position.X > lineX) || (!attackRight && m.Ball.Position.X < lineX)
			if !ballUp {
				return lineX, attackRight, true
			}
		}
	}
	return 0, false, false
}

// clampPlayerX holds a player on the allowed side of limitX, reflecting the velocity
// component that points across the line.
func clampPlayerX(p *Player, limitX float64, keepBelow bool) {
	if keepBelow {
		if p.Position.X > limitX {
			p.Position.X = limitX
			if p.Velocity.X > 0 {
				p.Velocity.X = -playerWallRestitution * p.Velocity.X
			}
		}
		return
	}
	if p.Position.X < limitX {
		p.Position.X = limitX
		if p.Velocity.X < 0 {
			p.Velocity.X = -playerWallRestitution * p.Velocity.X
		}
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
