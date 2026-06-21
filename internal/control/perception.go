package control

import (
	"math"

	"phootball/internal/config"
	"phootball/internal/geom"
	"phootball/internal/sim"
)

// perception is the set of derived facts an AI computes once per decision from the shared
// match view. It is plain read-only data: nothing here mutates the match, so two players
// computing the same perception from the same view always agree (which is what keeps the
// team coordination deterministic).
type perception struct {
	view sim.View
	me   sim.SelfView
	dt   float64

	ball    geom.Vec
	ballVel geom.Vec

	enemyGoal geom.Vec
	ownGoal   geom.Vec
	attackX   float64 // +1 if we attack toward increasing X, -1 otherwise

	gapToBall float64 // surface gap between me and the ball
	iControl  bool    // ball is within my touch range
	myCharge  float64 // my current shoot charge in seconds
	myTrap    float64 // my trap-energy bar (0..1): the limited, recharging "aura" resource, rationed so it's available to receive an incoming pass cleanly. Own SelfView state (rendered for every player), so within the AI<=human boundary.

	carrier      sim.ObservedView // who is in firm possession (nil if loose)
	carrierMine  bool
	carrierEnemy bool
	ballLoose    bool

	teammates []sim.ObservedView // my team, excluding me
	opponents []sim.ObservedView // the other team

	nearestOppToMe  sim.ObservedView
	nearestOppDist  float64
	pressureOnMe    float64 // 0..~1: how hard the nearest opponent is bearing down on me
	pressureOnCarry float64 // pressure on whoever holds the ball
	teamControls    bool    // our side has or is winning the ball (looser than firm possession)
	ballRadius      float64
	friction        float64
	rules           config.Ruleset   // the active match ruleset (box-occupancy caps etc.); a global, on-screen setting -- reading it is within the AI<=human boundary
	moveModel       config.MoveModel // the active movement model (Standard/Directional); a global on-screen setting. Under Directional, facing the travel direction moves FAST while facing off-axis is penalised -- the AI reads this to face its run when moving and face the ball when receiving.
	seed            uint64           // per-(match, self) NoiseSalt, mixed into AI noise so variety survives run-to-run without exposing the raw seed
}

// perceive builds the per-tick perception for player me from the match view.
func perceive(view sim.View, me sim.SelfView, dt float64) perception {
	ball := view.Ball()
	p := perception{
		view:       view,
		me:         me,
		dt:         dt,
		ball:       ball.Position(),
		ballVel:    ball.Velocity(),
		ballRadius: ball.Radius(),
		friction:   view.BallFriction(),
		// Per-(match, self) salt: deterministic variety run-to-run without seeing the raw seed.
		seed: uint64(view.NoiseSalt(me.ID())),
	}

	p.enemyGoal = view.AttackingGoalCenter(me)
	p.ownGoal = view.DefendingGoalCenter(me)
	if me.Side() == sim.SideLeft {
		p.attackX = 1
	} else {
		p.attackX = -1
	}

	p.gapToBall = geom.Dist(me.Position(), p.ball) - me.Radius() - p.ballRadius
	p.iControl = p.gapToBall < me.Tuning().TouchRange
	p.myCharge = me.ShootCharge()
	p.myTrap = me.TrapCharge()
	p.rules = view.Rules()
	p.moveModel = view.MoveModel()

	if c, ok := view.Carrier(); ok {
		p.carrier = c
		p.carrierMine = c.SameTeam(me)
		p.carrierEnemy = !p.carrierMine
	} else {
		p.ballLoose = true
	}

	p.teammates = view.Teammates(me)
	p.opponents = view.Opponents(me)

	p.nearestOppToMe, p.nearestOppDist = nearest(me.Position(), p.opponents)
	p.pressureOnMe = pressure(me.Position(), p.opponents)
	if p.carrier != nil {
		if p.carrierMine {
			p.pressureOnCarry = pressure(p.carrier.Position(), p.opponents)
		}
	} else {
		p.pressureOnCarry = pressure(p.ball, p.opponents)
	}

	// teamControls is a LOOSER "our side has the ball" than firm possession: true if a teammate
	// is the carrier, or the ball is loose and our nearest man is closer to it than theirs. It
	// lets off-ball players start offering passing options even before possession is firm, so
	// there is always someone to pass to -- which is what produces flowing, watchable football.
	_, ourBest := nearest(p.ball, view.Squad(me))
	_, oppBest := nearest(p.ball, p.opponents)
	p.teamControls = p.carrierMine || (p.carrier == nil && ourBest <= oppBest)
	return p
}

