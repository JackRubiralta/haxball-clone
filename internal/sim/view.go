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
	// Me returns the controlled player by id as a SelfView (full self-knowledge), or
	// (nil, false) if it is not in the match.
	Me(id int) (SelfView, bool)
	// Carrier returns the player in firm possession of the ball, or (nil, false) if loose.
	// It is an ObservedView: a controller knows WHICH player has the ball (visible), not that
	// player's hidden internal state.
	Carrier() (ObservedView, bool)
	// Teammates returns of's team-mates (the same team, EXCLUDING of), in stable order, as
	// ObservedViews (only what a human can see of another player).
	Teammates(of ObservedView) []ObservedView
	// Squad returns of's whole team (INCLUDING of), in stable order, as ObservedViews.
	Squad(of ObservedView) []ObservedView
	// Opponents returns the players on the other team from of, in stable order, as
	// ObservedViews.
	Opponents(of ObservedView) []ObservedView
	// Field returns a read-only handle to the pitch geometry.
	Field() FieldView
	// AttackingGoalCenter returns the centre of the goal of's team is attacking.
	AttackingGoalCenter(of ObservedView) geom.Vec
	// DefendingGoalCenter returns the centre of the goal of's team is defending.
	DefendingGoalCenter(of ObservedView) geom.Vec
	// KickoffSide returns the team taking the next kickoff.
	KickoffSide() Side
	// KickoffArmed reports whether a staged kickoff is set up and not yet taken (the
	// conceding side's taker is on the centre dot). Informational only -- it never gates
	// physics; the defending side uses it to stand off until the ball is played.
	KickoffArmed() bool
	// Tick is the current simulation tick.
	Tick() uint64
	// Clock is the elapsed match time in seconds.
	Clock() float64
	// NoiseSalt returns a deterministic per-(match, id) value a controller can mix into its
	// own noise so its variety survives run-to-run, WITHOUT exposing the raw RNG seed (which a
	// human cannot see). It replaces the former Seed() accessor.
	NoiseSalt(id int) int64
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

// ObservedView is what a controller may know about ANOTHER player -- EXACTLY what a human
// perceives from the rendered scene (see netcode.EntityState / render.go: position, facing,
// radius, jersey number, role/side, and both charge gauges, which are drawn for every
// player). It deliberately exposes NO hidden internal state -- not velocity, not the
// rate-limited steering heading, not possession build-up, not the tuning block -- because
// none of those are rendered, so neither the AI nor a human can read them off the screen.
// This narrowing is the type-level half of the "AI can only do what a human can" boundary.
//
// Keep this field set in lockstep with netcode.EntityState and what render.go draws.
type ObservedView interface {
	ID() int
	Number() int
	Role() Role
	Side() Side

	Position() geom.Vec
	Facing() geom.Vec
	Radius() float64
	ShootCharge() float64 // rendered as a gauge for every player
	TrapCharge() float64  // rendered as a gauge for every player

	// Same reports whether other is this same player.
	Same(other ObservedView) bool
	// SameTeam reports whether other plays for this player's team.
	SameTeam(other ObservedView) bool
	// AngleToFacing returns the unsigned angle (0..pi) between this player's facing and the
	// direction from it to point -- how far off straight-ahead point sits.
	AngleToFacing(point geom.Vec) float64
	// BallAngleToFacing returns the unsigned angle (0..pi) between this player's facing and
	// the direction to the ball.
	BallAngleToFacing(b BallView) float64
}

