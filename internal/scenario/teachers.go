package scenario

import (
	"math"

	"phootball/internal/geom"
	"phootball/internal/sim"
)

// Competent scripted "AI algo" teachers for the drills the net struggles to discover by random
// exploration (collecting a loose ball, dribbling, and -- the hard one -- passing). They are pure
// View->Intent demonstrators: they read only what a human sees and act only through the same Intent
// channel, so they obey the AI<=human boundary and can be discretized into the policy's action heads
// (neural.Discretize) for annealed action-override bootstrapping. They are validated against their
// drill objective by cmd/teachercheck BEFORE they are allowed to teach.

// Pass/charge tuning (ticks at 60/s). A pass holds the shoot button for a distance-proportional
// number of ticks (longer hold = more power), bounded so a short feed still charges usefully and a
// long ball does not over-power. These mirror the controller's commit-machine bounds.
const (
	passTicksMin  = 5    // a short feed still charges enough to count as a real pass (above the micro floor)
	passTicksMax  = 22   // a long ball; well below the engine's hard charge cap (a crisp rondo pass, not a rocket)
	passTicksPerU = 0.04 // hold ticks per world-unit of pass distance
	passCooldown  = 10   // ticks to settle after a release before charging again
	receiveLead   = 8.0  // ticks ahead to aim when intercepting a moving ball (lead the roll)
	openLaneClear = 34.0 // a teammate's lane is "open" if every opponent is at least this far from it
)

// collect: steer onto the rolling ball's future position, decelerate as it nears, and trap to settle.
func (s *Actor) collect(view sim.View, me sim.SelfView, ball sim.BallView) sim.Intent {
	lead := ball.Position().Add(ball.Velocity().Scale(receiveLead / 60.0))
	in := sim.Intent{Move: lead.Sub(me.Position()), Throttle: 1, Aim: lead}
	g := gap(me, ball)
	if g <= me.Tuning().PullRange+2 {
		// In reach: face the ball and trap to kill its momentum (a clean first touch).
		in.Aim = ball.Position()
		in.Trap = true
		if geom.Norm(ball.Velocity()) < 25 { // already settled: hold position, keep it close
			in.Throttle = 0.2
		}
	}
	return in
}

// carry: dribble the ball toward the attacking goal under the directional model (face where you run
// so you move fastest), and finish with a shot once into the attacking third.
func (s *Actor) carry(view sim.View, me sim.SelfView, ball sim.BallView) sim.Intent {
	goal := view.AttackingGoalCenter(me)
	g := gap(me, ball)
	if g > me.Tuning().PullRange+6 {
		// Lost touch: chase the ball (aim where we run = fastest under the directional model).
		toBall := ball.Position().Sub(me.Position())
		return sim.Intent{Move: toBall, Throttle: 1, Aim: ball.Position().Add(toBall)}
	}
	toGoal := goal.Sub(me.Position())
	// Close to goal: shoot to finish (hold then release via the charge state machine).
	if geom.Norm(toGoal) < 220 {
		return s.chargePass(me, goal, geom.Norm(toGoal))
	}
	// Otherwise drive the ball goalward, facing the goal so the carry run is at full forward speed.
	return sim.Intent{Move: toGoal, Throttle: 1, Aim: goal}
}

// tikitaka: the rondo/first-touch brain. On the ball -> pass to the most open team-mate; off the
// ball -> drift to open space and trap an incoming feed. The first behaviour generates the pass
// attempts the stuck stage never produced; the second completes the exchange.
func (s *Actor) tikitaka(view sim.View, me sim.SelfView, ball sim.BallView) sim.Intent {
	mates := view.Teammates(me)
	opps := view.Opponents(me)

	reach := me.Tuning().PullRange
	if s.nearestOfTeam(view, me, ball) {
		// I am the team's man on the ball: finish any charge, settle a fast ball, then pass; if the
		// ball is loose/out of reach, go intercept it (lead the roll) -- don't stand and wait.
		if s.shootLeft > 0 || s.cooldown > 0 {
			return s.chargeStep(me)
		}
		g := gap(me, ball)
		if g <= reach+4 {
			if geom.Norm(ball.Velocity()) > 60 { // just arrived, still moving: trap to settle first
				return sim.Intent{Move: ball.Position().Sub(me.Position()), Throttle: 0.5, Aim: ball.Position(), Trap: true}
			}
			if target, dist, ok := s.bestPassTarget(me, mates, opps, ball); ok {
				return s.chargePass(me, target, dist)
			}
			away := s.awayFromNearestOpp(me, opps) // no open mate: shield, keep the ball
			return sim.Intent{Move: away, Throttle: 0.5, Aim: ball.Position()}
		}
		lead := ball.Position().Add(ball.Velocity().Scale(receiveLead / 60.0))
		return sim.Intent{Move: lead.Sub(me.Position()), Throttle: 1, Aim: lead, Trap: g <= reach+2}
	}

	// Off the ball. If a feed is rolling toward me and is within reach, trap it to receive cleanly.
	toMe := me.Position().Sub(ball.Position())
	incoming := geom.Norm(ball.Velocity()) > 20 && geom.Dot(geom.Unit(ball.Velocity()), geom.Unit(toMe)) > 0.5
	if incoming && gap(me, ball) <= me.Tuning().PullRange+6 {
		return sim.Intent{Move: ball.Position().Sub(me.Position()), Throttle: 0.5, Aim: ball.Position(), Trap: true}
	}
	// Position to receive. If I'm already open (no marker nearby, or no opponents at all), HOLD and
	// present for a pass facing the ball -- a stationary target the on-ball mate can hit. Only when
	// marked do I step into space. (Drifting every tick made the passer aim at a moving target and
	// the exchange failed -- the validation gate caught exactly this.)
	if dn := distToNearestOpp(me, opps); dn > 140 {
		return sim.Intent{Throttle: 0, Aim: ball.Position()}
	}
	away := s.awayFromNearestOpp(me, opps)
	return sim.Intent{Move: away, Throttle: 0.8, Aim: me.Position().Add(away)}
}

