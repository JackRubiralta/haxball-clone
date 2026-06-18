package control

import (
	"testing"

	"phootball/internal/config"
	"phootball/internal/geom"
	"phootball/internal/sim"
)

// TestDribbleAvoidsWall: when the only "open" escape (peeling away from a marker) points
// straight into a wall, the dribbler must NOT carry the ball into the wall -- the wall-avoidance
// term steers the heading along/away from it instead of grinding the ball into the boundary.
func TestDribbleAvoidsWall(t *testing.T) {
	m := sim.BuildMatchFromConfig(sim.NewStandardField(), 3, config.Default())
	f := m.Field

	var carrier *sim.Player
	for _, pl := range m.Teams[0].Players {
		if pl.Role != sim.RoleGoalkeeper {
			carrier = pl
			break
		}
	}
	// Pin the carrier against the top wall with a marker directly below (toward midfield), so
	// "away from the marker" points straight up into the wall.
	carrier.Position = geom.NewVec(f.CenterSpot.X-100, f.Min.Y+carrier.Radius()+6)
	defender := m.Teams[1].Players[0]
	defender.Position = carrier.Position.Add(geom.NewVec(0, 36))
	// Park everyone else far away so only this marker shapes pressure/space.
	for _, pl := range m.Players {
		if pl != carrier && pl != defender {
			pl.Position = geom.NewVec(f.Max.X-60, f.Max.Y-60)
		}
	}

	ai := NewAISkill(carrier.PlayerID, SkillImpossible)
	p := newPerception(m, carrier)
	overshoot := func() float64 {
		target, _ := ai.bestDribble(*p)
		return geom.Dist(target, confineSlot(*p, target)) // how far the heading runs past the bounds
	}

	ai.tune.dribbleWallAvoid = 0
	without := overshoot()
	ai.tune.dribbleWallAvoid = defaultAITuning().dribbleWallAvoid
	with := overshoot()

	if without <= 25 {
		t.Fatalf("precondition: without wall avoidance the dribble should head into the wall, overshoot=%.1f", without)
	}
	if with >= without {
		t.Errorf("wall avoidance should reduce how far the dribble heads past the wall: with=%.1f without=%.1f", with, without)
	}
	if with > 25 {
		t.Errorf("with wall avoidance the dribble target should stay ~in bounds, overshoot=%.1f", with)
	}
}

// TestKeeperPassesSafeOutletInsteadOfClearing: a keeper with the ball and a safe, open, short
// outlet should PASS it (play it out) rather than hoof a clearance. The mate is too square for
// the progressive bestPass gate and the through-ball spot is contested, so only the outlet
// fallback can find the pass -- it must.
func TestKeeperPassesSafeOutletInsteadOfClearing(t *testing.T) {
	m := sim.BuildMatchFromConfig(sim.NewStandardField(), 3, config.Default())
	f := m.Field

	var keeper, mate *sim.Player
	for _, pl := range m.Teams[0].Players {
		if pl.Role == sim.RoleGoalkeeper {
			keeper = pl
		} else if mate == nil {
			mate = pl
		}
	}
	keeper.Position = geom.NewVec(f.Min.X+50, f.CenterSpot.Y)
	m.Ball.Position = keeper.Position.Add(geom.NewVec(keeper.Radius()+m.Ball.Radius(), 0))
	// Open mate just square/short of the ball (not progressive enough for bestPass). Kept a SHORT
	// hop from the ball so the outlet lane has a comfortable laneSafe margin at any reasonable pass
	// pace -- the keeper must play it out regardless of how soft passes are tuned (the scenario used
	// to sit right on the safety threshold, so a softer pass tipped it into a clear; that was test
	// fragility, not a behaviour bug).
	mate.Position = geom.NewVec(f.Min.X+72, f.CenterSpot.Y+45)
	for _, pl := range m.Teams[0].Players {
		if pl != keeper && pl != mate {
			pl.Position = geom.NewVec(f.Max.X-60, f.Max.Y-60)
		}
	}
	// One opponent on the through-ball spot ahead of the mate (kills bestPass's through ball);
	// the rest parked far so the short outlet to the mate's feet is safe and uncontested.
	opps := m.Teams[1].Players
	opps[0].Position = mate.Position.Add(geom.NewVec(110, 0))
	for i := 1; i < len(opps); i++ {
		opps[i].Position = geom.NewVec(f.Max.X-40, f.Min.Y+40)
	}

	ai := NewAISkill(keeper.PlayerID, SkillImpossible)
	p := newPerception(m, keeper)
	if !p.iControl {
		t.Fatalf("precondition: keeper should control the ball")
	}
	ai.keeperDistribute(*p)
	if ai.LastAction() != "pass" {
		t.Errorf("keeper should play a safe outlet pass instead of clearing; got %q", ai.LastAction())
	}
}

// TestKeeperGuardAnglesScaleWithNet: the keeper holds the angle on the ball-to-goal line,
// shifted toward the ball's side and clamped to the ACTUAL goal mouth -- so a bigger net lets
// it range further out to cover the wider angle, and it never strays past the posts.
func TestKeeperGuardAnglesScaleWithNet(t *testing.T) {
	guardOffset := func(goalHeight float64) (off, mouthHalf float64) {
		field := sim.NewField(geom.NewVec(60, 60), geom.NewVec(940, 540), 40, goalHeight)
		m := sim.BuildMatchFromConfig(field, 3, config.Default())
		var keeper *sim.Player
		for _, pl := range m.Teams[0].Players {
			if pl.Role == sim.RoleGoalkeeper {
				keeper = pl
			}
		}
		cy := field.CenterSpot.Y
		m.Ball.Position = geom.NewVec(field.Min.X+200, cy+150) // a wide, high shot angle near the box
		ai := NewAISkill(keeper.PlayerID, SkillImpossible)
		p := newPerception(m, keeper)
		spot := ai.keeperGuardSpot(*p)
		return spot.Y - cy, goalHeight/2 - keeper.Radius()
	}

	smallOff, smallMouth := guardOffset(50)
	bigOff, bigMouth := guardOffset(150)

	if smallOff <= 0 || bigOff <= 0 {
		t.Errorf("keeper should shift toward the ball's side (positive): small=%.1f big=%.1f", smallOff, bigOff)
	}
	if smallOff > smallMouth+0.01 || bigOff > bigMouth+0.01 {
		t.Errorf("keeper must stay within the mouth: smallOff=%.1f (mouth %.1f), bigOff=%.1f (mouth %.1f)",
			smallOff, smallMouth, bigOff, bigMouth)
	}
	if bigOff <= smallOff {
		t.Errorf("a bigger net should let the keeper range further toward the wide ball: small=%.1f big=%.1f", smallOff, bigOff)
	}
}
