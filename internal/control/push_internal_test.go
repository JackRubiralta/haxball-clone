package control

import (
	"testing"

	"phootball/internal/config"
	"phootball/internal/geom"
	"phootball/internal/sim"
)

// leftOutfielder returns a non-keeper player on team 0 (which attacks +x), with all other
// players cleared out of the way, for the radial-geometry push tests.
func leftOutfielder(m *sim.Match) *sim.Player {
	var me *sim.Player
	for _, pl := range m.Teams[0].Players {
		if pl.Role != sim.RoleGoalkeeper {
			me = pl
			break
		}
	}
	for _, q := range m.Players {
		if q != me {
			q.Position = geom.NewVec(q.Position.X, 40) // park others off the way
		}
	}
	return me
}

// TestPushClearsGeometry pins the clear gate: a push only counts as a clearance when its fixed
// radial direction sends the ball AWAY from our own goal (upfield/wide), never back into danger.
func TestPushClearsGeometry(t *testing.T) {
	m := sim.BuildMatchFromConfig(sim.NewStandardField(), 3, config.Default())
	me := leftOutfielder(m) // attacks +x
	ai := NewAISkill(me.PlayerID, SkillImpossible)
	surface := me.Radius() + m.Ball.Radius()
	me.Position = geom.NewVec(220, 240)

	// Ball just in front (upfield, +x): pushable and a valid clearance.
	m.Ball.Position = me.Position.Add(geom.NewVec(surface+1, 0))
	p := newPerception(m, me)
	if !ai.canPush(*p) {
		t.Fatalf("ball within the pull radius should be pushable (gap %.2f)", p.gapToBall)
	}
	if !ai.pushClears(*p) {
		t.Errorf("a push with the ball upfield should clear (it sends the ball away from our goal)")
	}

	// Ball behind us (toward our own goal, -x): a push would drive it goalward -> NOT a clear.
	m.Ball.Position = me.Position.Add(geom.NewVec(-(surface + 1), 0))
	p = newPerception(m, me)
	if ai.pushClears(*p) {
		t.Errorf("a push with the ball toward our own goal must NOT count as a clearance")
	}
}

// TestPushShotOnRange pins the shot gate: a push is a shot only from close range lined up at
// the goal mouth (no aim assist, so it must already be on target).
func TestPushShotOnRange(t *testing.T) {
	m := sim.BuildMatchFromConfig(sim.NewStandardField(), 3, config.Default())
	me := leftOutfielder(m)
	ai := NewAISkill(me.PlayerID, SkillImpossible)
	surface := me.Radius() + m.Ball.Radius()
	goal := m.View().AttackingGoalCenter(viewMe(m, me))

	// Close, ball lined up between us and the goal centre: a push is a shot on target.
	me.Position = goal.Add(geom.NewVec(-50, 0))
	m.Ball.Position = me.Position.Add(geom.NewVec(surface+1, 0))
	p := newPerception(m, me)
	if !ai.pushShotOn(*p) {
		t.Errorf("a close-range push lined up at the mouth should be a shot on target")
	}

	// Far from goal: too far for a 70%-power jab to be a real chance -> not a push shot.
	me.Position = goal.Add(geom.NewVec(-300, 0))
	m.Ball.Position = me.Position.Add(geom.NewVec(surface+1, 0))
	p = newPerception(m, me)
	if ai.pushShotOn(*p) {
		t.Errorf("a long-range push should not count as a shot on target")
	}
}

// TestKeeperPushesAwayUnderPressure: a keeper with the ball, an attacker bearing down, and no
// safe pass BOOTS it clear instantly with a push rather than dwelling on a slow charged clear.
func TestKeeperPushesAwayUnderPressure(t *testing.T) {
	m := sim.BuildMatchFromConfig(sim.NewStandardField(), 3, config.Default())
	var keeper *sim.Player
	var mates []*sim.Player
	for _, pl := range m.Teams[0].Players {
		if pl.Role == sim.RoleGoalkeeper {
			keeper = pl
		} else {
			mates = append(mates, pl)
		}
	}
	if keeper == nil || len(mates) < 2 {
		t.Fatalf("expected a keeper and >=2 outfielders on team 0 (got keeper=%v mates=%d)", keeper != nil, len(mates))
	}
	keeper.Position = geom.NewVec(110, 240)
	m.Ball.Position = keeper.Position.Add(geom.NewVec(keeper.Radius()+m.Ball.Radius(), 0)) // ball in front (+x)

	// Mark both outfielders right on top so there is no safe pass (to-feet space ~0, and the
	// on-man marker ties the race to any through-ball spot -> every pass is rejected).
	mates[0].Position = geom.NewVec(300, 150)
	mates[1].Position = geom.NewVec(300, 330)
	opps := m.Teams[1].Players
	opps[0].Position = mates[0].Position.Add(geom.NewVec(3, 0))
	opps[1].Position = mates[1].Position.Add(geom.NewVec(3, 0))
	// And an attacker bearing down on the keeper itself (to the side, so the +x lane stays clear).
	opps[2].Position = keeper.Position.Add(geom.NewVec(0, 30))

	ai := NewAISkill(keeper.PlayerID, SkillImpossible)
	p := newPerception(m, keeper)
	if !p.iControl {
		t.Fatalf("precondition: keeper should control the ball (gap %.2f)", p.gapToBall)
	}
	if !(p.pressureOnMe > ai.tune.pushPressure) {
		t.Fatalf("precondition: keeper should be under heavy pressure (got %.2f, need >%.2f)", p.pressureOnMe, ai.tune.pushPressure)
	}

	in := ai.keeperDistribute(*p)
	if !in.Push {
		t.Errorf("a pressured keeper with no safe pass should PUSH the ball clear (Push=%v, LastAction=%q)", in.Push, ai.LastAction())
	}
	if ai.LastAction() != "clear" {
		t.Errorf("the push clearance should record a clear action, got %q", ai.LastAction())
	}

	// Control case: with the pressure removed, the keeper takes the controlled charged clear, not a push.
	opps[2].Position = geom.NewVec(700, 240) // attacker steps off
	p2 := newPerception(m, keeper)
	if p2.pressureOnMe > ai.tune.pushPressure {
		t.Fatalf("control setup: keeper should no longer be under heavy pressure (got %.2f)", p2.pressureOnMe)
	}
	ai2 := NewAISkill(keeper.PlayerID, SkillImpossible)
	if in2 := ai2.keeperDistribute(*p2); in2.Push {
		t.Errorf("an unpressured keeper should charge a controlled clear, not push")
	}
}