// Advise returns the teacher's STATELESS per-tick recommendation for guided BC/kickstarting: the
// instantaneous intent the policy should imitate at THIS state, with ShootHeld=true at the moment a
// pass/shot should be initiated (the policy's commit machine then handles the hold). Unlike Intent it
// keeps no charge counters, so it is a clean per-state supervised label (Discretize turns it into the
// factored head indices). The second return is false when there is no clear recommendation.
func (s *Actor) Advise(view sim.View) (sim.Intent, bool) {
	if view == nil {
		return sim.Intent{}, false
	}
	me, ok := view.Me(s.id)
	if !ok {
		return sim.Intent{}, false
	}
	ball := view.Ball()
	reach := me.Tuning().PullRange
	switch s.kind {
	case ScriptCollector:
		lead := ball.Position().Add(ball.Velocity().Scale(receiveLead / 60.0))
		in := sim.Intent{Move: lead.Sub(me.Position()), Throttle: 1, Aim: lead}
		if gap(me, ball) <= reach+2 {
			in.Aim, in.Trap = ball.Position(), true
		}
		return in, true
	case ScriptCarrier:
		goal := view.AttackingGoalCenter(me)
		if g := gap(me, ball); g > reach+6 {
			toBall := ball.Position().Sub(me.Position())
			return sim.Intent{Move: toBall, Throttle: 1, Aim: ball.Position().Add(toBall)}, true
		}
		toGoal := goal.Sub(me.Position())
		if geom.Norm(toGoal) < 220 { // advise: shoot to finish
			return sim.Intent{Throttle: 0, Aim: goal, ShootHeld: true}, true
		}
		return sim.Intent{Move: toGoal, Throttle: 1, Aim: goal}, true
	case ScriptTikitaka:
		mates, opps := view.Teammates(me), view.Opponents(me)
		if s.nearestOfTeam(view, me, ball) {
			g := gap(me, ball)
			if g <= reach+4 {
				if geom.Norm(ball.Velocity()) > 60 { // settle first
					return sim.Intent{Move: ball.Position().Sub(me.Position()), Throttle: 0.5, Aim: ball.Position(), Trap: true}, true
				}
				if target, _, ok := s.bestPassTarget(me, mates, opps, ball); ok {
					return sim.Intent{Throttle: 0, Aim: target, ShootHeld: true}, true // advise: charge a pass toward the open mate
				}
				away := s.awayFromNearestOpp(me, opps)
				return sim.Intent{Move: away, Throttle: 0.5, Aim: ball.Position()}, true
			}
			lead := ball.Position().Add(ball.Velocity().Scale(receiveLead / 60.0))
			return sim.Intent{Move: lead.Sub(me.Position()), Throttle: 1, Aim: lead, Trap: g <= reach+2}, true
		}
		toMe := me.Position().Sub(ball.Position())
		incoming := geom.Norm(ball.Velocity()) > 20 && geom.Dot(geom.Unit(ball.Velocity()), geom.Unit(toMe)) > 0.5
		if incoming && gap(me, ball) <= reach+6 {
			return sim.Intent{Move: ball.Position().Sub(me.Position()), Throttle: 0.5, Aim: ball.Position(), Trap: true}, true
		}
		if distToNearestOpp(me, opps) > 140 {
			return sim.Intent{Throttle: 0, Aim: ball.Position()}, true
		}
		away := s.awayFromNearestOpp(me, opps)
		return sim.Intent{Move: away, Throttle: 0.8, Aim: me.Position().Add(away)}, true
	default:
		return sim.Intent{}, false
	}
}

