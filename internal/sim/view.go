package sim

import (
	"phootball/internal/config"
	"phootball/internal/geom"
)

// View is the read-only window a controller (AI or human) sees onto the match. It is the
// ONLY way a controller observes the game: every datum a bot reasons about -- the ball,
// itself, its teammates and opponents, who holds the ball, the field, the rules -- is
// reached through one of these functions, never by touching a raw sim struct. The only
// way a controller affects the game is the Intent it returns, so observation and action
// are both funnelled through the game's own API and the simulation cannot tell a bot from
// a human. The implementation wraps the live match, so reads are always current and
// allocation-free except for the team-roster slices.
type View interface {
	// Ball returns a read-only handle to the ball.
	Ball() BallView
	// Me returns the controlled player by id, or (nil, false) if it is not in the match.
	Me(id int) (PlayerView, bool)
	// Carrier returns the player in firm possession of the ball, or (nil, false) if loose.
	Carrier() (PlayerView, bool)
	// Teammates returns of's team-mates (the same team, EXCLUDING of), in stable order.
	Teammates(of PlayerView) []PlayerView
	// Squad returns of's whole team (INCLUDING of), in stable order.
	Squad(of PlayerView) []PlayerView
	// Opponents returns the players on the other team from of, in stable order.
	Opponents(of PlayerView) []PlayerView
	// Field returns a read-only handle to the pitch geometry.
	Field() FieldView
	// AttackingGoalCenter returns the centre of the goal of's team is attacking.
	AttackingGoalCenter(of PlayerView) geom.Vec
	// DefendingGoalCenter returns the centre of the goal of's team is defending.
	DefendingGoalCenter(of PlayerView) geom.Vec
	// KickoffSide returns the team taking the next kickoff.
	KickoffSide() Side
	// Tick is the current simulation tick.
	Tick() uint64
	// Clock is the elapsed match time in seconds.
	Clock() float64
	// Seed is the match RNG seed (lets deterministic bots vary run-to-run).
	Seed() int64
	// BallFriction is the ball's per-second linear drag coefficient (negative).
	BallFriction() float64
	// Rules is the match ruleset (offside, box caps, win conditions).
	Rules() config.Ruleset
}

// BallView is a read-only handle to the ball.
type BallView interface {
	Position() geom.Vec
	Velocity() geom.Vec
	Radius() float64
}

// PlayerView is a read-only handle to one player. It exposes both raw state (position,
// velocity, facing, the rate-limited movement heading) and the derived relationships a
// bot needs as first-class functions -- the angle of a point relative to the player's
// facing, and identity/affiliation tests -- so "where is the ball relative to where I'm
// looking" and "is this me / my team-mate" are game-provided queries, not something the
// bot recomputes from raw fields.
type PlayerView interface {
	ID() int
	Number() int
	Role() Role
	Side() Side

	Position() geom.Vec
	Velocity() geom.Vec
	Facing() geom.Vec
	// Heading is the player's current steering direction: the rate-limited movement
	// heading that rotates toward the requested direction at the player's TurnRate. It is
	// the zero vector before the player has moved (and just after a kickoff reset), which
	// correctly means "no committed direction". A bot reads this to account for the fact
	// that it cannot redirect instantly.
	Heading() geom.Vec
	Possession() float64
	ShootCharge() float64
	TrapCharge() float64
	Radius() float64
	// Stats returns a copy of the player's stat block (max speed, turn rate, shoot curve,
	// ranges...). It is a value, so reading it cannot mutate the player.
	Stats() PlayerStats
	HomePosition() geom.Vec

	// Same reports whether other is this same player.
	Same(other PlayerView) bool
	// SameTeam reports whether other plays for this player's team.
	SameTeam(other PlayerView) bool
	// AngleToFacing returns the unsigned angle (0..pi) between this player's facing and the
	// direction from it to point -- how far off straight-ahead point sits.
	AngleToFacing(point geom.Vec) float64
	// BallAngleToFacing returns the unsigned angle (0..pi) between this player's facing and
	// the direction to the ball.
	BallAngleToFacing(b BallView) float64
}

// FieldView is a read-only handle to the pitch geometry the rules and bots read.
type FieldView interface {
	Min() geom.Vec
	Max() geom.Vec
	Width() float64
	Height() float64
	GoalHeight() float64
	CenterSpot() geom.Vec
	CenterCircleRadius() float64
	GoalArea(side Side) config.Rect
	OffsideLineX(attacking Side, frac float64) float64
}

// View returns the read-only controller window onto this match. It wraps the live match
// (not a snapshot), so handles always read current state. A method wrapper is used rather
// than having *Match implement View directly because Match already has exported FIELDS
// named Tick/Clock/Seed, which would clash with the same-named View methods.
func (m *Match) View() View { return matchView{m} }

// matchView, playerView, ballView and fieldView are one-word value wrappers over the live
// pointers: passing one copies a single pointer (no heap), and every method is a direct
// field load or a call into the existing sim logic, so the View adds no behaviour and no
// numeric difference -- it only narrows what a controller can reach.
type matchView struct{ m *Match }
type playerView struct{ p *Player }
type ballView struct{ b *Ball }
type fieldView struct{ f *Field }

