package sim

import (
	"image/color"

	"phootball/internal/geom"
	"phootball/internal/physics"
)

// obstacleRestitution is how bouncy fixed obstacles (cones) and goal posts are.
const obstacleRestitution = 0.5

// netRestitution is how much the goal net gives; low so it catches the ball rather
// than springing it back out.
const netRestitution = 0.2

// celebrationSeconds is how long after a goal before kickoff. Play continues
// normally during it -- the game is never paused.
const celebrationSeconds = 3.0

// Match is the complete simulation state and the unit of authoritative play. Step
// advances it by one fixed tick and is deterministic and headless (no Ebiten, no
// input, no wall-clock), so the server and the local client run identical physics.
type Match struct {
	Field     *Field
	Teams     [2]*Team // index 0 defends the left goal, index 1 the right
	Ball      *Ball
	Players   []*Player // flattened roster; stable iteration order for determinism
	Tick      uint64
	celebrate float64 // seconds until kickoff after a goal (play continues meanwhile)
}

// Celebrating reports whether a goal was just scored and the kickoff countdown is
// running. Play is not paused during it.
func (m *Match) Celebrating() bool { return m.celebrate > 0 }

// Step advances the match by one fixed timestep, applying each player's intent.
// inputs is keyed by PlayerID; a missing entry leaves that player idle.
func (m *Match) Step(inputs map[int]Intent, deltaTime float64) {
	// 1. Apply intents: aim, update charges, set (trap-slowed) movement, and latch a
	//    kick on the shoot button's release edge.
	for _, p := range m.Players {
		in := inputs[p.PlayerID]
		if in.Aim != (geom.Vec{}) {
			p.FaceTowards(in.Aim)
		}

		// Shoot charge: accumulate while held (capped); fire on the release edge.
		if in.ShootHeld {
			p.shootCharge += deltaTime
			if p.shootCharge > shootChargeMax {
				p.shootCharge = shootChargeMax
			}
		} else if p.shootHeldPrev {
			p.WantsKick = true // release: the charge is consumed in step 4
		}
		p.shootHeldPrev = in.ShootHeld

		// Trap charge: build toward 1 while held, decay otherwise.
		if in.Trap {
			p.trapCharge += deltaTime / trapChargeTime
			if p.trapCharge > 1 {
				p.trapCharge = 1
			}
		} else if p.trapCharge > 0 {
			p.trapCharge -= trapChargeDecay * deltaTime
			if p.trapCharge < 0 {
				p.trapCharge = 0
			}
		}

		// Trapping (right-click) and charging a shot (left-click) both slow the player --
		// a lower soft top speed AND a lower acceleration -- and charging slows it MORE
		// than trapping. Both scale with their charge. Set unconditionally from the base
		// so nothing drifts. (Radius is unchanged by default: TrapRadiusBonus is 0.)
		p.Body.SetRadius(p.Stats.Radius + p.Stats.TrapRadiusBonus*p.trapCharge)
		shootCharge := NormShootCharge(p.shootCharge)
		speedMul := (1 - p.trapCharge*(1-p.Stats.TrapSpeedFactor)) * (1 - shootCharge*(1-p.Stats.ShootSpeedFactor))
		accelMul := (1 - p.trapCharge*(1-p.Stats.TrapAccelFactor)) * (1 - shootCharge*(1-p.Stats.ShootAccelFactor))
		// Carrying the ball adds its inertia to the player, so the player accelerates a
		// little slower while the ball is at its feet (touching).
		if geom.Dist(p.Position, m.Ball.Position)-p.Radius()-m.Ball.Radius() < p.Stats.TouchRange {
			accelMul *= p.Stats.Mass / (p.Stats.Mass + m.Ball.Mass())
		}
		p.Body.MaxSpeed = p.Stats.MaxSpeed * speedMul
		p.Move(in.Move, in.Throttle*accelMul, deltaTime)
	}

	// 2. Integrate the dynamic bodies (the ball and the players).
	m.Ball.Update(deltaTime)
	for _, p := range m.Players {
		p.Body.Update(deltaTime)
	}

	// 2.5 Update each player's possession from this frame's geometry.
	for _, p := range m.Players {
		updatePossession(m.Ball, p, deltaTime)
	}

	// 3. Resolve collisions and the ball-player dribble interaction.
	m.resolveInteractions(deltaTime)

	// 4. Consume kick requests, scaling by the held charge, then clear it.
	for _, p := range m.Players {
		if p.WantsKick {
			shoot(p, m.Ball)
			p.WantsKick = false
			p.shootCharge = 0
		}
	}

	// 5. Goals. Play is never paused: while a goal's kickoff countdown runs, the
	// match keeps simulating but no new goal is counted; when it elapses we kick off.
	// Otherwise, detect a fresh goal and start the countdown.
	if m.celebrate > 0 {
		m.celebrate -= deltaTime
		if m.celebrate <= 0 {
			m.celebrate = 0
			m.resetKickoff()
		}
	} else if side := m.Field.CheckGoal(m.Ball); side != SideNone {
		m.addScore(side)
		m.celebrate = celebrationSeconds
	}

	m.Tick++
}

