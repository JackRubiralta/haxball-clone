package sim

import (
	"image/color"
	"math"
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
	Seed   int64          // the match RNG seed (also lets deterministic AIs vary run-to-run)
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

	// Team possession charge: a 0..1 strength owned by whichever team is holding the ball.
	// It BUILDS while the owning team touches the ball (to full over teamBuildSeconds, on an
	// accelerating curve), is PRESERVED across a pass so a receiving teammate inherits and
	// continues the build, and after a release with nobody touching it HOLDS for
	// teamHoldSeconds then DECAYS to zero by teamDecaySeconds. The other team touching the
	// ball hands ownership over and resets the build. The strength drives every player's
	// touch-quality coefficient (see advanceTeamPossession / TouchQuality).
	possSide     Side    // team that owns the charge (SideNone = nobody)
	possProgress float64 // 0..1 build progress (held time toward full; preserved across a pass)
	possCoast    float64 // seconds since the owning team last touched the ball (hold+decay clock)

	// possessor is the player currently recognised as holding the ball (nil = nobody yet). It
	// stays the holder while they remain in contact -- so when the ball changes hands, the new
	// holder STEALS a fraction of the old holder's player possession (see updateBallPossessor).
	possessor *Player

	// possBuilder is the single player allowed to build player-possession this tick: of the
	// players that currently have the ball within their pull radius, the one whose radius the
	// ball entered MOST RECENTLY (see advancePossessionBuilder). pullSeq is the monotonic stamp
	// source that orders those entries.
	possBuilder *Player
	pullSeq     uint64
}

// Celebrating reports whether a goal was just scored and the kickoff countdown is
// running. Play is not paused during it.
func (m *Match) Celebrating() bool { return m.celebrate > 0 }