// unwrapPlayer recovers the underlying *Player from a PlayerView handle, or nil for a nil
// or foreign handle.
func unwrapPlayer(pv PlayerView) *Player {
	if w, ok := pv.(playerView); ok {
		return w.p
	}
	return nil
}

func (v matchView) Ball() BallView   { return ballView{v.m.Ball} }
func (v matchView) Field() FieldView { return fieldView{v.m.Field} }

func (v matchView) Me(id int) (PlayerView, bool) {
	if p := v.m.PlayerByID(id); p != nil {
		return playerView{p}, true
	}
	return nil, false
}

func (v matchView) Carrier() (PlayerView, bool) {
	if c := v.m.ballCarrier(); c != nil {
		return playerView{c}, true
	}
	return nil, false
}

func (v matchView) Teammates(of PlayerView) []PlayerView {
	me := unwrapPlayer(of)
	if me == nil {
		return nil
	}
	out := make([]PlayerView, 0, len(me.Team.Players))
	for _, q := range me.Team.Players {
		if q != me {
			out = append(out, playerView{q})
		}
	}
	return out
}

func (v matchView) Squad(of PlayerView) []PlayerView {
	me := unwrapPlayer(of)
	if me == nil {
		return nil
	}
	out := make([]PlayerView, 0, len(me.Team.Players))
	for _, q := range me.Team.Players {
		out = append(out, playerView{q})
	}
	return out
}

func (v matchView) Opponents(of PlayerView) []PlayerView {
	me := unwrapPlayer(of)
	if me == nil {
		return nil
	}
	var out []PlayerView
	for _, t := range v.m.Teams {
		if t != me.Team {
			out = make([]PlayerView, 0, len(t.Players))
			for _, q := range t.Players {
				out = append(out, playerView{q})
			}
		}
	}
	return out
}

func (v matchView) AttackingGoalCenter(of PlayerView) geom.Vec {
	me := unwrapPlayer(of)
	if me == nil {
		return geom.Vec{}
	}
	return v.m.AttackingGoal(me.Team).Center
}

func (v matchView) DefendingGoalCenter(of PlayerView) geom.Vec {
	me := unwrapPlayer(of)
	if me == nil {
		return geom.Vec{}
	}
	return v.m.DefendingGoal(me.Team).Center
}

func (v matchView) KickoffSide() Side     { return v.m.KickoffSide() }
func (v matchView) Tick() uint64          { return v.m.Tick }
func (v matchView) Clock() float64        { return v.m.Clock }
func (v matchView) Seed() int64           { return v.m.Seed }
func (v matchView) BallFriction() float64 { return v.m.Tuning.BallFriction }
func (v matchView) Rules() config.Ruleset { return v.m.Rules }

func (v ballView) Position() geom.Vec { return v.b.Position }
func (v ballView) Velocity() geom.Vec { return v.b.Velocity }
func (v ballView) Radius() float64    { return v.b.Radius() }

func (v playerView) ID() int                { return v.p.PlayerID }
func (v playerView) Number() int            { return v.p.Number }
func (v playerView) Role() Role             { return v.p.Role }
func (v playerView) Side() Side             { return v.p.Team.Side }
func (v playerView) Position() geom.Vec     { return v.p.Position }
func (v playerView) Velocity() geom.Vec     { return v.p.Velocity }
func (v playerView) Facing() geom.Vec       { return v.p.Facing }
func (v playerView) Heading() geom.Vec      { return v.p.moveHeading }
func (v playerView) Possession() float64    { return v.p.possession }
func (v playerView) ShootCharge() float64   { return v.p.shootCharge }
func (v playerView) TrapCharge() float64    { return v.p.trapCharge }
func (v playerView) Radius() float64        { return v.p.Radius() }
func (v playerView) Stats() PlayerStats     { return v.p.Stats }
func (v playerView) HomePosition() geom.Vec { return v.p.HomePosition }

func (v playerView) Same(other PlayerView) bool { return unwrapPlayer(other) == v.p }
func (v playerView) SameTeam(other PlayerView) bool {
	o := unwrapPlayer(other)
	return o != nil && o.Team == v.p.Team
}

func (v playerView) AngleToFacing(point geom.Vec) float64 {
	return geom.AngleBetween(v.p.Facing, point.Sub(v.p.Position))
}

func (v playerView) BallAngleToFacing(b BallView) float64 {
	return geom.AngleBetween(v.p.Facing, b.Position().Sub(v.p.Position))
}

func (v fieldView) Min() geom.Vec               { return v.f.Min }
func (v fieldView) Max() geom.Vec               { return v.f.Max }
func (v fieldView) Width() float64              { return v.f.Width() }
func (v fieldView) Height() float64             { return v.f.Height() }
func (v fieldView) GoalHeight() float64         { return v.f.GoalHeight }
func (v fieldView) CenterSpot() geom.Vec        { return v.f.CenterSpot }
func (v fieldView) CenterCircleRadius() float64 { return v.f.CenterCircleRadius() }
func (v fieldView) GoalArea(side Side) config.Rect {
	return v.f.GoalArea(side)
}
func (v fieldView) OffsideLineX(attacking Side, frac float64) float64 {
	return v.f.OffsideLineX(attacking, frac)
}
