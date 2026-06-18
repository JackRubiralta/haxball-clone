package control

import (
	"phootball/internal/geom"
	"phootball/internal/sim"
)

// Positional-rule awareness. Offside and the keeper-box limit are enforced by the sim as
// soft clamps (internal/sim/zonerule.go). Rather than fight that clamp -- which would make
// a player jitter against an invisible line -- the AI voluntarily keeps its target slots
// legal, so it simply never runs into the rule.

// kickoffActive reports whether the AI should treat the ball as a dead kickoff ball: the
// authoritative case is the sim having armed a staged kickoff (View.KickoffArmed -- taker on
// the centre dot, no touch yet), which is what gates the defending side's standoff. It also
// stays true while the ball is simply sitting (near) still on the centre spot, so the AI's
// own-half / formation clamps keep discipline through any centre-spot dead-ball lull, not
// only a sim-staged kickoff (this is what the off-ball positioning has always relied on).
func kickoffActive(p perception) bool {
	if p.view.KickoffArmed() {
		return true
	}
	f := p.view.Field()
	return geom.Dist(p.ball, f.CenterSpot()) < f.CenterCircleRadius()*0.6 && geom.Norm(p.ballVel) < 8
}

// clampOwnHalf keeps a slot on the team's own side of the halfway line (with a small
// margin), as required at a kickoff.
func clampOwnHalf(p perception, v geom.Vec) geom.Vec {
	f := p.view.Field()
	cx := f.CenterSpot().X
	m := p.me.Radius() + 2
	if p.me.Side() == sim.SideLeft {
		v.X = clampFloat(v.X, f.Min().X, cx-m)
	} else {
		v.X = clampFloat(v.X, cx+m, f.Max().X)
	}
	return v
}

// clampZoneRules pulls a slot back so it never violates the offside line or strays into
// the team's own goal area when the keeper-box rule is active.
func clampZoneRules(p perception, v geom.Vec) geom.Vec {
	r := p.view.Rules()
	f := p.view.Field()
	if r.OffsideEnabled && !weHaveBall(p) {
		frac := r.OffsideFrac
		if frac <= 0 {
			frac = 2.0 / 3.0
		}
		line := f.OffsideLineX(p.me.Side(), frac)
		if p.me.Side() == sim.SideLeft {
			v.X = clampFloat(v.X, f.Min().X, line)
		} else {
			v.X = clampFloat(v.X, line, f.Max().X)
		}
	}
	if r.GoalAreaMaxPlayers > 0 && p.me.Role() != sim.RoleGoalkeeper {
		v = keepOutOfGoalArea(p, v)
	}
	return v
}

// weHaveBall reports whether this player's team is in possession.
func weHaveBall(p perception) bool {
	return p.carrier != nil && p.carrier.SameTeam(p.me)
}

// keepOutOfGoalArea nudges a slot to the pitch-side edge of the team's own goal area, so
// an outfielder leaves the six-yard box to the keeper and never becomes the evicted
// surplus player.
func keepOutOfGoalArea(p perception, v geom.Vec) geom.Vec {
	box := p.view.Field().GoalArea(p.me.Side())
	if v.X < box.Min.X || v.X > box.Max.X || v.Y < box.Min.Y || v.Y > box.Max.Y {
		return v // already outside the box
	}
	m := p.me.Radius() + 2
	if p.me.Side() == sim.SideLeft {
		v.X = box.Max.X + m
	} else {
		v.X = box.Min.X - m
	}
	return v
}