// resolveInteractions runs the per-tick collisions in a fixed order: ball off the
// walls, ball-player dribble, ball off obstacles, player-player, then players off
// obstacles and walls.
func (m *Match) resolveInteractions(deltaTime float64) {
	m.Field.ConfineBall(m.Ball)

	for _, p := range m.Players {
		handleBallToPlayerInteraction(m.Ball, p, deltaTime)
	}
	for _, o := range m.Field.Obstacles {
		physics.Collide(m.Ball.Body, o.Body, obstacleRestitution)
	}
	for _, g := range m.Field.Goals() {
		for _, post := range g.Posts {
			physics.Collide(m.Ball.Body, post, obstacleRestitution)
		}
		for _, seg := range g.Net {
			physics.Collide(m.Ball.Body, seg, netRestitution)
		}
	}

	for i := 0; i < len(m.Players); i++ {
		for j := i + 1; j < len(m.Players); j++ {
			physics.Resolve(m.Players[i].Body, m.Players[j].Body)
		}
	}
	for _, p := range m.Players {
		for _, o := range m.Field.Obstacles {
			physics.Collide(p.Body, o.Body, 0)
		}
		for _, g := range m.Field.Goals() {
			for _, post := range g.Posts {
				physics.Collide(p.Body, post, 0)
			}
			for _, seg := range g.Net {
				physics.Collide(p.Body, seg, 0)
			}
		}
		m.Field.ConfinePlayer(p)
	}
}

// addScore credits the team attacking the goal that was entered.
func (m *Match) addScore(goalEntered Side) {
	scorer := goalEntered.Opponent()
	for _, t := range m.Teams {
		if t.Side == scorer {
			t.Score++
		}
	}
}

// resetKickoff recentres the ball and returns every player to its home position.
func (m *Match) resetKickoff() {
	m.Ball.Position = m.Field.CenterSpot
	m.Ball.Velocity = geom.NewVec(0, 0)
	m.Ball.Acceleration = geom.NewVec(0, 0)
	for _, p := range m.Players {
		p.Position = p.HomePosition
		p.Velocity = geom.NewVec(0, 0)
		p.Acceleration = geom.NewVec(0, 0)
		p.possession = 0
		p.shootCharge = 0
		p.trapCharge = 0
		p.shootHeldPrev = false
		p.Body.SetRadius(p.Stats.Radius)
		p.Body.MaxSpeed = p.Stats.MaxSpeed
	}
}

// PlayerByID returns the player with the given id, or nil.
func (m *Match) PlayerByID(id int) *Player {
	for _, p := range m.Players {
		if p.PlayerID == id {
			return p
		}
	}
	return nil
}

// AttackingGoal returns the goal the team is trying to score in.
func (m *Match) AttackingGoal(t *Team) *Goal {
	if t.Side == SideLeft {
		return m.Field.RightGoal
	}
	return m.Field.LeftGoal
}

// DefendingGoal returns the goal the team must protect.
func (m *Match) DefendingGoal(t *Team) *Goal {
	return m.Field.GoalOn(t.Side)
}

// ClosestToBall reports whether p is its team's nearest player to the ball.
func (m *Match) ClosestToBall(p *Player) bool {
	closest := p
	best := geom.Dist(p.Position, m.Ball.Position)
	for _, q := range p.Team.Players {
		if d := geom.Dist(q.Position, m.Ball.Position); d < best {
			best, closest = d, q
		}
	}
	return closest == p
}