// SelfView is the full handle a controller has on the player IT controls -- everything in
// ObservedView plus the internal state a player knows about ITSELF: its velocity, its
// rate-limited movement heading, its possession build-up, its tuning block, and its home
// position. Only View.Me returns a SelfView; every other-player handle is an ObservedView, so
// a non-self handle CANNOT be type-asserted up to SelfView -- the hidden state is unreachable.
type SelfView interface {
	ObservedView
	Velocity() geom.Vec
	// Heading is the player's current steering direction: the rate-limited movement heading
	// that rotates toward the requested direction at the player's TurnRate. It is the zero
	// vector before the player has moved (and just after a kickoff reset). A controller reads
	// its OWN heading to account for the fact that it cannot redirect instantly.
	Heading() geom.Vec
	Possession() float64
	// Tuning returns a copy of the player's own stat block (max speed, turn rate, shoot
	// curve, ranges...). It is a value, so reading it cannot mutate the player.
	Tuning() config.PlayerTuning
	HomePosition() geom.Vec
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

// matchView, observedView, selfView, ballView and fieldView are one-word value wrappers over
// the live pointers: passing one copies a single pointer (no heap), and every method is a
// direct field load or a call into the existing sim logic, so the View adds no behaviour and
// no numeric difference -- it only narrows what a controller can reach.
//
// selfView EMBEDS observedView, so it has every ObservedView method plus the self-only ones;
// observedView does NOT have the self-only methods, so an other-player handle (observedView)
// can never be type-asserted up to SelfView.
type matchView struct{ m *Match }
type observedView struct{ p *Player }
type selfView struct{ observedView }
type ballView struct{ b *Ball }
type fieldView struct{ f *Field }

// unwrapPlayer recovers the underlying *Player from any player handle (observed or self), or
// nil for a nil or foreign handle.
func unwrapPlayer(ov ObservedView) *Player {
	switch w := ov.(type) {
	case observedView:
		return w.p
	case selfView:
		return w.p
	}
	return nil
}

func (v matchView) Ball() BallView   { return ballView{v.m.Ball} }
func (v matchView) Field() FieldView { return fieldView{v.m.Field} }

func (v matchView) Me(id int) (SelfView, bool) {
	if p := v.m.PlayerByID(id); p != nil {
		return selfView{observedView{p}}, true
	}
	return nil, false
}

func (v matchView) Carrier() (ObservedView, bool) {
	if c := v.m.ballCarrier(); c != nil {
		return observedView{c}, true
	}
	return nil, false
}

func (v matchView) Teammates(of ObservedView) []ObservedView {
	me := unwrapPlayer(of)
	if me == nil {
		return nil
	}
	out := make([]ObservedView, 0, len(me.Team.Players))
	for _, q := range me.Team.Players {
		if q != me {
			out = append(out, observedView{q})
		}
	}
	return out
}

func (v matchView) Squad(of ObservedView) []ObservedView {
	me := unwrapPlayer(of)
	if me == nil {
		return nil
	}
	out := make([]ObservedView, 0, len(me.Team.Players))
	for _, q := range me.Team.Players {
		out = append(out, observedView{q})
	}
	return out
}

func (v matchView) Opponents(of ObservedView) []ObservedView {
	me := unwrapPlayer(of)
	if me == nil {
		return nil
	}
	var out []ObservedView
	for _, t := range v.m.Teams {
		if t != me.Team {
			out = make([]ObservedView, 0, len(t.Players))
			for _, q := range t.Players {
				out = append(out, observedView{q})
			}
		}
	}
	return out
}

func (v matchView) AttackingGoalCenter(of ObservedView) geom.Vec {
	me := unwrapPlayer(of)
	if me == nil {
		return geom.Vec{}
	}
	return v.m.AttackingGoal(me.Team).Center
}

func (v matchView) DefendingGoalCenter(of ObservedView) geom.Vec {
	me := unwrapPlayer(of)
	if me == nil {
		return geom.Vec{}
	}
	return v.m.DefendingGoal(me.Team).Center
}

func (v matchView) KickoffSide() Side  { return v.m.KickoffSide() }
func (v matchView) KickoffArmed() bool { return v.m.KickoffArmed() }
func (v matchView) Tick() uint64       { return v.m.Tick }
func (v matchView) Clock() float64     { return v.m.Clock }

// NoiseSalt mixes the match seed with a player id into a deterministic per-(match, id) value,
// so a controller's variety survives run-to-run without ever seeing the raw seed.
func (v matchView) NoiseSalt(id int) int64 {
	x := uint64(v.m.Seed)*0x9e3779b97f4a7c15 + uint64(id)*0x632be59bd9b4e019
	x ^= x >> 33
	x *= 0xff51afd7ed558ccd
	x ^= x >> 33
	return int64(x)
}

// BallFriction reports the ACTUAL ball-body friction the simulation is integrating with,
// not the config value, so a controller's prediction can never drift from the live ball
// (applyConfig stamps Tuning.BallFriction onto the body, so they normally agree).
func (v matchView) BallFriction() float64 { return v.m.Ball.Friction }
func (v matchView) Rules() config.Ruleset { return v.m.Rules }

func (v ballView) Position() geom.Vec { return v.b.Position }
func (v ballView) Velocity() geom.Vec { return v.b.Velocity }
func (v ballView) Radius() float64    { return v.b.Radius() }

// ObservedView methods (the human-perceivable subset) live on observedView, so selfView
// inherits them by embedding.
func (v observedView) ID() int              { return v.p.PlayerID }
func (v observedView) Number() int          { return v.p.Number }
func (v observedView) Role() Role           { return v.p.Role }
func (v observedView) Side() Side           { return v.p.Team.Side }
func (v observedView) Position() geom.Vec   { return v.p.Position }
func (v observedView) Facing() geom.Vec     { return v.p.Facing }
func (v observedView) ShootCharge() float64 { return v.p.shootCharge }
func (v observedView) TrapCharge() float64  { return v.p.trapCharge }
func (v observedView) Radius() float64      { return v.p.Radius() }

func (v observedView) Same(other ObservedView) bool { return unwrapPlayer(other) == v.p }
func (v observedView) SameTeam(other ObservedView) bool {
	o := unwrapPlayer(other)
	return o != nil && o.Team == v.p.Team
}

func (v observedView) AngleToFacing(point geom.Vec) float64 {
	return geom.AngleBetween(v.p.Facing, point.Sub(v.p.Position))
}

func (v observedView) BallAngleToFacing(b BallView) float64 {
	return geom.AngleBetween(v.p.Facing, b.Position().Sub(v.p.Position))
}

// Self-only methods (the hidden internal state) live on selfView, so an observedView handle
// never carries them and cannot be type-asserted up to SelfView.
func (v selfView) Velocity() geom.Vec          { return v.p.Velocity }
func (v selfView) Heading() geom.Vec           { return v.p.moveHeading }
func (v selfView) Possession() float64         { return v.p.possession }
func (v selfView) Tuning() config.PlayerTuning { return v.p.Tuning }
func (v selfView) HomePosition() geom.Vec      { return v.p.HomePosition }

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
