package sim

import (
	"phootball/internal/geom"
	"phootball/internal/physics"
)

// Penalty shootout timing (seconds).
const (
	penaltyReadySeconds   = 0.8 // pause before each kick goes live
	penaltyResultSeconds  = 1.2 // pause after a MISS before the next kick
	penaltyGoalSeconds    = 3.0 // pause after a SCORED pen before the next kick (longer to savour it)
	penaltyAimSeconds     = 8.0 // a frozen taker has this long to aim and strike before it is a miss
	penaltyResolveSeconds = 4.0 // after the strike, the shot has this long to score before it is a miss
	penaltyStopSpeed      = 6.0 // a released ball slower than this (after a moment) is dead -> a miss
)

// keeperLineBand is how far (px) the keeper may stray from its goal line before release: it may
// shuffle laterally along the line and aim, but cannot charge the spot.
const keeperLineBand = 6.0

// PenKick records one taken penalty for post-match attribution. Order is the count of kicks that
// side had already taken before this one (so the first kick of a side is Order 1).
type PenKick struct {
	Side    Side
	TakerID int
	Scored  bool
	Order   int
}

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
	released   bool // the taker has taken its one touch -- the ball is in flight and the keeper may move
	timer      float64
	takerID    int
	keeperID   int
	lastScored bool
	kicks      []PenKick
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