// BuildMatch creates a standard match: a centred field with a goal on each side,
// two teams of teamSize players in a simple formation, and the ball on the spot.
func BuildMatch(field *Field, teamSize int) *Match {
	left := &Team{Side: SideLeft, Name: "Blue", Color: color.RGBA{80, 140, 255, 255}}
	right := &Team{Side: SideRight, Name: "Red", Color: color.RGBA{255, 100, 100, 255}}

	m := &Match{
		Field: field,
		Teams: [2]*Team{left, right},
		Ball:  NewBall(field.CenterSpot, 7.5),
	}

	id := 0
	left.Players = buildFormation(field, left, teamSize, &id)
	right.Players = buildFormation(field, right, teamSize, &id)
	m.Players = append(m.Players, left.Players...)
	m.Players = append(m.Players, right.Players...)
	return m
}

// BuildSolo creates a single-player testing match: one human-controlled player with
// the default tuning and the ball, with no opponents and no obstacles. The opposing
// team exists but has an empty roster so scoring and rendering still work.
func BuildSolo(field *Field) *Match {
	left := &Team{Side: SideLeft, Name: "Blue", Color: color.RGBA{80, 140, 255, 255}}
	right := &Team{Side: SideRight, Name: "Red", Color: color.RGBA{255, 100, 100, 255}}

	m := &Match{
		Field: field,
		Teams: [2]*Team{left, right},
		Ball:  NewBall(field.CenterSpot, 7.5),
	}

	start := geom.NewVec(field.Min.X+field.Width()*0.25, field.CenterSpot.Y)
	p := NewPlayer(0, start, DefaultStats(500), left)
	p.Role = RoleMidfielder
	p.Number = 10
	left.Players = []*Player{p}
	m.Players = []*Player{p}
	return m
}

// BuildDuo creates a two-player testing match: one player on each side (no AI) that
// the human alternates control of. Good for testing dribbling, passing, and stealing
// by switching between them.
func BuildDuo(field *Field) *Match {
	left := &Team{Side: SideLeft, Name: "Blue", Color: color.RGBA{80, 140, 255, 255}}
	right := &Team{Side: SideRight, Name: "Red", Color: color.RGBA{255, 100, 100, 255}}

	m := &Match{
		Field: field,
		Teams: [2]*Team{left, right},
		Ball:  NewBall(field.CenterSpot, 7.5),
	}

	c := field.CenterSpot
	p0 := NewPlayer(0, geom.NewVec(c.X-120, c.Y), DefaultStats(500), left)
	p0.Role = RoleMidfielder
	p0.Number = 1
	p1 := NewPlayer(1, geom.NewVec(c.X+120, c.Y), DefaultStats(500), right)
	p1.Role = RoleMidfielder
	p1.Number = 2
	p1.Facing = geom.NewVec(-1, 0)

	left.Players = []*Player{p0}
	right.Players = []*Player{p1}
	m.Players = []*Player{p0, p1}
	return m
}

// buildFormation lays out one team's players across its own half: a keeper near the
// goal, the rest spread as midfielders and strikers.
func buildFormation(f *Field, team *Team, n int, id *int) []*Player {
	players := make([]*Player, 0, n)
	center := f.CenterSpot

	var ownX, dir float64
	face := geom.NewVec(1, 0)
	if team.Side == SideLeft {
		ownX, dir = f.Min.X, 1
	} else {
		ownX, dir, face = f.Max.X, -1, geom.NewVec(-1, 0)
	}

	for i := 0; i < n; i++ {
		role := RoleMidfielder
		var pos geom.Vec
		if i == 0 {
			role = RoleGoalkeeper
			pos = geom.NewVec(ownX+dir*40, center.Y)
		} else {
			if i%2 == 0 {
				role = RoleStriker
			}
			depth := 80 + (float64(i)/float64(n))*(f.Width()*0.35)
			spread := f.Height() * 0.6
			denom := float64(n - 1)
			if denom < 1 {
				denom = 1
			}
			y := center.Y - spread/2 + spread*float64(i-1)/denom
			pos = geom.NewVec(ownX+dir*depth, y)
		}
		p := NewPlayer(*id, pos, StatsForRole(role), team)
		p.Role = role
		p.Number = i + 1
		p.Facing = face
		players = append(players, p)
		*id++
	}
	return players
}
