// Package scenario builds drill/situation setups (ball + player placement, roles) and the
// lightweight scripted actors (keeper / presser) used to scaffold them. It is shared by the RL env
// (cmd/env) and the live training viewer (cmd/watch) so both arrange a drill identically. It writes
// only exported position/facing/role state -- never physics -- so it is safe over any built match.
package scenario

import (
	"math"

	"phootball/internal/geom"
	"phootball/internal/sim"
)

// Kinds. Mirror these in training/phball/env_client.py (SCEN_*).
const (
	KindKickoff  = 0 // standard formation + centre ball (full game / self-play)
	KindShooting = 1 // learner attacker(s) + ball in the attacking third vs a scripted keeper
	KindRondo    = 2 // keep-away: a ring of learner passers around scripted pressers
	KindBuildup  = 3 // ball deep in the learner's own third; progress it out under pressure
	KindDefend   = 4 // ball given to the opponents in midfield; learner must defend with shape
	KindCollect  = 5 // a lone learner + a slow-rolling loose ball nearby: collect/settle (no opponent)
	KindCarry    = 6 // a lone learner + ball on a flank, carry it toward goal past one presser
)

// Name returns a short human label for a kind (for the viewer overlay).
func Name(kind int) string {
	switch kind {
	case KindCollect:
		return "COLLECT"
	case KindCarry:
		return "CARRY"
	case KindShooting:
		return "SHOOTING"
	case KindRondo:
		return "RONDO"
	case KindBuildup:
		return "BUILD-UP"
	case KindDefend:
		return "DEFEND"
	default:
		return "FULL GAME"
	}
}