// applyConfig stamps a match with its ruleset, physics tuning, and seeded RNG.
func (m *Match) applyConfig(cfg config.Config) {
	m.Rules = cfg.Ruleset
	m.Tuning = cfg.Tuning
	m.Seed = cfg.Seed
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

	// 2.5 Update each player's possession from this frame's geometry. Only the player whose pull
	// radius the ball entered most recently builds possession (advancePossessionBuilder); when
	// two players share the ball, the latest to reach it takes over the build and the rest decay.
	m.advancePossessionBuilder()
	for _, p := range m.Players {
		updatePossession(m.Ball, p, deltaTime, p == m.possBuilder)
	}

	// 2.55 Track who holds the ball; on a change of hands, the taker steals a slice of the
	// dispossessed player's possession. Runs before the dribble interaction so the stolen
	// possession feeds this tick's grip.
	m.updateBallPossessor(deltaTime)

	// 2.6 Advance the team possession charge and publish each player's touch-quality
	// coefficient for this tick, so the collision resolver can read it as a per-player value.
	m.advanceTeamPossession(deltaTime)

	// 3. Resolve collisions and the ball-player dribble interaction.
	m.resolveInteractions(deltaTime)

	// 4. Consume kick requests, scaling by the held charge, then clear it. A middle-click poke
	// fires first (instant, min power, anywhere in the pull radius) if requested.
	for _, p := range m.Players {
		if p.wantsPoke {
			p.pokeFlash = 1 // kick off the poke-press pulse animation (plays on the press itself)
			if poke(p, m.Ball) {
				m.recordTouch(p, TouchKick)
				m.emit(SoundKick, geom.Norm(m.Ball.Velocity), m.Ball.Position)
			}
			p.wantsPoke = false
		}
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
	// Fade the poke-press pulse animation (a cosmetic 1->0 timer set when a middle-click fires).
	if p.pokeFlash > 0 {
		if p.pokeFlash -= deltaTime / pokeFlashSeconds; p.pokeFlash < 0 {
			p.pokeFlash = 0
		}
	}
	if in.Aim != (geom.Vec{}) {
		if in.AimFromCursor {
			p.faceTowardLimited(in.Aim, deltaTime) // human cursor: turn at TurnRate, no instant snap
		} else {
			p.FaceTowards(in.Aim) // AI (capped in the control layer) / network: instant
		}
	}

	// Shoot charge: accumulate while held (capped); fire on the release edge. A cancel
	// (right-click while charging) drops the charge and latches "canceled", so the eventual
	// shoot-release does NOT fire and holding shoot after the cancel does nothing until it
	// is released and pressed afresh. Cancel is an explicit Intent signal, NOT inferred from
	// the trap button, so the AI's trap-while-charging recover move never self-cancels (the
	// AI leaves CancelCharge false); the same right-click still engages a trap below, so a
	// human who aborts a mistaken shot settles the ball in the same motion.
	if in.ShootHeld {
		if in.CancelCharge && p.shootCharge > 0 {
			p.shootCharge = 0
			p.shootCanceled = true
		}
		if !p.shootCanceled {
			p.shootCharge += deltaTime
			if p.shootCharge > shootChargeMax {
				p.shootCharge = shootChargeMax
			}
		}
	} else {
		if p.shootHeldPrev && !p.shootCanceled {
			p.WantsKick = true
		}
		p.shootCanceled = false
	}
	p.shootHeldPrev = in.ShootHeld

	// Middle-click jab: latch the instant poke request for the kick phase (it fires once on the
	// press edge -- the human sets Poke only on the rising edge of middle-click).
	p.wantsPoke = in.Poke

	// Trap charge: build toward 1 while held, decay otherwise. (No sound on the trap/right-click
	// rising edge -- the trap is silent by request.)
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

	// Effective trap strength (stateful): WHILE HELD it follows a hump of the charge -- swelling
	// to full at the peak charge, then weakening as the trap is over-held; on RELEASE it shrinks
	// straight to nothing and never re-swells (it is not recomputed from the decaying charge).
	// This single level drives BOTH the trap effect (capture/pull/reach/slowdown/...) and the
	// visual aura, so the on-screen glow always matches what the trap is actually doing.
	if in.Trap {
		p.trapAura = trapAuraShape(p.trapCharge)
	} else if p.trapAura > 0 {
		if p.trapAura -= deltaTime / trapAuraReleaseSeconds; p.trapAura < 0 {
			p.trapAura = 0
		}
	}

	// Trapping and charging a shot both slow the player; charging slows it more. The trap term
	// uses the humped trapAura (so the slowdown swells then eases as the trap is over-held, like
	// the rest of its effect). Set unconditionally from the base so nothing drifts.
	p.Body.SetRadius(p.Stats.Radius + p.Stats.TrapRadiusBonus*p.trapAura)
	shootCharge := NormShootCharge(p.shootCharge)
	speedMul := (1 - p.trapAura*(1-p.Stats.TrapSpeedFactor)) * (1 - shootCharge*(1-p.Stats.ShootSpeedFactor))
	accelMul := (1 - p.trapAura*(1-p.Stats.TrapAccelFactor)) * (1 - shootCharge*(1-p.Stats.ShootAccelFactor))
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

// Team possession-charge timing. The charge builds to full over teamBuildSeconds of held
// ball, then after a release (nobody touching) HOLDS at its built strength for
// teamHoldSeconds and DECAYS to zero by teamDecaySeconds.
const (
	teamBuildSeconds   = 1.5 // seconds of team possession to build the charge to full
	teamHoldSeconds    = 1.5 // seconds the charge holds at full strength after release (no touch)
	teamDecaySeconds   = 3.5 // seconds until the charge has fully decayed after release (no touch)
	teamBuildExponent  = 3.0 // build-curve exponent: higher = stays low for most of the build, spiking near the end
	teamDrainPerSecond = 1.0 // build-progress drained per second while the carrier is challenged by an opponent

	// Per-player boost drain: while an opponent is body-touching a player on the BOOSTED team,
	// that ONE player's share of the team boost erodes (its published touch coefficient is scaled
	// toward 0) -- the team charge and its team-mates are untouched. It recovers once the contact
	// ends. So an opponent marking you body-to-body drains YOUR clean touch even off the ball.
	boostContactDrainPerSecond   = 2.0 // fraction of a player's own boost drained per second while an opponent is touching it
	boostContactRecoverPerSecond = 1.5 // fraction recovered per second once no opponent is touching it
)

// teamBuildCurve maps build progress (0..1, the held-time fraction) to charge strength on a
// strongly ACCELERATING ramp -- it stays LOW for the majority of the build and shoots up only
// in the last portion (the user's "lower for most of the start, increases a lot at the end").
// strength = progress^teamBuildExponent (cubic), so full strength is reached only near the end
// of the (deliberately long) build window.
func teamBuildCurve(progress float64) float64 {
	return math.Pow(clampUnit(progress), teamBuildExponent)
}

// teamBuildCurveInv inverts teamBuildCurve: the build progress that yields a given charge
// strength. Used when a teammate receives a (possibly decayed) charge -- the decayed strength
// is baked back into the progress so the build resumes from that point, not from full.
func teamBuildCurveInv(strength float64) float64 {
	return math.Pow(clampUnit(strength), 1.0/teamBuildExponent)
}

// teamCoastEnvelope maps seconds-since-last-touch to a 0..1 fade applied after a release: full
// (1) through teamHoldSeconds, then a smooth CONVEX decay to 0 by teamDecaySeconds -- a gentle
// fall at first that speeds up toward the end (the user's "slow rate that speeds up at the
// end"). 1 - x^2 over the decay window: flat where x=0, steepening to its fastest at x=1. The
// window is long so a released charge lingers and fades slowly.
func teamCoastEnvelope(coast float64) float64 {
	switch {
	case coast <= teamHoldSeconds:
		return 1
	case coast >= teamDecaySeconds:
		return 0
	default:
		x := (coast - teamHoldSeconds) / (teamDecaySeconds - teamHoldSeconds) // 0..1 across the decay
		return 1 - x*x
	}
}

// teamPossessionStrength returns the current 0..1 strength of side's possession charge: the
// build curve over the progress, faded by the coast (hold+decay) envelope. It is 0 for any
// side that does not currently own the charge.
func (m *Match) teamPossessionStrength(side Side) float64 {
	if side == SideNone || side != m.possSide {
		return 0
	}
	return teamBuildCurve(m.possProgress) * teamCoastEnvelope(m.possCoast)
}

// PossessionCharge returns side's current possession-charge strength (0..1), for the HUD /
// test bars. It is 0 unless side currently owns the charge.
func (m *Match) PossessionCharge(side Side) float64 { return m.teamPossessionStrength(side) }

// resetTeamPossession drops the charge entirely (contested ball, expiry, kickoff, shootout):
// no owner, no progress, and every player's published coefficient zeroed at once so the
// reset takes effect within the same collision loop.
func (m *Match) resetTeamPossession() {
	m.possSide = SideNone
	m.possProgress = 0
	m.possCoast = 0
	for _, p := range m.Players {
		p.touchCoef = 0
	}
}

// drainTeamPossession bleeds the team possession charge DOWN over time (rather than zeroing
// it), used while the ball carrier is being challenged by an opponent -- either a body-to-body
// challenge on the holder OR an opponent reaching the ball within its pull radius while the
// owning team still has it (a ranged contest). The build progress drops at teamDrainPerSecond,
// so sustained pressure wears the boost away while a glancing bump only nicks it. The owner
// keeps the ball, rebuilding from whatever is left.
func (m *Match) drainTeamPossession(deltaTime float64) {
	if m.possSide == SideNone {
		return
	}
	if m.possProgress -= teamDrainPerSecond * deltaTime; m.possProgress < 0 {
		m.possProgress = 0
	}
}

// touching reports whether the player is currently in contact with the ball (the same touch
// test as the possession slowdown and possession build-up).
func (m *Match) touching(p *Player) bool {
	return geom.Dist(p.Position, m.Ball.Position)-p.Radius()-m.Ball.Radius() < p.Stats.TouchRange
}

// inPullRange reports whether the ball is within the player's (trap-extended) pull radius --
// close enough to act on the ball without touching it. Used so a player can contest/steal
// possession from arm's length (a held trap reaches further), not only in direct contact.
func (m *Match) inPullRange(p *Player) bool {
	return geom.Dist(p.Position, m.Ball.Position)-p.Radius()-m.Ball.Radius() < p.pullRadius()
}

// ballInTeamPullRange reports whether any player on the given side has the ball within its pull
// radius (the trap-extended reach). Used to detect a pull-range possession contest: the ball is
// in BOTH the owning team's and an opponent's pull radius.
func (m *Match) ballInTeamPullRange(side Side) bool {
	for _, p := range m.Players {
		if p.Team.Side == side && m.inPullRange(p) {
			return true
		}
	}
	return false
}

// playersTouching reports whether two players' bodies overlap (a body-to-body bump).
func playersTouching(a, b *Player) bool {
	return geom.Dist(a.Position, b.Position) < a.Radius()+b.Radius()
}

// opponentTouching reports whether any player on the OTHER team is body-touching p -- used to
// drain that one player's team-possession boost while it is being marked/contacted.
func (m *Match) opponentTouching(p *Player) bool {
	for _, o := range m.Players {
		if o.Team.Side != p.Team.Side && playersTouching(p, o) {
			return true
		}
	}
	return false
}

// engaged reports whether a player is contesting the ball this tick: either the ball is within
// its (trap-extended) pull radius, OR it is bumping the current holder body-to-body -- so a
// player-to-player challenge counts even when the ball itself is not quite in reach. The latest
// player to become engaged is the sole possession builder (advancePossessionBuilder) and the
// player a held ball drains into (updateBallPossessor).
func (m *Match) engaged(p *Player) bool {
	if m.inPullRange(p) {
		return true
	}
	h := m.possessor
	return h != nil && h != p && playersTouching(p, h)
}

// advancePossessionBuilder selects the single player allowed to build player-possession this
// tick: of the players that currently have the ball within their pull radius, the one whose
// radius the ball entered MOST RECENTLY. Each player is stamped with an increasing sequence the
// tick the ball first enters its pull radius (cleared when the ball leaves); the builder is the
// in-range player with the latest stamp. So when the ball comes into a second player's reach
// (e.g. an opponent closing down a carrier), that newcomer becomes the sole builder and the
// earlier player stops building -- only one player's possession rises at a time. If the latest
// builder loses the ball, the build falls back to whoever still has it in reach.
func (m *Match) advancePossessionBuilder() {
	var builder *Player
	for _, p := range m.Players {
		if !m.engaged(p) {
			p.pullEnterSeq = 0 // out of reach: forget the entry so a re-engagement counts as newest
			continue
		}
		if p.pullEnterSeq == 0 { // rising edge: just became engaged (ball in reach, or bumping the holder)
			m.pullSeq++
			p.pullEnterSeq = m.pullSeq
		}
		if builder == nil || p.pullEnterSeq > builder.pullEnterSeq {
			builder = p
		}
	}
	m.possBuilder = builder
}

// touchingSides reports which teams have at least one player overlapping the ball this tick.
// Used to drive the team charge.
func (m *Match) touchingSides() (left, right bool) {
	for _, p := range m.Players {
		if m.touching(p) {
			switch p.Team.Side {
			case SideLeft:
				left = true
			case SideRight:
				right = true
			}
		}
	}
	return left, right
}

// updateBallPossessor tracks who holds the ball and resolves a CONTEST for it. The single
// possession BUILDER (the latest player to engage -- ball in its pull radius, or bumping the
// holder; see advancePossessionBuilder) is the player gaining possession. While the current
// holder is a DIFFERENT player still engaged, possession drains GRADUALLY from the holder INTO
// that builder -- the displaced player loses exactly what the builder gains (contestPossession),
// so possession always flows TO the latest engager and never leaks back out of it (the earlier
// bug: it bled back into the displaced player, whose fast decay then zeroed both). The builder
// takes the ball once it holds the larger share. A LOOSE ball (the holder no longer engaged) is
// claimed only on an actual touch, so a ball merely flying past a player is not taken over
// (protects passes); a passed ball carries possession 0 (shoot resets it), so reception starts cold.
func (m *Match) updateBallPossessor(deltaTime float64) {
	holder := m.possessor
	builder := m.possBuilder // the latest engager: the player gaining possession this tick

	switch {
	case holder != nil && m.engaged(holder) && builder != nil && builder != holder:
		// A different player than the holder is the latest engager (ball in its pull radius, or
		// bumping the holder). Drain the holder INTO that builder; the builder takes the ball
		// once it leads. Possession flows to the builder and never leaks back out.
		m.contestPossession(holder, builder, deltaTime)
		if builder.possession > holder.possession {
			m.possessor = builder
		}
	case holder != nil && m.engaged(holder):
		// The holder is itself the latest engager (or alone in reach): it keeps the ball, no
		// transfer -- so an established builder holds without bleeding possession away.
	default:
		// The holder no longer has the ball in reach. Only an ACTUAL touch claims the loose ball
		// (a ball merely within a player's pull radius -- a pass in flight, or flying past -- does
		// not flip possession; protects passes). Nearest toucher wins; roster order breaks ties.
		var toucher *Player
		bestT := 0.0
		for _, p := range m.Players {
			if p == holder {
				continue
			}
			if d := geom.Dist(p.Position, m.Ball.Position); m.touching(p) && (toucher == nil || d < bestT) {
				toucher, bestT = p, d
			}
		}
		if toucher != nil {
			m.possessor = toucher
		}
	}
}

// contestPossession moves player possession from the holder to a challenger while both are on
// the ball: per tick the challenger GAINS and the holder LOSES PossessionStealRate*dt, so a
// sustained challenge wins the ball gradually rather than snatching it. Capped so nothing is
// created (challenger to full) or destroyed (holder to empty).
func (m *Match) contestPossession(holder, taker *Player, deltaTime float64) {
	xfer := taker.Stats.PossessionStealRate * deltaTime
	if xfer > holder.possession {
		xfer = holder.possession
	}
	if room := 1 - taker.possession; xfer > room {
		xfer = room
	}
	if xfer <= 0 {
		return
	}
	holder.possession -= xfer
	taker.possession += xfer
}

// advanceTeamPossession runs one tick of the team possession charge. It first PUBLISHES every
// player's touch-quality coefficient from the CURRENT charge -- before this tick's contact
// can change ownership -- so a contact (resolved in the next phase) sees possession as it
// stood when the ball arrived: a shot blocked by the conceding team flies off them, while a
// pass to a teammate lands cleanly. It then UPDATES the charge from who is touching the ball:
// the owning team builds (and resets its coast), a different team takes over with a fresh
// build, both teams at once is contested (reset), and nobody touching coasts (hold then decay).
func (m *Match) advanceTeamPossession(deltaTime float64) {
	// 1. Publish coefficients from the current (pre-contact) charge.
	strength := m.teamPossessionStrength(m.possSide)
	for _, p := range m.Players {
		boosted := m.possSide != SideNone && p.Team.Side == m.possSide
		switch {
		case m.possSide == SideNone:
			p.touchCoef = 0
		case boosted:
			p.touchCoef = p.Stats.TouchQuality.OwnTeamMax * strength
		default:
			p.touchCoef = p.Stats.TouchQuality.OtherTeam * strength
		}

		// Per-player boost drain: while an opponent is body-touching a player on the BOOSTED
		// team, that ONE player's boost erodes; it recovers once the contact ends. Only the
		// boosted team's own buff drains (a conceding player has no boost to lose), and only
		// that player's published coefficient is scaled down -- the team charge and team-mates
		// are untouched. So an opponent marking you erodes YOUR clean touch even off the ball.
		if boosted && m.opponentTouching(p) {
			p.boostDrain = clampUnit(p.boostDrain + boostContactDrainPerSecond*deltaTime)
		} else {
			p.boostDrain = clampUnit(p.boostDrain - boostContactRecoverPerSecond*deltaTime)
		}
		if boosted {
			p.touchCoef *= 1 - p.boostDrain
		}
	}

	// 2. Update the charge from this tick's contact.
	left, right := m.touchingSides()
	switch {
	case left && right:
		m.resetTeamPossession() // contested: nobody has a clean possession
	case left || right:
		toucher := SideLeft
		if right {
			toucher = SideRight
		}
		if m.possSide == toucher {
			// Same team (re)touches. If the ball was in flight (coast > 0), the charge may have
			// decayed: bake that decayed strength back into the progress, so a receiving teammate
			// inherits the CURRENT (decayed) coefficient and rebuilds from there -- not from full.
			if m.possCoast > 0 {
				m.possProgress = teamBuildCurveInv(teamBuildCurve(m.possProgress) * teamCoastEnvelope(m.possCoast))
			}
			m.possProgress = clampUnit(m.possProgress + deltaTime/teamBuildSeconds)
		} else {
			m.possSide = toucher // a new team won the ball: fresh build from zero
			m.possProgress = clampUnit(deltaTime / teamBuildSeconds)
		}
		m.possCoast = 0
	default:
		if m.possSide != SideNone { // ball in flight: hold then decay; progress preserved until a touch bakes in the decay
			m.possCoast += deltaTime
			if m.possCoast >= teamDecaySeconds {
				m.resetTeamPossession()
			}
		}
	}
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

	// Re-confine the ball after the dribble/contact and obstacle/goal collisions. The ConfineBall
	// at the top ran BEFORE those, so a player dribbling the ball into a wall or corner (its
	// centre-pull dragging the ball toward the player, which is itself momentarily past the wall
	// until ConfinePlayer clamps it below) would otherwise leave the ball penetrating the wall.
	// This pushes it back inside each tick so it cannot be wedged into the corner. No sound here --
	// a genuine high-speed wall impact is caught by the confine at the top of the next tick.
	m.Field.ConfineBall(m.Ball)

	challenged := false
	for i := 0; i < len(m.Players); i++ {
		for j := i + 1; j < len(m.Players); j++ {
			pi, pj := m.Players[i], m.Players[j]
			// A collision between OPPOSING players where one of them is IN CONTACT WITH THE BALL
			// is a challenge on the ball: it DRAINS the team possession charge (rather than
			// zeroing it), so sustained pressure wears the boost away while a glancing bump only
			// nicks it. Detected before Resolve pushes them apart; an off-ball bump does nothing.
			if pi.Team.Side != pj.Team.Side &&
				geom.Dist(pi.Position, pj.Position) < pi.Radius()+pj.Radius() &&
				(m.touching(pi) || m.touching(pj)) {
				challenged = true
			}
			physics.Resolve(pi.Body, pj.Body)
		}
	}
	// A RANGED challenge also drains the charge: while the owning team still has the ball within
	// an owning player's pull radius, an opponent that now has it within THEIR pull radius too is
	// contesting the possession at arm's length -- no body collision needed.
	if !challenged && m.possSide != SideNone &&
		m.ballInTeamPullRange(m.possSide) && m.ballInTeamPullRange(m.possSide.Opponent()) {
		challenged = true
	}
	if challenged {
		m.drainTeamPossession(deltaTime)
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

		// A ball confined against a wall or corner can't be shoved aside, so once the player is
		// also clamped against that boundary their centres can't reach radius-sum apart and the
		// player would sit ON TOP OF the ball. Separate them by moving the PLAYER out of the
		// overlap (never the ball -- it must stay inside the arena), pushed away from the ball
		// toward open space, so a player can never overlap a pinned ball.
		sep := p.Position.Sub(m.Ball.Position)
		if d := geom.Norm(sep); d > 0 {
			if push := p.Radius() + m.Ball.Radius() - d; push > 0 {
				p.Position = p.Position.Add(sep.Scale(push / d))
			}
		}
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
	m.resetTeamPossession()
	m.possessor = nil
	m.Ball.Position = m.Field.CenterSpot
	m.Ball.Velocity = geom.NewVec(0, 0)
	m.Ball.Acceleration = geom.NewVec(0, 0)
	for _, p := range m.Players {
		p.Position = p.HomePosition
		p.Velocity = geom.NewVec(0, 0)
		p.Acceleration = geom.NewVec(0, 0)
		p.moveHeading = geom.Vec{}
		p.possession = 0
		p.control = 0
		p.shootCharge = 0
		p.trapCharge = 0
		p.trapAura = 0
		p.shootHeldPrev = false
		p.shootCanceled = false
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

// BallCarrier returns the player currently in firm possession of the ball, or nil.
// It exposes the same logic the positional rules use, so an AI can reason about who
// controls the ball without recomputing it.
func (m *Match) BallCarrier() *Player { return m.ballCarrier() }

// KickoffSide returns the team that takes the next kickoff: the side that conceded the
// most recent goal, or SideLeft at the start of a match (before any goal).
func (m *Match) KickoffSide() Side {
	if m.LastGoal != nil {
		return m.LastGoal.Team.Opponent()
	}
	return SideLeft
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
