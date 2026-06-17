package sim

import (
	"phootball/internal/geom"
	"phootball/internal/physics"
)

// Penalty shootout timing (seconds).
const (
	penaltyReadySeconds = 0.8 // pause before each kick goes live and after a result
	penaltyKickTimeout  = 6.0 // an unresolved kick by now is a miss
	penaltyStopSpeed    = 6.0 // ball slower than this (after a moment live) is dead
)

type penaltyKickState int

const (
	kickReady penaltyKickState = iota
	kickLive
	kickDone
)

// Shootout is a playable penalty shootout: kicks alternate sides, best-of-BestOf then
// sudden death. Only the current taker and the defending keeper move, so the human
// takes their own team's kicks. Side indices follow the team order (0 left, 1 right).
type Shootout struct {
	BestOf      int
	SuddenDeath bool
	First       Side

	taker      Side
	goals      [2]int
	taken      [2]int
	kickState  penaltyKickState
	timer      float64
	takerID    int
	keeperID   int
	lastScored bool
}

func sideIndex(s Side) int {
	if s == SideRight {
		return 1
	}
	return 0
}

func sideForIndex(i int) Side {
	if i == 1 {
		return SideRight
	}
	return SideLeft
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// beginShootout starts the shootout; a coin toss picks who shoots first.
func (m *Match) beginShootout() {
	m.shootout = &Shootout{
		BestOf:      maxInt(1, m.Rules.Penalties.BestOf),
		SuddenDeath: m.Rules.Penalties.SuddenDeath,
		First:       m.coinToss(),
	}
	m.shootout.taker = m.shootout.First
	m.State.Phase = PhasePenalties
	m.State.PhaseStart = m.Clock
	// The shootout pipeline never advances the team possession charge, so make sure no stale
	// charge from open play leaks into a penalty contact (resetTeamPossession also zeroes every
	// player's published per-tick coefficient). Clear the holder of record too.
	m.resetTeamPossession()
	m.possessor = nil
	m.setupKick()
	if m.shootout != nil {
		m.shootout.kickState = kickReady
	}
}

// InShootout reports whether a penalty shootout is in progress.
func (m *Match) InShootout() bool { return m.shootout != nil && m.State.Phase == PhasePenalties }

// ShootoutScore returns the goals scored by each side so far.
func (m *Match) ShootoutScore() (left, right int) {
	if m.shootout == nil {
		return 0, 0
	}
	return m.shootout.goals[0], m.shootout.goals[1]
}

// ShootoutTaken returns the kicks taken by each side so far.
func (m *Match) ShootoutTaken() (left, right int) {
	if m.shootout == nil {
		return 0, 0
	}
	return m.shootout.taken[0], m.shootout.taken[1]
}

// penaltyTaker / penaltyKeeper pick a side's kicker (an outfielder if any) and keeper.
func (m *Match) penaltyTaker(side Side) *Player {
	t := m.teamFor(side)
	if len(t.Players) > 1 {
		return t.Players[1]
	}
	if len(t.Players) > 0 {
		return t.Players[0]
	}
	return nil
}

func (m *Match) penaltyKeeper(side Side) *Player {
	t := m.teamFor(side)
	if len(t.Players) > 0 {
		return t.Players[0]
	}
	return nil
}

// setupKick places the ball on the spot, positions the taker and defending keeper, and
// parks everyone else at home.
func (m *Match) setupKick() {
	s := m.shootout
	defender := s.taker.Opponent()
	spot := m.Field.PenaltySpot(defender)
	m.Ball.Position = spot
	m.Ball.Velocity = geom.NewVec(0, 0)
	m.Ball.Acceleration = geom.NewVec(0, 0)

	taker := m.penaltyTaker(s.taker)
	keeper := m.penaltyKeeper(defender)
	if taker == nil || keeper == nil {
		m.finishShootout() // degenerate roster: decide by tally / coin toss
		return
	}
	s.takerID = taker.PlayerID
	s.keeperID = keeper.PlayerID

	goalX := m.Field.Min.X
	if defender == SideRight {
		goalX = m.Field.Max.X
	}
	into := 1.0
	if goalX < spot.X {
		into = -1.0
	}
	taker.Position = geom.NewVec(spot.X-into*(m.Ball.Radius()+taker.Radius()+8), spot.Y)
	taker.Velocity = geom.NewVec(0, 0)
	taker.Acceleration = geom.NewVec(0, 0)
	taker.Facing = geom.NewVec(into, 0)

	keeper.Position = geom.NewVec(goalX-into*(keeper.Radius()+1), m.Field.CenterSpot.Y)
	keeper.Velocity = geom.NewVec(0, 0)
	keeper.Acceleration = geom.NewVec(0, 0)
	keeper.Facing = geom.NewVec(-into, 0)

	for _, p := range m.Players {
		if p == taker || p == keeper {
			continue
		}
		p.Position = p.HomePosition
		p.Velocity = geom.NewVec(0, 0)
		p.Acceleration = geom.NewVec(0, 0)
	}
}

// stepShootout advances the shootout by one tick.
func (m *Match) stepShootout(inputs map[int]Intent, dt float64) {
	s := m.shootout
	if s == nil {
		m.finish(SideNone)
		return
	}
	switch s.kickState {
	case kickReady:
		s.timer += dt
		if s.timer >= penaltyReadySeconds {
			s.kickState = kickLive
			s.timer = 0
			m.emit(SoundWhistle, 1, m.Ball.Position)
		}
	case kickLive:
		s.timer += dt
		m.stepPenaltyPlay(inputs, dt)
		defender := s.taker.Opponent()
		switch {
		case m.Field.CheckGoal(m.Ball) == defender:
			m.recordKick(true)
		case s.timer > penaltyKickTimeout:
			m.recordKick(false)
		case s.timer > 0.6 && geom.Norm(m.Ball.Velocity) < penaltyStopSpeed:
			m.recordKick(false)
		}
	case kickDone:
		s.timer += dt
		if s.timer < penaltyReadySeconds {
			return // brief pause to show the result
		}
		if w := s.winner(); w != SideNone {
			m.finish(w)
			return
		}
		if s.settled() {
			m.finish(m.coinToss()) // tied with no sudden death (rare)
			return
		}
		s.advanceTaker()
		m.setupKick()
		if m.shootout != nil {
			s.kickState = kickReady
			s.timer = 0
		}
	}
}

// stepPenaltyPlay integrates only the taker and keeper; everyone else stays frozen.
func (m *Match) stepPenaltyPlay(inputs map[int]Intent, dt float64) {
	s := m.shootout
	taker := m.PlayerByID(s.takerID)
	keeper := m.PlayerByID(s.keeperID)
	active := [2]*Player{taker, keeper}

	for _, p := range active {
		if p != nil {
			m.applyIntent(p, inputs[p.PlayerID], dt)
		}
	}
	m.Ball.Update(dt)
	for _, p := range active {
		if p != nil {
			p.Body.Update(dt)
		}
	}
	m.advancePossessionBuilder()
	for _, p := range active {
		if p != nil {
			updatePossession(m.Ball, p, dt, p == m.possBuilder)
		}
	}
	if spd := m.Field.ConfineBall(m.Ball); spd > ballHitMinSpeed {
		m.emit(SoundBallHit, spd, m.Ball.Position)
	}
	for _, p := range active {
		if p != nil {
			if touched, bounce := handleBallToPlayerInteraction(m.Ball, p, dt); touched {
				m.recordTouch(p, TouchDribble)
				if bounce > ballHitMinSpeed {
					m.emit(SoundBallHit, bounce, m.Ball.Position)
				}
			}
		}
	}
	for _, g := range m.Field.Goals() {
		for _, post := range g.Posts {
			physics.Collide(m.Ball.Body, post, ballWallRestitution)
		}
		for _, seg := range g.Net {
			physics.Collide(m.Ball.Body, seg, netRestitution)
		}
	}
	for _, p := range active {
		if p == nil {
			continue
		}
		if p.WantsKick {
			if shoot(p, m.Ball) {
				m.recordTouch(p, TouchKick)
				m.emit(SoundKick, geom.Norm(m.Ball.Velocity), m.Ball.Position)
			}
			p.WantsKick = false
			p.shootCharge = 0
		}
		m.Field.ConfinePlayer(p)
	}
}

func (m *Match) recordKick(scored bool) {
	s := m.shootout
	i := sideIndex(s.taker)
	s.taken[i]++
	if scored {
		s.goals[i]++
	}
	s.lastScored = scored
	s.kickState = kickDone
	s.timer = 0
}

func (m *Match) finishShootout() {
	if w := m.shootout.winner(); w != SideNone {
		m.finish(w)
		return
	}
	m.finish(m.coinToss())
}

// winner returns the side that has won the shootout, or SideNone if undecided.
func (s *Shootout) winner() Side {
	a := sideIndex(s.First)
	b := 1 - a
	remA := s.BestOf - s.taken[a]
	remB := s.BestOf - s.taken[b]
	if remA < 0 {
		remA = 0
	}
	if remB < 0 {
		remB = 0
	}
	// During the best-of rounds, decide as soon as a lead cannot be caught.
	if s.taken[a] < s.BestOf || s.taken[b] < s.BestOf {
		if s.goals[a] > s.goals[b]+remB {
			return sideForIndex(a)
		}
		if s.goals[b] > s.goals[a]+remA {
			return sideForIndex(b)
		}
		return SideNone
	}
	// Both have taken at least BestOf: decide only on a completed pair.
	if s.taken[a] == s.taken[b] {
		if s.goals[a] > s.goals[b] {
			return sideForIndex(a)
		}
		if s.goals[b] > s.goals[a] {
			return sideForIndex(b)
		}
	}
	return SideNone
}

// settled reports a tie with no sudden death after a completed best-of round.
func (s *Shootout) settled() bool {
	a := sideIndex(s.First)
	b := 1 - a
	return !s.SuddenDeath && s.taken[a] >= s.BestOf && s.taken[a] == s.taken[b] && s.goals[a] == s.goals[b]
}

// advanceTaker hands the next kick to whichever side has taken fewer (the first side
// when level), so the sides alternate.
func (s *Shootout) advanceTaker() {
	a := sideIndex(s.First)
	b := 1 - a
	if s.taken[a] <= s.taken[b] {
		s.taker = sideForIndex(a)
	} else {
		s.taker = sideForIndex(b)
	}
}
