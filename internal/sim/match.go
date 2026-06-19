package sim

import (
	"math/rand"

	"phootball/internal/config"
	"phootball/internal/geom"
	"phootball/internal/physics"
)

// The collision restitutions (how bouncy obstacles, the net, and the pitch/goal walls
// are) now live in config.Tuning and are read off m.Tuning at every collision site, so
// the simulation has a single source of truth and the values can be tuned per match.
// config.DefaultTuning() reproduces the original constants exactly:
//
//	ObstacleRestitution   0.50  // fixed cone obstacles
//	NetRestitution        0.20  // the goal net gives, so it catches rather than springs
//	BallWallRestitution   0.90  // the ball stays lively (absorbs ~10% per wall touch)
//	PlayerWallRestitution 0.50  // a player is damped hard, so hitting a wall costs momentum
//
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

	rng          *rand.Rand   // deterministic, seeded; used only for coin tosses
	celebrate    float64      // seconds until kickoff after a goal (play continues meanwhile)
	shootout     *Shootout    // set only while Phase is PhasePenalties
	sounds       []SoundEvent // sound events emitted this tick (drained by the client)
	kickoffArmed bool         // a staged kickoff is set up (taker on the dot) and not yet taken;
	// purely informational (the HUD/AI read it) -- it never gates physics. Cleared on the first
	// touch after a staged kickoff.

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
	possSide      Side    // team that owns the charge (SideNone = nobody)
	possProgress  float64 // 0..1 build progress (held time toward full; preserved across a pass)
	possCoast     float64 // seconds since the owning team last touched the ball (hold+decay clock)
	possBuffDrain float64 // 0..1 suppression of ONLY the owning team's buff while its carrier is
	// contested by an opponent; scales the owners' published coefficient toward 0 but leaves the
	// conceding team's debuff (OtherTeam*strength) untouched. Recovers when the pressure ends.
	possDebuffDrain float64 // 0..1 team-wide RELIEF of the CONCEDING team's debuff: scales every
	// conceding player's coefficient toward 0 (neutral, never a buff). A tug-of-war: DRAINS while a
	// defender contests the ball OR the ball is loose after a defender touched it (see
	// possContestLatched), REGENERATES while the owning team has clean control, FREEZES on a clean
	// release. Mirrors the owner's buff suppression; resets to 0 on handover/claim/reset.
	possContestLatched bool // a defender has touched/reached the ball and the owning team has not yet
	// regained clean control; while set, both the buff and debuff keep draining through a LOOSE phase
	// (e.g. a shot deflecting off a defender). Set on defenderNearBall, cleared when ownerNearBall.

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

	// rec is the opt-in match recorder (nil = recording off). Every hook is nil-safe, so a
	// match with rec == nil simulates byte-identically to one with no recorder at all. It is
	// deliberately NOT reachable through View, so a controller can never read match stats.
	rec *Recorder
}

// EnableRecording turns on the write-only stats/play-by-play recorder for this match. It is
// off by default; call this once after building the match (before stepping).
func (m *Match) EnableRecording() { m.rec = NewRecorder(m) }

// Recorder returns the match recorder, or nil if recording was never enabled.
func (m *Match) Recorder() *Recorder { return m.rec }

// Stats returns a deep, stable-ordered copy of the recorded statistics, or a zero MatchStats
// if recording is off. It never leaks the recorder's internal maps or pointers.
func (m *Match) Stats() MatchStats { return m.rec.Snapshot() }

// Celebrating reports whether a goal was just scored and the kickoff countdown is
// running. Play is not paused during it.
func (m *Match) Celebrating() bool { return m.celebrate > 0 }

