package sim

import (
	"math"

	"phootball/internal/geom"
)

// teamBuildCurve maps build progress (0..1, the held-time fraction) to charge strength on a
// strongly ACCELERATING ramp -- it stays LOW for the majority of the build and shoots up only
// in the last portion. strength = progress^BuildExponent (cubic by default), so full strength
// is reached only near the end of the (deliberately long) build window.
func (m *Match) teamBuildCurve(progress float64) float64 {
	return math.Pow(clampUnit(progress), m.Tuning.Possession.BuildExponent)
}

// teamBuildCurveInv inverts teamBuildCurve: the build progress that yields a given charge
// strength. Used when a teammate receives a (possibly decayed) charge -- the decayed strength
// is baked back into the progress so the build resumes from that point, not from full.
func (m *Match) teamBuildCurveInv(strength float64) float64 {
	return math.Pow(clampUnit(strength), 1.0/m.Tuning.Possession.BuildExponent)
}

// teamCoastEnvelope maps seconds-since-last-touch to a 0..1 fade applied after a release: full
// (1) through HoldSeconds, then a smooth CONVEX decay to 0 by DecaySeconds -- a gentle fall at
// first that speeds up toward the end. 1 - x^2 over the decay window: flat where x=0, steepening
// to its fastest at x=1. The window is long so a released charge lingers and fades slowly.
func (m *Match) teamCoastEnvelope(coast float64) float64 {
	hold, decay := m.Tuning.Possession.HoldSeconds, m.Tuning.Possession.DecaySeconds
	switch {
	case coast <= hold:
		return 1
	case coast >= decay:
		return 0
	default:
		x := (coast - hold) / (decay - hold) // 0..1 across the decay
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
	return m.teamBuildCurve(m.possProgress) * m.teamCoastEnvelope(m.possCoast)
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
	// Who has the ball within reach (touching, or in a player's base possession reach -- NOT
	// trap-extended; see possessionReach)? These gate both the publish-step boost drain/recovery and
	// the charge update below, so compute up front.
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
			p.boostDrain = clampUnit(p.boostDrain + m.Tuning.Possession.BoostContactDrainPerSecond*deltaTime)
		} else if ownerNearBall {
			p.boostDrain = clampUnit(p.boostDrain - m.Tuning.Possession.BoostContactRecoverPerSecond*deltaTime)
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
		m.possBuffDrain = clampUnit(m.possBuffDrain + m.Tuning.Possession.DrainPerSecond*deltaTime)
	} else if ownerNearBall {
		m.possBuffDrain = clampUnit(m.possBuffDrain - m.Tuning.Possession.DrainPerSecond*deltaTime)
	}

	// Debuff relief mirrors the buff: it DRAINS while a defender contests the ball OR the ball is
	// loose after a defender touched it (latched) -- so a deflection off a defender keeps relieving
	// the whole conceding team's debuff even as the ball flies away. It REGENERATES (climbs back
	// toward full) only while the owning team has clean control (ownerNearBall, no defender), and
	// FREEZES on a clean release the defender never touched -- it never increases while the defending
	// team is contesting or chasing a loose touched ball. Ownership hands over once it has fully
	// drained with a defender alone on the ball (below).
	if defenderNearBall || (m.possContestLatched && !ownerNearBall) {
		m.possDebuffDrain = clampUnit(m.possDebuffDrain + m.Tuning.Possession.DrainPerSecond*deltaTime)
	} else if ownerNearBall {
		m.possDebuffDrain = clampUnit(m.possDebuffDrain - m.Tuning.Possession.DrainPerSecond*deltaTime)
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
			m.possProgress = clampUnit(deltaTime / m.Tuning.Possession.BuildSeconds)
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
		m.possProgress = clampUnit(deltaTime / m.Tuning.Possession.BuildSeconds)
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
			m.possProgress = m.teamBuildCurveInv(m.teamBuildCurve(m.possProgress) * m.teamCoastEnvelope(m.possCoast))
		}
		m.possProgress = clampUnit(m.possProgress + deltaTime/m.Tuning.Possession.BuildSeconds)
		m.possCoast = 0
	default:
		// Nobody touching and no contest: hold then decay (progress preserved until a touch bakes
		// in the decay).
		if m.possSide != SideNone {
			m.possCoast += deltaTime
			if m.possCoast >= m.Tuning.Possession.DecaySeconds {
				m.resetTeamPossession()
			}
		}
	}
}