// beginShootout starts the shootout; a coin toss picks who shoots first.
func (m *Match) beginShootout() {
	m.shootout = &Shootout{
		BestOf:      max(1, m.Rules.Penalties.BestOf),
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
	// The penalties chain skips resetKickoff, so the recorder's pass/shot/save latches are never
	// cleared on entry. Reset them here so a still-live on-target shot from regulation cannot
	// credit a phantom save to the first penalty keeper.
	m.rec.resetDerivation()
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

// ShootoutKicks returns a copy of every penalty taken so far (in order), for the post-match
// screen, or nil if there is no shootout.
func (m *Match) ShootoutKicks() []PenKick {
	if m.shootout == nil || len(m.shootout.kicks) == 0 {
		return nil
	}
	out := make([]PenKick, len(m.shootout.kicks))
	copy(out, m.shootout.kicks)
	return out
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
	s.released = false
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
	// Stand the taker right at the ball (just within shoot range) facing the goal: it strikes from
	// here without a run-up, and stays frozen on the spot for its single touch.
	taker.Position = geom.NewVec(spot.X-into*(m.Ball.Radius()+taker.Radius()+1), spot.Y)
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
		m.stepPenaltyPlay(inputs, dt) // sets s.released and restarts s.timer when the strike fires
		if m.shootout == nil {
			return
		}
		s.timer += dt
		defender := s.taker.Opponent()
		if !s.released {
			// Aiming: the frozen taker lines up its one strike. Dawdling past the window is a miss.
			if s.timer > penaltyAimSeconds {
				m.recordKick(false)
			}
			return
		}
		// The strike is away -- resolve the shot (goal, stopped/saved, or out of time).
		switch {
		case m.Field.CheckGoal(m.Ball) == defender:
			m.recordKick(true)
		case s.timer > penaltyResolveSeconds:
			m.recordKick(false)
		case s.timer > 0.4 && geom.Norm(m.Ball.Velocity) < penaltyStopSpeed:
			m.recordKick(false)
		}
	case kickDone:
		s.timer += dt
		wait := penaltyResultSeconds
		if s.lastScored {
			wait = penaltyGoalSeconds // savour a scored pen for longer
		}
		if s.timer < wait {
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

// stepPenaltyPlay runs one tick of a live penalty. The taker is frozen on the spot -- it may aim
// and charge, but never moves and only ever touches the ball with its single strike. The keeper
// is held on its line (it may turn but not move) until the ball is RELEASED by that strike; after
// release it is free to move and dive for the save. The ball sits still on the spot until struck,
// then flies. Everyone else stays frozen.
func (m *Match) stepPenaltyPlay(inputs map[int]Intent, dt float64) {
	s := m.shootout
	taker := m.PlayerByID(s.takerID)
	keeper := m.PlayerByID(s.keeperID)

	// Keeper: before release it may shuffle laterally along its line and aim, but it cannot charge
	// the spot -- its X is clamped within keeperLineBand of the goal line (and its Y to the mouth).
	if keeper != nil {
		in := inputs[keeper.PlayerID]
		m.applyIntent(keeper, in, dt)
		if !s.released {
			keeper.Body.Update(dt)
			m.clampKeeperToLine(keeper, s.taker.Opponent())
		}
	}

	if !s.released {
		// Aiming: the taker is frozen on the spot, aiming and charging its one strike; nothing else
		// moves and the ball sits still on the spot until the strike releases it.
		if taker != nil {
			in := inputs[taker.PlayerID]
			in.Move = geom.Vec{} // frozen in place: no run-up, no follow-up
			in.Throttle = 0
			m.applyIntent(taker, in, dt) // facing + shoot charge only; Move(0) => no acceleration
			taker.Velocity = geom.NewVec(0, 0)
			taker.Acceleration = geom.NewVec(0, 0)
			if taker.WantsKick {
				if shoot(taker, m.Ball) { // the single touch
					m.recordTouch(taker, TouchKick)
					m.emit(SoundKick, geom.Norm(m.Ball.Velocity), m.Ball.Position)
					s.released = true
					s.timer = 0 // restart the clock for the shot's flight
				}
				taker.WantsKick = false
				taker.shootCharge = 0
			}
		}
		return
	}

	// Released: the taker has spent its one touch and is now inert (it never touches the ball
	// again). The ball is live and the keeper is free to move and save.
	m.Ball.Update(dt)
	if keeper != nil {
		keeper.Body.Update(dt)
	}
	if spd := m.Field.ConfineBall(m.Ball, m.Tuning.BallWallRestitution); spd > ballHitMinSpeed {
		m.emit(SoundBallHit, spd, m.Ball.Position)
	}
	if keeper != nil {
		if touched, bounce := handleBallToPlayerInteraction(m.Ball, keeper, dt); touched {
			m.recordTouch(keeper, TouchDribble)
			if bounce > ballHitMinSpeed {
				m.emit(SoundBallHit, bounce, m.Ball.Position)
			}
		}
	}
	for _, g := range m.Field.Goals() {
		for _, post := range g.Posts {
			physics.Collide(m.Ball.Body, post, m.Tuning.BallWallRestitution)
		}
		for _, seg := range g.Net {
			physics.Collide(m.Ball.Body, seg, m.Tuning.NetRestitution)
		}
	}
	if keeper != nil {
		if keeper.WantsKick { // the keeper may boot a rebound clear
			if shoot(keeper, m.Ball) {
				m.recordTouch(keeper, TouchKick)
				m.rec.onKick(m, keeper)
				m.emit(SoundKick, geom.Norm(m.Ball.Velocity), m.Ball.Position)
			}
			keeper.WantsKick = false
			keeper.shootCharge = 0
		}
		m.Field.ConfinePlayer(keeper, m.Tuning.PlayerWallRestitution)
	}
}

// clampKeeperToLine holds the keeper near its goal line before release: it may shuffle laterally
// (Y, within the goal mouth) and aim, but its X is pinned within keeperLineBand of the line so it
// cannot charge the spot. Velocity into the line is killed so it does not drift off over time.
func (m *Match) clampKeeperToLine(keeper *Player, defender Side) {
	// Mirror setupKick's geometry exactly: goalX is the keeper's goal line and `into` points from
	// the spot toward the goal, so the keeper stands at goalX - into*(r+1) and the pitch lies in
	// the -into direction.
	spot := m.Field.PenaltySpot(defender)
	goalX := m.Field.Min.X
	if defender == SideRight {
		goalX = m.Field.Max.X
	}
	into := 1.0
	if goalX < spot.X {
		into = -1.0
	}
	// The keeper stands a hair off its line; allow a small band toward the pitch (never behind).
	lineX := goalX - into*(keeper.Radius()+1)
	near, far := lineX, lineX-into*keeperLineBand
	lo, hi := near, far
	if lo > hi {
		lo, hi = hi, lo
	}
	if keeper.Position.X < lo {
		keeper.Position.X = lo
		if keeper.Velocity.X < 0 {
			keeper.Velocity.X = 0
		}
	} else if keeper.Position.X > hi {
		keeper.Position.X = hi
		if keeper.Velocity.X > 0 {
			keeper.Velocity.X = 0
		}
	}
	// Confine laterally to the goal mouth so it patrols the line, not the whole box.
	top, bot := m.Field.goalMouthRange()
	r := keeper.Radius()
	top += r
	bot -= r
	if top > bot {
		top, bot = (top+bot)/2, (top+bot)/2
	}
	if keeper.Position.Y < top {
		keeper.Position.Y = top
		if keeper.Velocity.Y < 0 {
			keeper.Velocity.Y = 0
		}
	} else if keeper.Position.Y > bot {
		keeper.Position.Y = bot
		if keeper.Velocity.Y > 0 {
			keeper.Velocity.Y = 0
		}
	}
}

func (m *Match) recordKick(scored bool) {
	s := m.shootout
	i := sideIndex(s.taker)
	s.taken[i]++
	if scored {
		s.goals[i]++
	}
	m.rec.onPenaltyKick(m, m.PlayerByID(s.takerID), scored)
	s.kicks = append(s.kicks, PenKick{
		Side:    s.taker,
		TakerID: s.takerID,
		Scored:  scored,
		Order:   s.taken[i], // this side's kicks taken so far (this kick included)
	})
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
