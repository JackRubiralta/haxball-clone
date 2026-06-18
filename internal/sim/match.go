package sim

import (
	"image/color"
	"math"
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
			p.pushFlashPos = p.Position
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

	// Middle-click jab: latch the instant push request for the kick phase (it fires once on the
	// press edge -- the human sets Push only on the rising edge of middle-click).
	p.wantsPush = in.Push

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

// Team possession-charge timing. The charge builds to full over teamBuildSeconds of held
// ball, then after a release (nobody touching) HOLDS at its built strength for
// teamHoldSeconds and DECAYS to zero by teamDecaySeconds.
const (
	teamBuildSeconds   = 1.5 // seconds of team possession to build the charge to full
	teamHoldSeconds    = 1.5 // seconds the charge holds at full strength after release (no touch)
	teamDecaySeconds   = 3.5 // seconds until the charge has fully decayed after release (no touch)
	teamBuildExponent  = 3.0 // build-curve exponent: higher = stays low for most of the build, spiking near the end
	teamDrainPerSecond = 1.0 // owner buff-suppression (possBuffDrain) gained per second while the carrier is contested (and recovered per second otherwise); the conceding team's debuff is never drained

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
	m.possBuffDrain = 0
	m.possDebuffDrain = 0
	m.possContestLatched = false
	for _, p := range m.Players {
		p.touchCoef = 0
	}
}

// touching reports whether the player is currently in contact with the ball (the same touch
// test as the possession slowdown and possession build-up).
func (m *Match) touching(p *Player) bool {
	return geom.Dist(p.Position, m.Ball.Position)-p.Radius()-m.Ball.Radius() < p.Tuning.TouchRange
}

// inPullRange reports whether the ball is within the player's POSSESSION radius -- close enough
// to act on the ball without touching it. It uses possessionReach() (the PossessionRange knob,
// defaulting to base PullRange), NOT the trap-extended pullRadius(): a held trap extends the ball
// ATTRACTION (the centre-pull in handleBallToPlayerInteraction) but it must NOT extend possession
// reach, so trapping never widens who builds or contests possession. This is the single reach test
// behind both the player-possession builder (engaged/advancePossessionBuilder) and the
// team-possession contest (ballInTeamPullRange/advanceTeamPossession).
func (m *Match) inPullRange(p *Player) bool {
	return geom.Dist(p.Position, m.Ball.Position)-p.Radius()-m.Ball.Radius() < p.possessionReach()
}

// ballInTeamPullRange reports whether any player on the given side has the ball within its BASE
// pull radius (inPullRange; NOT trap-extended). Used to detect a pull-range possession contest:
// the ball is in BOTH the owning team's and an opponent's pull radius.
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

// playerReach reports whether challenger c has target t within c's POSSESSION radius -- a
// player-to-player reach (body contact is included, since the surface gap is then negative). It
// uses possessionReach() (PossessionRange, defaulting to base PullRange), NOT the trap-extended
// pullRadius(), so trapping does not widen the marking/pressure that drives the possession contest
// (pressuredByOpponent / markedByNonBallOpponent).
func playerReach(c, t *Player) bool {
	return geom.Dist(c.Position, t.Position)-c.Radius()-t.Radius() < c.possessionReach()
}

// pressuredByOpponent reports whether any opponent has p within its pull reach (an opponent is
// marking/closing on p). Drives the team-charge and per-player boost drains (an opponent in reach
// of a boosted player erodes its boost).
func (m *Match) pressuredByOpponent(p *Player) bool {
	for _, o := range m.Players {
		if o.Team.Side != p.Team.Side && playerReach(o, p) {
			return true
		}
	}
	return false
}

// markedByNonBallOpponent reports whether an opponent has p within its reach WITHOUT itself
// having the ball in reach -- a pure marker contesting the player, not the ball. Such marking
// DRAINS (denies) p's player-possession even though that marker can't take the ball for itself; a
// ball-near opponent instead steals via out-building (see updatePossession / updateBallPossessor).
func (m *Match) markedByNonBallOpponent(p *Player) bool {
	for _, o := range m.Players {
		if o.Team.Side != p.Team.Side && playerReach(o, p) && !m.inPullRange(o) {
			return true
		}
	}
	return false
}

// hasBall reports whether p is the recognised ball holder or in direct contact with the ball.
func (m *Match) hasBall(p *Player) bool {
	return m.possessor == p || m.touching(p)
}

// engaged reports whether a player has the BALL within its BASE pull reach this tick (inPullRange;
// NOT trap-extended -- trapping does not widen who can build possession).
// The latest player to become engaged is the sole possession BUILDER (advancePossessionBuilder)
// -- so a player only builds/gains possession while it can actually reach the ball (Rule 3),
// never merely by marking an opponent. Denial-by-marking (draining a holder who has nobody near
// the ball) is handled separately via markedByNonBallOpponent in the possession update.
func (m *Match) engaged(p *Player) bool {
	return m.inPullRange(p)
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

// updateBallPossessor tracks WHO is recognised as the ball holder. The possession values
// themselves are built and drained in updatePossession (the sole builder -- the latest player to
// reach the ball -- gains; a holder marked by an opponent off the ball is denied). Here we only
// move the holder flag: while the holder still has the ball in reach, a DIFFERENT builder takes
// over once it has out-built the holder; a LOOSE ball (the holder no longer has it in reach) is
// claimed only on an ACTUAL touch -- a ball merely within pull range (a pass in flight, or flying
// past) does not flip possession (protects passes); a passed ball carries possession 0 (shoot
// resets it), so reception starts cold. deltaTime is unused (possession changes happen earlier).
func (m *Match) updateBallPossessor(deltaTime float64) {
	holder := m.possessor
	builder := m.possBuilder

	switch {
	case holder != nil && m.engaged(holder):
		// The holder still has the ball in reach. If a different builder has out-built it (it
		// gained while the holder was denied or fell away), hand the ball over to that builder.
		if builder != nil && builder != holder && builder.possession > holder.possession {
			m.possessor = builder
		}
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

// advanceTeamPossession runs one tick of the team possession charge. It first PUBLISHES every
// player's touch-quality coefficient from the CURRENT charge -- before this tick's contact
// can change ownership -- so a contact (resolved in the next phase) sees possession as it
// stood when the ball arrived: a shot blocked by the conceding team flies off them, while a
// pass to a teammate lands cleanly. It then UPDATES the charge from who is touching the ball:
// the owning team builds (and resets its coast), a different team takes over with a fresh
// build, both teams at once is contested (reset), and nobody touching coasts (hold then decay).
func (m *Match) advanceTeamPossession(deltaTime float64) {
	// Who has the ball within reach (touching, or in a player's trap-extended pull radius)? These
	// gate both the publish-step boost drain/recovery and the charge update below, so compute up front.
	left, right := m.touchingSides()
	ownerTouches := (m.possSide == SideLeft && left) || (m.possSide == SideRight && right)
	ownerNearBall := m.possSide != SideNone && (ownerTouches || m.ballInTeamPullRange(m.possSide))
	defenderNearBall := m.possSide != SideNone && m.ballInTeamPullRange(m.possSide.Opponent())

	// 1. Publish coefficients from the current (pre-contact) charge.
	strength := m.teamPossessionStrength(m.possSide)
	for _, p := range m.Players {
		boosted := m.possSide != SideNone && p.Team.Side == m.possSide
		switch {
		case m.possSide == SideNone:
			p.touchCoef = 0
		case boosted:
			p.touchCoef = p.Tuning.TouchQuality.OwnTeamMax * strength
		default:
			// Conceding team: the raw debuff, lifted team-wide by possDebuffDrain (relief while this
			// team has the ball -- scaled toward neutral 0, never into a buff). The buff-drains above
			// never touch this branch, and this relief never touches the owner's buff.
			p.touchCoef = p.Tuning.TouchQuality.OtherTeam * strength * (1 - m.possDebuffDrain)
		}

		// Per-player boost drain (Rule 1, off-ball case): while a boosted player does NOT have the ball
		// in its own reach -- an opponent is marking it, or it has released/passed the ball -- that ONE
		// player's boost erodes. This keys off the ball ACTUALLY being in this player's pull reach
		// (inPullRange), NOT the stale possessor flag: a player who has PASSED is no longer a carrier,
		// so marking them drains only their own boost, never the whole team charge (that is reserved
		// for marking the real carrier, in the update step below). The boost only RECOVERS while the
		// owning team still has the ball within reach (ownerNearBall) -- once the ball is released and
		// loose/in-flight, a drained boost is FROZEN until a team-mate regains the ball.
		if boosted && !m.inPullRange(p) && m.pressuredByOpponent(p) {
			p.boostDrain = clampUnit(p.boostDrain + boostContactDrainPerSecond*deltaTime)
		} else if ownerNearBall {
			p.boostDrain = clampUnit(p.boostDrain - boostContactRecoverPerSecond*deltaTime)
		}
		if boosted {
			// Scale the owner's BUFF by both the team-wide contest suppression (possBuffDrain, when
			// the carrier is being challenged) and this player's own off-ball mark (boostDrain). The
			// conceding team's coefficient above is the raw OtherTeam*strength -- its DEBUFF is never
			// scaled by either drain, so pressuring the carrier erodes the attacker's buff without
			// relieving the defender's own debuff.
			p.touchCoef *= (1 - m.possBuffDrain) * (1 - p.boostDrain)
		}
	}

	// 2. Update the charge from this tick's contact and any opponent contest (reach computed above).
	// The owner's BUFF is contested when the defender has the ball within reach (Rule 4 -- includes
	// touching), OR an opponent is marking a boosted ball-CARRIER within reach (Rule 1, has-ball --
	// keyed on the ball being in that player's pull reach, NOT the stale possessor, so a player who
	// has already passed does not count as the carrier and only its own boost drains).
	contested := defenderNearBall
	if !contested && m.possSide != SideNone {
		for _, p := range m.Players {
			if p.Team.Side == m.possSide && m.inPullRange(p) && m.pressuredByOpponent(p) {
				contested = true
				break
			}
		}
	}

	// Contest latch: a defender touching/reaching the ball sets it; the owning team regaining clean
	// control clears it. While latched, both the buff (here) and the debuff (below) KEEP draining
	// through a LOOSE phase after the touch -- e.g. a shot that deflects off a defender keeps eroding
	// both even as it flies away -- so a contest's effect persists past the instant of contact.
	if defenderNearBall {
		m.possContestLatched = true
	} else if ownerNearBall {
		m.possContestLatched = false
	}

	// Buff suppression: the OWNING team's buff DRAINS while the carrier is contested OR the ball is
	// loose after a defender touched it (latched). It only RECOVERS while the owning team has clean
	// control (ownerNearBall, no contest) -- so a carrier marked then RELEASING the ball stays faded
	// while it is loose and only heals once a team-mate regains it -- and FREEZES on a clean release
	// the defender never touched. This scales only the owner's published buff, never the debuff.
	if contested || (m.possContestLatched && !ownerNearBall) {
		m.possBuffDrain = clampUnit(m.possBuffDrain + teamDrainPerSecond*deltaTime)
	} else if ownerNearBall {
		m.possBuffDrain = clampUnit(m.possBuffDrain - teamDrainPerSecond*deltaTime)
	}

	// Debuff relief mirrors the buff: it DRAINS while a defender contests the ball OR the ball is
	// loose after a defender touched it (latched) -- so a deflection off a defender keeps relieving
	// the whole conceding team's debuff even as the ball flies away. It REGENERATES (climbs back
	// toward full) only while the owning team has clean control (ownerNearBall, no defender), and
	// FREEZES on a clean release the defender never touched -- it never increases while the defending
	// team is contesting or chasing a loose touched ball. Ownership hands over once it has fully
	// drained with a defender alone on the ball (below).
	if defenderNearBall || (m.possContestLatched && !ownerNearBall) {
		m.possDebuffDrain = clampUnit(m.possDebuffDrain + teamDrainPerSecond*deltaTime)
	} else if ownerNearBall {
		m.possDebuffDrain = clampUnit(m.possDebuffDrain - teamDrainPerSecond*deltaTime)
	}

	switch {
	case m.possSide == SideNone:
		// Nobody owns the charge: a single team touching the ball claims it and starts building;
		// both teams at once stays unowned (a neutral scramble).
		if (left || right) && !(left && right) {
			toucher := SideLeft
			if right {
				toucher = SideRight
			}
			m.possSide = toucher
			m.possProgress = clampUnit(deltaTime / teamBuildSeconds)
			m.possCoast = 0
			m.possBuffDrain = 0
			m.possDebuffDrain = 0
			m.possContestLatched = false
		}
	case defenderNearBall && !ownerNearBall && m.possDebuffDrain >= 1:
		// The defending team has the ball ALONE and its debuff has now drained FULLY (gradually, via
		// the relief above) -- so it has WON the ball cleanly. Hand the charge over: the former owner
		// becomes the new (freshly debuffed) conceding side and the winner builds from zero. Until the
		// debuff is fully drained, a defender alone on the ball falls through to the contested case
		// below and keeps draining -- so ownership flips smoothly rather than snapping on first touch.
		m.possSide = m.possSide.Opponent()
		m.possProgress = clampUnit(deltaTime / teamBuildSeconds)
		m.possCoast = 0
		m.possBuffDrain = 0
		m.possDebuffDrain = 0
		m.possContestLatched = false
	case contested:
		// Both teams still contesting (the owner is on the ball too, or its carrier is being marked):
		// the buff and (when the defender is on the ball) the debuff only DRAIN over time -- there is
		// no handover while the owner is still holding on. The build progress is preserved.
		m.possCoast = 0
	case ownerTouches:
		// The owning team holds the ball uncontested: build. If the ball was in flight (coast > 0)
		// the charge had begun to decay -- bake that into the progress so a receiving teammate
		// inherits the CURRENT (decayed) strength and rebuilds from there, not from full.
		if m.possCoast > 0 {
			m.possProgress = teamBuildCurveInv(teamBuildCurve(m.possProgress) * teamCoastEnvelope(m.possCoast))
		}
		m.possProgress = clampUnit(m.possProgress + deltaTime/teamBuildSeconds)
		m.possCoast = 0
	default:
		// Nobody touching and no contest: hold then decay (progress preserved until a touch bakes
		// in the decay).
		if m.possSide != SideNone {
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

// resetKickoff recentres the ball and returns every player to its home position, facing
// its attacking goal. The touch history is cleared so a goal can never be attributed
// across a kickoff; the goal log and the match clock are kept. When staged, the conceding
// team's (KickoffSide) taker is placed on the centre dot facing the opponent goal with a
// tiny gap and m.kickoffArmed is set; an unstaged reset (match start / a fresh timed stage)
// just leaves the ball dead-centre with everyone home. The flag is informational only --
// it never gates physics.
func (m *Match) resetKickoff(staged bool) {
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
		p.shootCharge = 0
		p.trapCharge = 0
		p.trapAura = 0
		p.shootHeldPrev = false
		p.shootCanceled = false
		p.trapHeldPrev = false
		p.evictDwell = 0
		p.Body.SetRadius(p.Tuning.Radius)
		p.Body.MaxSpeed = p.Tuning.MaxSpeed
		// Face the attacking goal (FaceTowards normalises and is a no-op for a coincident point).
		p.FaceTowards(m.AttackingGoal(p.Team).Center)
	}

	// Emit a kickoff marker and reset the recorder's pass-derivation latches (so nothing is
	// attributed across a kickoff) and its prevPos baseline (so the teleport home is not
	// counted as distance). Done after positions are reset and the touch history is cleared.
	m.rec.onKickoff(m)

	m.kickoffArmed = staged

	// Centre-circle setup. A staged kickoff places the kickoff side's taker INSIDE the circle, a
	// bit off the ball; everyone else is pushed OUT so the kickoff begins with the circle
	// otherwise clear. (The match start is not staged -- it goes through clearCenterCircle in
	// BuildMatchSized -- so it has no taker: the ball simply sits alone in the circle.)
	var taker *Player
	if staged {
		taker = m.kickoffTaker(m.KickoffSide())
		if taker == nil {
			m.kickoffArmed = false // the conceding side has no one to take it
		}
	}
	m.clearCenterCircle(taker)
	if taker != nil {
		m.placeKickoffTaker(taker, m.Ball.Position, m.AttackingGoal(taker.Team).Center)
	}
}

// kickoffTaker picks the side's kickoff taker: the lone outfielder closest behind the
// ball is awkward to model here, so we simply use the first outfielder (index 1) when one
// exists, else the only player (a 1-player team). Mirrors penaltyTaker's index convention
// (index 0 is the keeper).
func (m *Match) kickoffTaker(side Side) *Player {
	t := m.teamFor(side)
	if len(t.Players) > 1 {
		return t.Players[1]
	}
	if len(t.Players) > 0 {
		return t.Players[0]
	}
	return nil
}

// kickoffTakerStandoff is how far behind the ball (beyond the ball+player radii) the conceding
// side's taker stands at a kickoff -- "a bit off the ball", clamped so the taker stays inside the
// circle when it is small.
const kickoffTakerStandoff = 16.0

// clearCenterCircle pushes every player except exempt to just OUTSIDE the centre circle, radially
// out from the centre spot (so each stays on its own side of the pitch), clearing the circle for a
// kickoff. It moves only the kickoff Position -- HomePosition and the formations are untouched, so
// normal play and the AI's positioning are unchanged; the armed-kickoff standoff then holds the
// defenders out until the ball is in play.
func (m *Match) clearCenterCircle(exempt *Player) {
	r := m.Field.CenterCircleRadius()
	if r <= 0 {
		return
	}
	center := m.Field.CenterSpot
	for _, p := range m.Players {
		if p == exempt {
			continue
		}
		off := p.Position.Sub(center)
		minD := r + p.Radius() + 4 // clear the painted line with a small margin
		if geom.Norm(off) >= minD {
			continue
		}
		dir := off
		if geom.Norm(dir) < 1e-6 {
			dir = center.Sub(m.AttackingGoal(p.Team).Center) // on the spot: push toward our own half
			if geom.Norm(dir) < 1e-6 {
				dir = geom.NewVec(-1, 0)
			}
		}
		p.Position = center.Add(geom.Unit(dir).Scale(minD))
		p.Velocity = geom.NewVec(0, 0)
		p.Acceleration = geom.NewVec(0, 0)
		p.moveHeading = geom.Vec{}
	}
}

// placeKickoffTaker stands the conceding side's taker INSIDE the centre circle, a bit behind the
// ball on the line toward the opponent goal, facing it, motionless -- close enough to strike
// without a run-up, far enough to read as "a bit off the ball". The standoff is clamped so the
// whole taker stays inside the circle even when it is small. HomePosition is left as the taker's
// formation spot, so after the kickoff it resumes its normal role.
func (m *Match) placeKickoffTaker(p *Player, ballPos, goalCenter geom.Vec) {
	dir := goalCenter.Sub(ballPos)
	if dir == (geom.Vec{}) {
		dir = geom.NewVec(1, 0)
	}
	unit := geom.Unit(dir)
	gap := m.Ball.Radius() + p.Radius() + kickoffTakerStandoff
	if maxGap := m.Field.CenterCircleRadius() - p.Radius() - 4; maxGap > 0 && gap > maxGap {
		gap = maxGap
	}
	if floor := m.Ball.Radius() + p.Radius() + 1; gap < floor {
		gap = floor // never overlap the ball
	}
	p.Position = ballPos.Sub(unit.Scale(gap)) // a bit behind the ball, toward our own half
	p.Velocity = geom.NewVec(0, 0)
	p.Acceleration = geom.NewVec(0, 0)
	p.moveHeading = geom.Vec{}
	p.Facing = unit
}

// KickoffArmed reports whether a staged kickoff is set up and not yet taken (the taker is
// on the centre dot). It is informational only -- it never gates physics -- and is cleared
// the first tick a touch is recorded after the kickoff.
func (m *Match) KickoffArmed() bool { return m.kickoffArmed }

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
	return BuildMatchSized(field, teamSize, teamSize)
}

// BuildMatchSized builds a standard match with per-team roster sizes (home = left/Blue,
// away = right/Red). buildFormation already lays out an arbitrary count per team, so the
// two teams may differ in size.
func BuildMatchSized(field *Field, homeSize, awaySize int) *Match {
	left := &Team{Side: SideLeft, Name: "Blue", Color: color.RGBA{80, 140, 255, 255}}
	right := &Team{Side: SideRight, Name: "Red", Color: color.RGBA{255, 100, 100, 255}}

	m := &Match{
		Field: field,
		Teams: [2]*Team{left, right},
		Ball:  NewBall(field.CenterSpot, 7.5),
	}

	id := 0
	left.Players = buildFormation(field, left, homeSize, &id)
	right.Players = buildFormation(field, right, awaySize, &id)
	m.Players = append(m.Players, left.Players...)
	m.Players = append(m.Players, right.Players...)
	m.clearCenterCircle(nil) // match start: the ball sits alone in the centre circle, players outside
	m.applyConfig(config.Default())
	return m
}

// BuildMatchFromConfig builds a standard match and applies a full config (ruleset,
// physics tuning, RNG seed). The field is expected to be built from cfg.Geometry.
func BuildMatchFromConfig(field *Field, teamSize int, cfg config.Config) *Match {
	return BuildMatchFromConfigSized(field, teamSize, teamSize, cfg)
}

// BuildMatchFromConfigSized builds a per-team-sized match and applies a full config
// (ruleset, physics tuning, RNG seed). The field is expected to be built from
// cfg.Geometry.
func BuildMatchFromConfigSized(field *Field, homeSize, awaySize int, cfg config.Config) *Match {
	m := BuildMatchSized(field, homeSize, awaySize)
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
	p := NewPlayer(0, start, DefaultPlayerTuning(500), left)
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
	p0 := NewPlayer(0, geom.NewVec(c.X-120, c.Y), DefaultPlayerTuning(500), left)
	p0.Role = RoleMidfielder
	p0.Number = 1
	p1 := NewPlayer(1, geom.NewVec(c.X+120, c.Y), DefaultPlayerTuning(500), right)
	p1.Role = RoleMidfielder
	p1.Number = 2
	p1.Facing = geom.NewVec(-1, 0)

	left.Players = []*Player{p0}
	right.Players = []*Player{p1}
	m.Players = []*Player{p0, p1}
	m.applyConfig(config.Default())
	return m
}

// formationLine groups the outfield players into depth-banded lines: defenders, then
// midfielders, then forwards. The keeper is always added separately at index 0.
type formationLine struct {
	role  Role
	count int
	// depth is the fraction of the team's OWN half (0 = on the goal line, 1 = at the
	// halfway line) at which the line sits.
	depth float64
}

// outfieldLines returns the DEF/MID/FWD line breakdown for k outfield players (the
// roster minus the keeper). The shapes mirror real small-sided formations and scale
// across the supported team sizes; any larger count keeps adding midfielders. Depths
// deepen the further forward a line plays.
func outfieldLines(k int) []formationLine {
	switch k {
	case 0:
		return nil
	case 1: // GK + lone striker
		return []formationLine{{RoleStriker, 1, 0.78}}
	case 2: // 1-0-1
		return []formationLine{{RoleMidfielder, 1, 0.45}, {RoleStriker, 1, 0.82}}
	case 3: // 1-1-1
		return []formationLine{{RoleMidfielder, 1, 0.35}, {RoleMidfielder, 1, 0.6}, {RoleStriker, 1, 0.85}}
	case 4: // 2-1-1
		return []formationLine{{RoleMidfielder, 2, 0.35}, {RoleMidfielder, 1, 0.62}, {RoleStriker, 1, 0.86}}
	case 5: // 2-2-1
		return []formationLine{{RoleMidfielder, 2, 0.35}, {RoleMidfielder, 2, 0.62}, {RoleStriker, 1, 0.86}}
	case 6: // 2-3-1
		return []formationLine{{RoleMidfielder, 2, 0.32}, {RoleMidfielder, 3, 0.58}, {RoleStriker, 1, 0.86}}
	default: // 7+ : 3 at the back, the surplus in midfield, 1 up top
		fwd := 1
		def := 3
		mid := k - def - fwd
		return []formationLine{{RoleMidfielder, def, 0.3}, {RoleMidfielder, mid, 0.56}, {RoleStriker, fwd, 0.86}}
	}
}

// buildFormation lays out one team across its own half in role-based, depth-banded lines:
// a keeper on the goal line at index 0 (number 1) -- always, so penaltyTaker/humanSlot's
// index-0-is-keeper convention holds -- then DEF/MID/FWD lines per outfieldLines, each
// line spread evenly across the pitch via (i+1)/(count+1) so a player never sits on a
// touchline. A team of 1 is a lone midfielder with no keeper. PlayerID order follows the
// *id sequence (keeper first, then each line). Every player faces the opponent goal.
func buildFormation(f *Field, team *Team, n int, id *int) []*Player {
	if n < 1 {
		return nil
	}
	players := make([]*Player, 0, n)
	center := f.CenterSpot
	halfWidth := f.Width() / 2 // distance from the goal line to the halfway line

	var ownX, dir float64
	face := geom.NewVec(1, 0)
	if team.Side == SideLeft {
		ownX, dir = f.Min.X, 1
	} else {
		ownX, dir, face = f.Max.X, -1, geom.NewVec(-1, 0)
	}

	number := 1
	add := func(role Role, pos geom.Vec) {
		p := NewPlayer(*id, pos, TuningForRole(role), team)
		p.Role = role
		p.Number = number
		p.Facing = face
		players = append(players, p)
		*id++
		number++
	}

	// A lone player (n == 1) is an outfielder, not a keeper. Otherwise the keeper is
	// always index 0 / number 1, parked just off its own goal line.
	k := n
	if n >= 2 {
		add(RoleGoalkeeper, geom.NewVec(ownX+dir*40, center.Y))
		k = n - 1
	}

	for _, line := range outfieldLines(k) {
		depth := 60 + line.depth*(halfWidth-60) // keep a margin off the goal line
		x := ownX + dir*depth
		for i := 0; i < line.count; i++ {
			// Even Y-spread: (i+1)/(count+1) places count players inside the pitch height
			// with equal gaps, so a line never sits on a touchline.
			y := f.Min.Y + f.Height()*float64(i+1)/float64(line.count+1)
			add(line.role, geom.NewVec(x, y))
		}
	}
	return players
}