// Arrange repositions the ball and players for the drill kind, from the controlled side's point of
// view, with seed-based jitter for variety. Only exported position/facing/role state is written.
func Arrange(m *sim.Match, kind int, ctrlSide sim.Side, seed int64) {
	var learner, opp *sim.Team
	for _, tm := range m.Teams {
		if tm.Side == ctrlSide {
			learner = tm
		} else {
			opp = tm
		}
	}
	if learner == nil {
		return
	}
	rng := &lcg{s: uint64(seed)*2862933555777941757 + 3037000493}
	att := m.AttackingGoal(learner).Center
	def := m.DefendingGoal(learner).Center
	fmin, fmax := m.Field.Min, m.Field.Max
	center := m.Field.CenterSpot
	height := fmax.Y - fmin.Y
	yJit := func(span float64) float64 { return (rng.next()*2 - 1) * span }
	lerpX := func(t float64) float64 { return def.X + (att.X-def.X)*t }
	place := func(p *sim.Player, pos geom.Vec, face geom.Vec) {
		p.Position = pos
		p.HomePosition = pos
		p.Velocity = geom.Vec{}
		p.Acceleration = geom.Vec{}
		if face != (geom.Vec{}) {
			p.Facing = geom.Unit(face)
		}
	}
	setBall := func(pos, vel geom.Vec) {
		m.Ball.Position = pos
		m.Ball.Velocity = vel
		m.Ball.Acceleration = geom.Vec{}
	}
	toGoal := att.Sub(center)

	switch kind {
	case KindShooting:
		for i, p := range learner.Players {
			t := 0.62 + 0.12*rng.next()
			y := center.Y + yJit(height*0.30)
			if len(learner.Players) > 1 {
				y = fmin.Y + height*(float64(i)+1)/(float64(len(learner.Players))+1) + yJit(height*0.06)
			}
			place(p, geom.NewVec(lerpX(t), y), toGoal)
		}
		if len(learner.Players) > 0 {
			lead := learner.Players[0]
			setBall(lead.Position.Add(geom.Unit(toGoal).Scale(lead.Radius()+m.Ball.Radius()+1)), geom.Vec{})
		}
		for i, p := range opp.Players {
			y := center.Y + yJit(height*0.10)
			x := att.X - geom.Unit(toGoal).X*(28+float64(i)*22)
			role := sim.RoleKeeper
			if i > 0 {
				role = sim.RoleDefender
			}
			p.Role = role
			place(p, geom.NewVec(x, y), center.Sub(geom.NewVec(x, y)))
		}

	case KindRondo:
		r := 0.20 * (fmax.X - fmin.X)
		n := len(learner.Players)
		for i, p := range learner.Players {
			ang := 2 * math.Pi * (float64(i) + 0.5) / float64(maxInt(n, 1))
			pos := center.Add(geom.NewVec(r*math.Cos(ang), r*math.Sin(ang)))
			place(p, pos, center.Sub(pos))
		}
		for _, p := range opp.Players {
			place(p, center.Add(geom.NewVec(yJit(30), yJit(30))), toGoal.Scale(-1))
		}
		if n > 0 {
			lead := learner.Players[0]
			setBall(lead.Position.Add(geom.Unit(center.Sub(lead.Position)).Scale(lead.Radius()+m.Ball.Radius()+1)), geom.Vec{})
		}

	case KindBuildup:
		for i, p := range learner.Players {
			t := 0.10 + 0.30*float64(i)/float64(maxInt(len(learner.Players)-1, 1))
			place(p, geom.NewVec(lerpX(t), fmin.Y+height*(float64(i)+1)/(float64(len(learner.Players))+1)), toGoal)
		}
		for i, p := range opp.Players {
			t := 0.30 + 0.25*float64(i)/float64(maxInt(len(opp.Players)-1, 1))
			place(p, geom.NewVec(lerpX(t), center.Y+yJit(height*0.3)), toGoal.Scale(-1))
		}
		if len(learner.Players) > 0 {
			deep := learner.Players[0]
			setBall(deep.Position.Add(geom.Unit(toGoal).Scale(deep.Radius()+m.Ball.Radius()+1)), geom.Vec{})
		}

	case KindDefend:
		for i, p := range learner.Players {
			t := 0.15 + 0.25*float64(i)/float64(maxInt(len(learner.Players)-1, 1))
			place(p, geom.NewVec(lerpX(t), center.Y+yJit(height*0.32)), toGoal)
		}
		for i, p := range opp.Players {
			t := 0.45 + 0.10*float64(i)/float64(maxInt(len(opp.Players)-1, 1))
			place(p, geom.NewVec(lerpX(t), center.Y+yJit(height*0.3)), toGoal.Scale(-1))
		}
		if len(opp.Players) > 0 {
			a := opp.Players[len(opp.Players)-1]
			setBall(a.Position.Add(geom.Unit(toGoal.Scale(-1)).Scale(a.Radius()+m.Ball.Radius()+1)), geom.Vec{})
		}

	case KindCollect:
		// A lone learner + a slow-rolling loose ball a short distance away (no opponents): the
		// learner must move to it and settle it -- the very first ball-control fundamental.
		for _, p := range learner.Players {
			place(p, center.Add(geom.NewVec(yJit(90), yJit(130))), toGoal)
		}
		if len(learner.Players) > 0 {
			lp := learner.Players[0]
			ang := rng.next() * 2 * math.Pi
			dir := geom.NewVec(math.Cos(ang), math.Sin(ang))
			// A firmer, closer feed so the rolling ball is still moving (above the receive floor)
			// when a still-learning agent arrives -- collect/settle it.
			setBall(lp.Position.Add(dir.Scale(90+60*rng.next())), dir.Scale(95+90*rng.next()))
		}

	case KindCarry:
		// A lone learner + ball at feet deep on a flank, one presser ahead: dribble the ball toward
		// the attacking goal (place-to-place) past the defender.
		for _, p := range learner.Players {
			place(p, geom.NewVec(lerpX(0.25), fmin.Y+height*(0.2+0.6*rng.next())), toGoal)
		}
		if len(learner.Players) > 0 {
			lp := learner.Players[0]
			setBall(lp.Position.Add(geom.Unit(toGoal).Scale(lp.Radius()+m.Ball.Radius()+1)), geom.Vec{})
		}
		for _, p := range opp.Players {
			place(p, geom.NewVec(lerpX(0.55), center.Y+yJit(height*0.3)), toGoal.Scale(-1))
		}

	default: // KindKickoff: keep the build's formation + centre ball
	}
}