// --- charge state machine (shared by pass and shot) ---

// chargePass begins (or continues) a charge whose hold length encodes the pass power for `dist`,
// aimed at `target`. The first tick sets the hold budget; subsequent ticks are driven by chargeStep.
func (s *Actor) chargePass(me sim.SelfView, target geom.Vec, dist float64) sim.Intent {
	if s.shootLeft == 0 && s.cooldown == 0 {
		ticks := int(math.Round(dist * passTicksPerU))
		if ticks < passTicksMin {
			ticks = passTicksMin
		}
		if ticks > passTicksMax {
			ticks = passTicksMax
		}
		s.shootLeft = ticks
	}
	s.aimTarget = target // keep facing the pass/shot target throughout the hold
	return s.chargeStep(me)
}

// chargeStep advances the hold/release/cooldown counters and emits the matching Intent. Holding
// ShootHeld accumulates power; releasing (ShootHeld=false) fires on the edge.
func (s *Actor) chargeStep(me sim.SelfView) sim.Intent {
	aim := s.aimTarget
	if aim == (geom.Vec{}) {
		aim = me.Position().Add(me.Facing())
	}
	if s.shootLeft > 0 {
		s.shootLeft--
		if s.shootLeft == 0 {
			s.cooldown = passCooldown // released this tick (button goes up next tick -> fire)
		}
		return sim.Intent{Throttle: 0, Aim: aim, ShootHeld: true}
	}
	if s.cooldown > 0 {
		s.cooldown--
	}
	return sim.Intent{Throttle: 0, Aim: aim}
}

// --- passing geometry ---

// bestPassTarget picks the most open team-mate: the one whose lane from the ball is clearest of
// opponents and whose distance clears the micro-pass floor. Returns its position and the distance.
func (s *Actor) bestPassTarget(me sim.SelfView, mates, opps []sim.ObservedView, ball sim.BallView) (geom.Vec, float64, bool) {
	from := ball.Position()
	bestClear, bestDist := -1.0, 0.0
	var best geom.Vec
	found := false
	for _, mt := range mates {
		to := mt.Position()
		d := geom.Dist(from, to)
		if d < 80 { // too close to count as a real (length-gated) pass
			continue
		}
		clear := laneClearance(from, to, opps)
		// Prefer open lanes; among open lanes, prefer the longer pass (tiki-taka values progression).
		score := clear + 0.05*d
		if clear >= openLaneClear*0.5 && score > bestClear {
			bestClear, bestDist, best, found = score, d, to, true
		}
	}
	return best, bestDist, found
}

// laneClearance returns the smallest distance from any opponent to the segment from->to (only
// opponents that project onto the segment count -- a defender behind the passer does not block it).
func laneClearance(from, to geom.Vec, opps []sim.ObservedView) float64 {
	seg := to.Sub(from)
	l2 := geom.Dot(seg, seg)
	if l2 < 1e-6 {
		return math.MaxFloat64
	}
	minD := math.MaxFloat64
	for _, op := range opps {
		t := geom.Dot(op.Position().Sub(from), seg) / l2
		if t < 0 || t > 1 {
			continue
		}
		proj := from.Add(seg.Scale(t))
		if d := geom.Dist(op.Position(), proj); d < minD {
			minD = d
		}
	}
	return minD
}

// distToNearestOpp returns the distance to the closest opponent (MaxFloat64 if there are none).
func distToNearestOpp(me sim.SelfView, opps []sim.ObservedView) float64 {
	best := math.MaxFloat64
	for _, op := range opps {
		if d := geom.Dist(me.Position(), op.Position()); d < best {
			best = d
		}
	}
	return best
}

func (s *Actor) awayFromNearestOpp(me sim.SelfView, opps []sim.ObservedView) geom.Vec {
	var nearest sim.ObservedView
	best := math.MaxFloat64
	for _, op := range opps {
		if d := geom.Dist(me.Position(), op.Position()); d < best {
			best, nearest = d, op
		}
	}
	if nearest == nil {
		return me.Facing()
	}
	return me.Position().Sub(nearest.Position())
}

// nearestOfTeam reports whether me is the closest member of its own team to the ball (so exactly one
// team-mate plays the ball; the others get open).
func (s *Actor) nearestOfTeam(view sim.View, me sim.SelfView, ball sim.BallView) bool {
	myD := geom.Dist(me.Position(), ball.Position())
	for _, mt := range view.Teammates(me) {
		if geom.Dist(mt.Position(), ball.Position()) < myD {
			return false
		}
	}
	return true
}
