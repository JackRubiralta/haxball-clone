package control

import (
	"testing"

	"phootball/internal/config"
	"phootball/internal/geom"
	"phootball/internal/sim"
)

// TestKeeperReceivesBackPassOnTrajectory: a loose, moderate-pace ball our side is collecting
// uncontested near the keeper's box (a back-pass it sweeps, not a shot it saves) should be met
// ON its trajectory -- the keeper runs to the point on the ball's predicted path where it has
// slowed (receivePoint), the same way an outfield receiver glides onto a pass in press -- rather
// than charging the earliest touchable point. This mirrors TestReceivePointOnTrajectory for the
// goalkeeper, per "make the receiver move onto the pass trajectory ... also for the keeper".
func TestKeeperReceivesBackPassOnTrajectory(t *testing.T) {
	m := sim.BuildMatchFromConfig(sim.NewStandardField(), 3, config.Default())
	f := m.Field

	var keeper *sim.Player
	for _, pl := range m.Teams[0].Players {
		if pl.Role == sim.RoleGoalkeeper {
			keeper = pl
		}
	}
	if keeper == nil {
		t.Fatal("left team has no keeper")
	}
	keeper.Position = geom.NewVec(f.Min.X+40, f.CenterSpot.Y)
	keeper.Facing = geom.NewVec(1, 0)

	// A loose ball just outside the box, rolling mostly ACROSS goal with a little back-spin toward
	// our line: fast enough to be an in-flight pass (>receiveMinSpeed) but its component AT goal is
	// gentle (<keeperSaveSpeed), so it's a ball to sweep/receive, not a shot to save.
	m.Ball.Position = geom.NewVec(f.Min.X+120, f.CenterSpot.Y-70)
	m.Ball.Velocity = geom.NewVec(-22, 100)

	// Park every other player far away so the keeper is the clear, uncontested favourite.
	for _, pl := range m.Players {
		if pl != keeper {
			pl.Position = geom.NewVec(f.Max.X-40, f.Max.Y-40)
			pl.Velocity = geom.Vec{}
		}
	}

	ai := NewAISkill(keeper.PlayerID, SkillHard)
	p := newPerception(m, keeper)
	plan := assignRoles(*p, ai.tune)

	// Preconditions: this must land in the receive/sweep band, not the save band.
	toGoal := geom.Unit(p.ownGoal.Sub(p.ball))
	if closing := geom.Dot(p.ballVel, toGoal); closing > ai.tune.keeperSaveSpeed {
		t.Fatalf("precondition: closing %.0f > keeperSaveSpeed %.0f (would trigger save, not receive)", closing, ai.tune.keeperSaveSpeed)
	}
	if !ai.receivingPass(*p) {
		t.Fatalf("precondition: scenario is not an in-flight pass to receive (ballLoose=%v vel=%.0f teamControls=%v)", p.ballLoose, geom.Norm(p.ballVel), p.teamControls)
	}
	if !ai.keeperShouldSweep(*p, plan) {
		t.Fatal("precondition: keeper does not rate this as a sweepable ball")
	}

	// The receive point must lie on the ball's forward trajectory (collinear with ballVel, ahead
	// of the ball) -- i.e. the keeper meets it on its path.
	rp := ai.receivePoint(*p)
	along := geom.Dot(rp.Sub(p.ball), geom.Unit(p.ballVel))
	if along <= 0 {
		t.Errorf("receivePoint %v is not ahead of the ball along its velocity", rp)
	}
	off := geom.Dist(rp, p.ball) - along // perpendicular distance from the trajectory line
	if off > 1.0 {
		t.Errorf("receivePoint %v is %.2f off the ball's trajectory (should be on it)", rp, off)
	}

	// The keeper's actual move must head toward that on-trajectory meet point.
	in := ai.keeper(*p, plan)
	toRP := geom.Unit(rp.Sub(keeper.Position))
	if in.Move == (geom.Vec{}) {
		t.Fatal("keeper did not move to meet the incoming ball")
	}
	if d := geom.Dot(geom.Unit(in.Move), toRP); d < 0.7 {
		t.Errorf("keeper move %v is not aimed at the trajectory meet point %v (dot %.2f)", in.Move, rp, d)
	}
}