// --- scripted scaffolding actors (NOT the rule AI; just enough to drive a drill) ---

type ScriptKind int

const (
	ScriptIdle ScriptKind = iota
	ScriptKeeper
	ScriptPresser
	// The competent "AI algo" teacher kinds (teachers.go): hand-written demonstrators that actually
	// solve a drill, used to validate the drill (cmd/teachercheck) and to bootstrap the net via
	// annealed action-override. They observe ONLY sim.View and act ONLY through sim.Intent, so they
	// respect the same AI<=human boundary as any controller -- they are NOT the rule AI.
	ScriptCollector // collect: intercept a rolling ball on its line and trap it
	ScriptCarrier   // carry: dribble the ball toward the attacking goal, shielding from a presser
	ScriptTikitaka  // firsttouch/rondo: on-ball -> pass to the most open mate; off-ball -> get open & receive
)

// IsTeacher reports whether k is one of the competent drill demonstrators (vs idle/keeper/presser).
func (k ScriptKind) IsTeacher() bool {
	return k == ScriptCollector || k == ScriptCarrier || k == ScriptTikitaka
}

// Actor is a minimal scripted control.Controller: it observes only sim.View and acts only through
// sim.Intent, so it respects the same boundary as any controller. The teacher kinds keep a little
// per-player charge state (shoot-hold countdown + cooldown) so a pass holds the shoot button for a
// distance-proportional number of ticks then releases -- the same way a human charges a pass.
type Actor struct {
	id   int
	kind ScriptKind

	shootLeft int      // ticks of shoot-hold remaining on the current pass/shot (0 = not charging)
	cooldown  int      // ticks to wait after releasing before starting another charge
	aimTarget geom.Vec // world point to keep facing through a charge hold (pass/shot destination)
}

func NewActor(id int, k ScriptKind) *Actor { return &Actor{id: id, kind: k} }

func (s *Actor) Intent(view sim.View) sim.Intent {
	if view == nil {
		return sim.Intent{}
	}
	me, ok := view.Me(s.id)
	if !ok {
		return sim.Intent{}
	}
	ball := view.Ball()
	switch s.kind {
	case ScriptKeeper:
		goal := view.DefendingGoalCenter(me)
		target := geom.NewVec(goal.X+sign(view.Field().CenterSpot().X-goal.X)*26, ball.Position().Y)
		in := sim.Intent{Move: target.Sub(me.Position()), Throttle: 1, Aim: ball.Position()}
		if gap(me, ball) <= me.Tuning().PullRange+2 {
			in.Push = true
		}
		return in
	case ScriptPresser:
		in := sim.Intent{Move: ball.Position().Sub(me.Position()), Throttle: 1, Aim: ball.Position()}
		if gap(me, ball) <= me.Tuning().PullRange+2 {
			in.Push = true
		}
		return in
	case ScriptCollector:
		return s.collect(view, me, ball)
	case ScriptCarrier:
		return s.carry(view, me, ball)
	case ScriptTikitaka:
		return s.tikitaka(view, me, ball)
	default:
		return sim.Intent{}
	}
}

func gap(me sim.SelfView, ball sim.BallView) float64 {
	return geom.Dist(me.Position(), ball.Position()) - me.Radius() - ball.Radius()
}

func sign(x float64) float64 {
	if x < 0 {
		return -1
	}
	return 1
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// lcg is a tiny deterministic PRNG for placement jitter (reproducible from the seed).
type lcg struct{ s uint64 }

func (r *lcg) next() float64 {
	r.s = r.s*6364136223846793005 + 1442695040888963407
	return float64(r.s>>11) / float64(uint64(1)<<53)
}
