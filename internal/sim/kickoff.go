package sim

import "phootball/internal/geom"

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
