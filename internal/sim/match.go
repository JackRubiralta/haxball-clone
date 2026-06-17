package sim

import (
	"image/color"
	"math/rand"

	"phootball/internal/config"
	"phootball/internal/geom"
	"phootball/internal/physics"
)

// obstacleRestitution is how bouncy fixed cone obstacles are.
const obstacleRestitution = 0.5

// netRestitution is how much the goal net gives; low so it catches the ball rather
// than springing it back out.
const netRestitution = 0.2

// Wall bounce restitution: the speed a body keeps when it bounces off a solid boundary
// (the pitch walls and the goal frame/posts). Separate values for the ball and players
// by design: the ball stays lively (keeps 90% -> absorbs ~10%) while a player is damped
// hard (keeps 50% -> absorbs ~50%), so running into a wall costs a player real
// momentum. Players now bounce off these surfaces rather than dead-stopping.
const (
	ballWallRestitution   = 0.90
	playerWallRestitution = 0.50
)

// Match is the complete simulation state and the unit of authoritative play. Step
// advances it by one fixed tick and is deterministic and headless (no Ebiten, no
// input, no wall-clock), so the server and the local client run identical physics.
type Match struct {
	Field   *Field
	Teams   [2]*Team // index 0 defends the left goal, index 1 the right
	Ball    *Ball
	Players []*Player // flattened roster; stable iteration order for determinism
	Tick    uint64

	Rules  config.Ruleset // how the match is won and how a draw is resolved
	Tuning config.Tuning  // physics constants
	State  MatchState     // where the match is in its rules progression
	Paused bool           // set by the front end; while true Step does nothing
	Clock  float64        // total elapsed match time in seconds (+= dt each live tick)

	rng       *rand.Rand   // deterministic, seeded; used only for coin tosses
	celebrate float64      // seconds until kickoff after a goal (play continues meanwhile)
	shootout  *Shootout    // set only while Phase is PhasePenalties
	sounds    []SoundEvent // sound events emitted this tick (drained by the client)

	// Scoring attribution.
	LastTouch    *Touch       // the most recent toucher (nil at kickoff)
	touchHistory []Touch      // recent distinct touchers, oldest first
	Goals        []ScoreEvent // every goal's resolved attribution, in order
	LastGoal     *ScoreEvent  // the most recent goal's attribution (nil before the first)
}

// Celebrating reports whether a goal was just scored and the kickoff countdown is
// running. Play is not paused during it.
func (m *Match) Celebrating() bool { return m.celebrate > 0 }

// applyConfig stamps a match with its ruleset, physics tuning, and seeded RNG.
func (m *Match) applyConfig(cfg config.Config) {
	m.Rules = cfg.Ruleset
	m.Tuning = cfg.Tuning
	m.rng = newRNG(cfg.Seed)
}

// Step advances the match by one fixed timestep, applying each player's intent.
// inputs is keyed by PlayerID; a missing entry leaves that player idle.
func (m *Match) Step(inputs map[int]Intent, deltaTime float64) {
	// A paused or finished match does not advance at all (deterministic: no clock, no
	// physics), so a local pause and a network resume are bit-identical.
	if m.Frozen() {
		return
	}
	m.Clock += deltaTime
	m.sounds = m.sounds[:0] // start a fresh batch of sound events for this tick

	// A penalty shootout has its own restricted pipeline (only the taker and keeper
	// move); it is the sole resolver of the match while it runs.
	if m.State.Phase == PhasePenalties {
		m.stepShootout(inputs, deltaTime)
		m.Tick++
		return
	}

	// 1. Apply each player's intent: aim, charges, trap-slowed movement, kick latch.
	for _, p := range m.Players {
		m.applyIntent(p, inputs[p.PlayerID], deltaTime)
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
			if shoot(p, m.Ball) {
				m.recordTouch(p, TouchKick)
				m.emit(SoundKick, geom.Norm(m.Ball.Velocity), m.Ball.Position)
			}
			p.WantsKick = false
			p.shootCharge = 0
		}
	}

	// 4.5 Positional rules (offside anti-camp, keeper-box occupancy). Off by default;
	// enforced as a soft clamp after collisions and before goal detection.
	enforceZoneRules(m, deltaTime)

	// 5. Rules: goal detection, the kickoff celebration, win conditions, and the
	// draw-resolution chain (extra time, golden goal, penalties). Play is never paused
	// for a goal -- the match keeps simulating during the celebration countdown.
	m.advanceRules(deltaTime)

	m.Tick++
}