// applyConfig stamps a match with its ruleset, physics tuning, and seeded RNG.
func (m *Match) applyConfig(cfg config.Config) {
	m.Rules = cfg.Ruleset
	m.Tuning = cfg.Tuning
	m.Seed = cfg.Seed
	m.rng = newRNG(cfg.Seed)
	// Make the physics tuning authoritative over the ball body: the ball is built with
	// placeholder defaults, then stamped here so a custom Tuning (radius/friction/mass)
	// actually reaches the simulation. DefaultTuning() is byte-equal to those placeholders,
	// so the default match is unchanged.
	if m.Ball != nil {
		m.Ball.Friction = cfg.Tuning.BallFriction
		if cfg.Tuning.BallMass > 0 { // guard a misconfigured/zero mass (would be a divide-by-zero -> +Inf InvMass)
			m.Ball.InvMass = 1 / cfg.Tuning.BallMass
		}
		m.Ball.SetRadius(cfg.Tuning.BallRadius)
	}
	// Make the tuning authoritative over every player the same way: stamp the configured
	// player profile (one shared profile for all roles) and re-sync the body fields it
	// derives. Idempotent -- for the default config this re-applies the values the players
	// were already built with, so the default match is unchanged.
	for _, p := range m.Players {
		p.SetTuning(cfg.Tuning.Player)
	}
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
		// Rule 2 (priority): a holder marked by an opponent that is NOT near the ball is denied --
		// its possession drains even if it is itself the latest near the ball. Otherwise the sole
		// builder (latest with the ball in reach) gains (Rule 3); everyone else decays.
		drain := p == m.possessor && m.markedByNonBallOpponent(p)
		build := !drain && p == m.possBuilder
		updatePossession(m.Ball, p, deltaTime, build, drain)
	}

	// 2.55 Track who holds the ball; on a change of hands, the taker steals a slice of the
	// dispossessed player's possession. Runs before the dribble interaction so the stolen
	// possession feeds this tick's grip.
	m.updateBallPossessor(deltaTime)

	// 2.6 Advance the team possession charge and publish each player's touch-quality
	// coefficient for this tick, so the collision resolver can read it as a per-player value.
	m.advanceTeamPossession(deltaTime)

	// 2.7 Sample per-tick possession, distance covered, and time-in-thirds for the recorder
	// (no-op when recording is off; reads positions only, never mutates them).
	m.rec.sample(m, deltaTime)

	// 3. Resolve collisions and the ball-player dribble interaction.
	m.resolveInteractions(deltaTime)

	// 4. Consume kick requests, scaling by the held charge, then clear it. A middle-click push
	// fires first (instant, min power, anywhere in the pull radius) if requested.
	for _, p := range m.Players {
		if p.wantsPush {
			// Fire the cosmetic pulse on the ATTEMPT, anchored on the player: a middle-click jab
			// animates over the player every time it is pressed, even when the ball is out of reach
			// (a whiff still shows the effect). Only a CONNECT registers a touch and kick sound.
			p.pushFlash = 1
			if push(p, m.Ball) {
				m.recordTouch(p, TouchKick)
				m.rec.onKick(m, p)
				m.emit(SoundKick, geom.Norm(m.Ball.Velocity), m.Ball.Position)
			}
			p.wantsPush = false
		}
		if p.WantsKick {
			if shoot(p, m.Ball) {
				m.recordTouch(p, TouchKick)
				m.rec.onKick(m, p)
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

	// A staged kickoff is disarmed the instant any touch is recorded -- the ball is in play.
	// Informational only; this does not gate physics.
	if m.kickoffArmed && m.LastTouch != nil {
		m.kickoffArmed = false
	}

	m.Tick++
}

// applyIntent applies one player's intent for this tick: aim, the shoot/trap charge
// update, the trap/charge/possession speed penalties, and movement. It does not
// integrate the body (step 2 does). Shared by normal play and the penalty shootout.
func (m *Match) applyIntent(p *Player, in Intent, deltaTime float64) {
	// Fade the push-press pulse animation (a cosmetic 1->0 timer set when a middle-click fires).
	if p.pushFlash > 0 {
		if p.pushFlash -= deltaTime / pushFlashSeconds; p.pushFlash < 0 {
			p.pushFlash = 0
		}
	}
	if in.Aim != (geom.Vec{}) {
		if in.AimFromCursor {
			p.faceTowardLimited(in.Aim, deltaTime) // human cursor: turn at TurnRate, no instant snap
		} else {
			p.FaceTowards(in.Aim) // AI: instant in the sim; rate-limited in the control layer (capAim)
		}
	}

	// Shoot charge: accumulate while held (capped); fire on the release edge. A cancel
	// (CancelCharge) drops the charge and latches "canceled", so the eventual shoot-release
	// does NOT fire and holding shoot after the cancel does nothing until it is released and
	// pressed afresh. Cancel is an explicit Intent signal, NOT inferred from the trap button,
	// so the AI's deliberate trap-WHILE-charging recover move (CancelCharge false) keeps its
	// shot, while a genuine takeover (a trap/push overriding the charge, or an aborted overtime
	// charge) sets CancelCharge true on purpose -- exactly the human right-click-cancel, which
	// also engages a trap in the same motion.
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

	// Middle-click jab: fire once on the RISING edge of the (held) push signal, reconstructed here
	// in the sim -- exactly like the shoot release-edge above. Detecting the edge authoritatively
	// (rather than trusting a one-frame pulse from the input layer) makes the jab idempotent when an
	// intent is re-applied across several server ticks, so the push works the same over the network
	// as it does locally. (Push is a level signal: in.Push is true for every tick the button is held.)
	p.wantsPush = in.Push && !p.pushHeldPrev
	p.pushHeldPrev = in.Push

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
	p.Body.SetRadius(p.Tuning.Radius + p.Tuning.TrapRadiusBonus*p.trapAura)
	shootCharge := NormShootCharge(p.shootCharge)
	speedMul := (1 - p.trapAura*(1-p.Tuning.TrapSpeedFactor)) * (1 - shootCharge*(1-p.Tuning.ShootSpeedFactor))
	accelMul := (1 - p.trapAura*(1-p.Tuning.TrapAccelFactor)) * (1 - shootCharge*(1-p.Tuning.ShootAccelFactor))
	// Possession penalty: a ball at the player's feet costs a little speed and accel.
	if geom.Dist(p.Position, m.Ball.Position)-p.Radius()-m.Ball.Radius() < p.Tuning.TouchRange {
		speedMul *= p.Tuning.PossessionSpeedFactor
		accelMul *= p.Tuning.PossessionAccelFactor
	}
	p.Body.MaxSpeed = p.Tuning.MaxSpeed * speedMul
	p.Move(in.Move, in.Throttle*accelMul, deltaTime)
}

// teamFor returns the team defending the given side.
func (m *Match) teamFor(side Side) *Team {
	if m.Teams[0].Side == side {
		return m.Teams[0]
	}
	return m.Teams[1]
}

// The team possession-charge timings/rates now live in config.Tuning.Possession (read off
// m.Tuning each tick, so they can be tuned per match); config.DefaultTuning() reproduces the
// original constants exactly. The charge builds to full over BuildSeconds of held ball, then
// after a release (nobody touching) HOLDS for HoldSeconds and DECAYS to zero by DecaySeconds.

// resolveInteractions runs the per-tick collisions in a fixed order: ball off the
// walls, ball-player dribble, ball off obstacles, player-player, then players off
// obstacles and walls.
func (m *Match) resolveInteractions(deltaTime float64) {
	if spd := m.Field.ConfineBall(m.Ball, m.Tuning.BallWallRestitution); spd > ballHitMinSpeed {
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
		physics.Collide(m.Ball.Body, o.Body, m.Tuning.ObstacleRestitution)
	}
	for _, g := range m.Field.Goals() {
		for _, post := range g.Posts {
			physics.Collide(m.Ball.Body, post, m.Tuning.BallWallRestitution)
		}
		for _, seg := range g.Net {
			physics.Collide(m.Ball.Body, seg, m.Tuning.NetRestitution)
		}
	}

	// Re-confine the ball after the dribble/contact and obstacle/goal collisions. The ConfineBall
	// at the top ran BEFORE those, so a player dribbling the ball into a wall or corner (its
	// centre-pull dragging the ball toward the player, which is itself momentarily past the wall
	// until ConfinePlayer clamps it below) would otherwise leave the ball penetrating the wall.
	// This pushes it back inside each tick so it cannot be wedged into the corner. No sound here --
	// a genuine high-speed wall impact is caught by the confine at the top of the next tick.
	m.Field.ConfineBall(m.Ball, m.Tuning.BallWallRestitution)

	// Player-vs-player physics. The team-charge drain from an opponent contesting the ball is no
	// longer detected here -- it is handled in advanceTeamPossession (Rules 1 & 4: a marked
	// ball-carrier or an opponent reaching the ball drains the charge over time).
	for i := 0; i < len(m.Players); i++ {
		for j := i + 1; j < len(m.Players); j++ {
			physics.Resolve(m.Players[i].Body, m.Players[j].Body)
		}
	}
	for _, p := range m.Players {
		for _, o := range m.Field.Obstacles {
			physics.Collide(p.Body, o.Body, m.Tuning.PlayerWallRestitution)
		}
		for _, g := range m.Field.Goals() {
			for _, post := range g.Posts {
				physics.Collide(p.Body, post, m.Tuning.PlayerWallRestitution)
			}
			for _, seg := range g.Net {
				physics.Collide(p.Body, seg, m.Tuning.PlayerWallRestitution)
			}
		}
		m.Field.ConfinePlayer(p, m.Tuning.PlayerWallRestitution)

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
