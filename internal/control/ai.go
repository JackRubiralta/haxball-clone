package control

import (
	"phootball/internal/geom"
	"phootball/internal/sim"
)

// AI is a headless, role-aware controller. The nearest teammate presses the ball,
// the ball carrier drives at the enemy goal and shoots when lined up, the keeper
// tracks its goal line and clears, and everyone else holds a ball-biased formation.
// It produces the same Intent a human does, so the simulation cannot tell them apart.
type AI struct {
	ID          int
	shootLatched bool // true once a shot has been asserted, until the urge clears (for tap shots)
}

// NewAI creates an AI controller for the player with the given id.
func NewAI(id int) *AI { return &AI{ID: id} }

const (
	aiShootRange   = 280 // distance to goal under which a lined-up carrier shoots
	aiKeeperRush   = 4.0  // multiples of radius within which the keeper leaves its line
	aiSupportBias  = 0.25 // how far off-position a supporter drifts toward the ball
	aiArriveRadius = 6.0  // movement deadzone so players settle on their target
)

// Intent decides this player's action for the tick.
func (a *AI) Intent(view *sim.Match) sim.Intent {
	me := view.PlayerByID(a.ID)
	if me == nil {
		return sim.Intent{}
	}

	ballPos := view.Ball.Position
	toBall := ballPos.Sub(me.Position)
	gap := geom.Norm(toBall) - me.Radius() - view.Ball.Radius()
	inControl := gap < me.Stats.TouchRange

	enemyGoal := view.AttackingGoal(me.Team).Center
	ownGoal := view.DefendingGoal(me.Team).Center

	in := sim.Intent{Aim: ballPos}

	switch me.Role {
	case sim.RoleGoalkeeper:
		guardY := clampFloat(ballPos.Y, view.Field.Min.Y+40, view.Field.Max.Y-40)
		target := geom.NewVec(ownGoal.X+goalDepthOffset(me)*30, guardY)
		if geom.Dist(me.Position, ballPos) < me.Radius()*aiKeeperRush {
			target = ballPos
		}
		in.Move = target.Sub(me.Position)
		in.Throttle = throttleToward(in.Move)
		if inControl {
			in.ShootHeld = true
			in.Aim = geom.NewVec(view.Field.CenterSpot.X, ballPos.Y) // clear it up-field
		}

	default: // midfielder / striker
		switch {
		case inControl:
			in.Aim = enemyGoal
			in.Move = enemyGoal.Sub(me.Position)
			in.Throttle = 1
			if geom.Dist(me.Position, enemyGoal) < aiShootRange && facing(me.Facing, enemyGoal.Sub(me.Position)) {
				in.ShootHeld = true
			}
		case view.ClosestToBall(me):
			in.Move = toBall
			in.Throttle = 1
		default:
			support := me.HomePosition.Add(ballPos.Sub(me.HomePosition).Scale(aiSupportBias))
			in.Move = support.Sub(me.Position)
			in.Throttle = throttleToward(in.Move)
		}
	}

	// Fire a single tap shot: hold the button for one tick, then release (the sim
	// shoots on the release edge with minimal charge). Re-arms once the urge clears.
	want := in.ShootHeld
	in.ShootHeld = want && !a.shootLatched
	a.shootLatched = want
	return in
}

// goalDepthOffset returns +1 if the player's own goal is on the left (so it stands
// just in front of it, toward the pitch), -1 if on the right.
func goalDepthOffset(p *sim.Player) float64 {
	if p.Team.Side == sim.SideLeft {
		return 1
	}
	return -1
}

// throttleToward returns 0 inside a small deadzone so players settle on their
// target, and full throttle otherwise.
func throttleToward(move geom.Vec) float64 {
	if geom.Norm(move) < aiArriveRadius {
		return 0
	}
	return 1
}

// facing reports whether the player is aimed close enough at target to shoot.
func facing(face, target geom.Vec) bool {
	length := geom.Norm(target)
	if length == 0 {
		return true
	}
	return geom.Dot(face, target.Scale(1/length)) > 0.9
}

func clampFloat(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