// applyIntent applies one player's intent for this tick: aim, the shoot/trap charge
// update, the trap/charge/possession speed penalties, and movement. It does not
// integrate the body (step 2 does). Shared by normal play and the penalty shootout.
func (m *Match) applyIntent(p *Player, in Intent, deltaTime float64) {
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
		p.WantsKick = true
	}
	p.shootHeldPrev = in.ShootHeld

	// Trap charge: build toward 1 while held, decay otherwise.
	if in.Trap && !p.trapHeldPrev {
		m.emit(SoundTrap, 1, p.Position) // rising edge of the trap button
	}
	p.trapHeldPrev = in.Trap
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

	// Trapping and charging a shot both slow the player; charging slows it more. Set
	// unconditionally from the base so nothing drifts.
	p.Body.SetRadius(p.Stats.Radius + p.Stats.TrapRadiusBonus*p.trapCharge)
	shootCharge := NormShootCharge(p.shootCharge)
	speedMul := (1 - p.trapCharge*(1-p.Stats.TrapSpeedFactor)) * (1 - shootCharge*(1-p.Stats.ShootSpeedFactor))
	accelMul := (1 - p.trapCharge*(1-p.Stats.TrapAccelFactor)) * (1 - shootCharge*(1-p.Stats.ShootAccelFactor))
	// Possession penalty: a ball at the player's feet costs a little speed and accel.
	if geom.Dist(p.Position, m.Ball.Position)-p.Radius()-m.Ball.Radius() < p.Stats.TouchRange {
		speedMul *= p.Stats.PossessionSpeedFactor
		accelMul *= p.Stats.PossessionAccelFactor
	}
	p.Body.MaxSpeed = p.Stats.MaxSpeed * speedMul
	p.Move(in.Move, in.Throttle*accelMul, deltaTime)
}

// teamFor returns the team defending the given side.
func (m *Match) teamFor(side Side) *Team {
	if m.Teams[0].Side == side {
		return m.Teams[0]
	}
	return m.Teams[1]
}

// resolveInteractions runs the per-tick collisions in a fixed order: ball off the
// walls, ball-player dribble, ball off obstacles, player-player, then players off
// obstacles and walls.
func (m *Match) resolveInteractions(deltaTime float64) {
	if spd := m.Field.ConfineBall(m.Ball); spd > ballHitMinSpeed {
		m.emit(SoundBallHit, spd, m.Ball.Position)
	}

	for _, p := range m.Players {
		if touched, bounce := handleBallToPlayerInteraction(m.Ball, p, deltaTime); touched {
			m.recordTouch(p, TouchDribble)
			if bounce > ballHitMinSpeed {
				m.emit(SoundBallHit, bounce, m.Ball.Position)
			}
		}
	}
	for _, o := range m.Field.Obstacles {
		physics.Collide(m.Ball.Body, o.Body, obstacleRestitution)
	}
	for _, g := range m.Field.Goals() {
		for _, post := range g.Posts {
			physics.Collide(m.Ball.Body, post, ballWallRestitution)
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
			physics.Collide(p.Body, o.Body, playerWallRestitution)
		}
		for _, g := range m.Field.Goals() {
			for _, post := range g.Posts {
				physics.Collide(p.Body, post, playerWallRestitution)
			}
			for _, seg := range g.Net {
				physics.Collide(p.Body, seg, playerWallRestitution)
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

// resetKickoff recentres the ball and returns every player to its home position. The
// touch history is cleared so a goal can never be attributed across a kickoff; the
// goal log and the match clock are kept.
func (m *Match) resetKickoff() {
	m.LastTouch = nil
	m.touchHistory = m.touchHistory[:0]
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
		p.trapHeldPrev = false
		p.evictDwell = 0
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
	m.applyConfig(config.Default())
	return m
}

// BuildMatchFromConfig builds a standard match and applies a full config (ruleset,
// physics tuning, RNG seed). The field is expected to be built from cfg.Geometry.
func BuildMatchFromConfig(field *Field, teamSize int, cfg config.Config) *Match {
	m := BuildMatch(field, teamSize)
	m.applyConfig(cfg)
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
	m.applyConfig(config.Default())
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
	m.applyConfig(config.Default())
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