// nearest returns the closest player in list to point, and the distance to it. An empty list
// yields (nil, +Inf) -- "no one, infinitely far" -- so a caller comparing distances treats an
// absent player as never the nearest (rather than the old 0.0, which read as "right here").
func nearest(point geom.Vec, list []sim.ObservedView) (sim.ObservedView, float64) {
	var best sim.ObservedView
	bestD := math.Inf(1)
	for _, q := range list {
		if d := geom.Dist(point, q.Position()); best == nil || d < bestD {
			best, bestD = q, d
		}
	}
	return best, bestD
}

// pressure scores how threatened a point is by the given players: it rises as the nearest
// one closes in (1 at contact, ~0 beyond a couple of player-lengths). It is a smooth
// proxy the AI uses to decide when to shield, clear, or release the ball quickly.
func pressure(point geom.Vec, threats []sim.ObservedView) float64 {
	q, d := nearest(point, threats)
	if q == nil {
		return 0
	}
	return clampFloat(1-d/120, 0, 1)
}

// teammateSpace returns the distance from a point to the nearest opponent -- a simple
// "how open is this spot" measure used to rank pass targets and support runs.
func (p perception) space(point geom.Vec) float64 {
	_, d := nearest(point, p.opponents)
	return d
}

// goalwardness returns how much moving from a to b advances the ball toward the enemy
// goal (positive = goalward), in world units along the attack axis.
func (p perception) goalwardness(a, b geom.Vec) float64 {
	return (b.X - a.X) * p.attackX
}

// openDuration estimates how long (seconds) a point stays open: the time until the nearest
// opponent closes to within pressure range of it, from its distance and top speed. 0 means
// already under pressure; a large value means free. It lets the AI reason not just about
// being open, but about how long it will STAY open (shoot now vs take an extra touch).
func (a *AI) openDuration(p perception, at geom.Vec) float64 {
	best := a.tune.interceptHorizon * 3 // effectively "a long time"
	for _, o := range p.opponents {
		gap := geom.Dist(o.Position(), at) - o.Radius() - p.me.Radius()
		// The opponent's top speed and velocity are hidden state, so assume the nominal speed
		// and estimate "is it closing" from its VISIBLE facing: a defender facing toward the
		// point shuts the window faster than one facing away (the same bias as before, but from
		// observable facing rather than an unreadable velocity).
		speed := a.tune.assumedOppSpeed
		if closing := geom.Dot(geom.Unit(o.Facing()).Scale(speed), geom.Unit(at.Sub(o.Position()))); closing > speed*0.5 {
			speed = closing
		}
		t := (gap - a.tune.pressureRadius) / speed
		if t < best {
			best = t
		}
	}
	return clampFloat(best, 0, a.tune.interceptHorizon*3)
}

// trapped reports whether the carrier is hemmed in: pressured and with no nearby open space
// to dribble into (its candidate headings are all blocked). Used to choose a recycle pass
// over forcing a dribble.
func (a *AI) trapped(p perception) bool {
	if p.pressureOnMe < a.tune.actPressure {
		return false
	}
	for _, ang := range []float64{-1.0471975511965976, -0.3490658503988659, 0.3490658503988659, 1.0471975511965976} { // radians (~60deg, ~20deg)
		dir := rotate(geom.Unit(p.enemyGoal.Sub(p.me.Position())), ang)
		if p.space(p.me.Position().Add(dir.Scale(70))) > a.tune.pressureRadius {
			return false // there's an open lane to dribble into
		}
	}
	return true
}
