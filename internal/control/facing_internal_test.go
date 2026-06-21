package control

import (
	"math"
	"testing"

	"phootball/internal/config"
	"phootball/internal/geom"
	"phootball/internal/sim"
)

// Unit tests for the facing module (facing.go): the directional "face your run, turn to the ball
// only to receive" policy, the far-projected aim primitive, and the turn-rate cap. They pin the
// behavioural contract so the module can be refactored/tuned without silently changing how the AI
// points (which, under the directional model, is how fast it moves).

// faceFixture builds a 3-a-side match under the given move model and returns the AI + a perception
// for player 1, with the ball placed `ballGap` to the RIGHT of the player and at rest (so it is
// neither an incoming pass nor in trap-receive range when far).
func faceFixture(t *testing.T, model config.MoveModel, mePos, facing, ballPos geom.Vec) (*AI, perception) {
	t.Helper()
	cfg := config.Default()
	cfg.Tuning.MoveModel = model
	m := sim.BuildMatchFromConfig(sim.NewStandardField(), 3, cfg)
	me := m.Players[1]
	for _, q := range m.Players {
		if q != me {
			q.Position = geom.NewVec(q.Position.X, 40) // clear others away so they don't perturb perception
		}
	}
	me.Position = mePos
	me.Facing = geom.Unit(facing)
	m.Ball.Position = ballPos
	m.Ball.Velocity = geom.Vec{}
	ai := NewAISkill(me.PlayerID, SkillHard)
	return ai, perceive(m.View(), viewMe(m, me), 1.0/60)
}

// aimDir is the unit facing direction an Aim point encodes from the player.
func aimDir(p perception, aim geom.Vec) geom.Vec { return geom.Unit(aim.Sub(p.me.Position())) }

func dirsClose(a, b geom.Vec) bool { return geom.Dot(geom.Unit(a), geom.Unit(b)) > 0.999 }

// TestFaceAimStandardAlwaysFacesAction: under the Standard model facing is speed-neutral, so the
// off-ball policy must always face the action target (the pre-directional behaviour), regardless of
// the travel direction -- the standard path must never regress.
func TestFaceAimStandardAlwaysFacesAction(t *testing.T) {
	ai, p := faceFixture(t, config.MoveStandard, geom.NewVec(0, 0), geom.NewVec(1, 0), geom.NewVec(500, 0))
	in := sim.Intent{Move: geom.NewVec(0, 1), Throttle: 1} // travelling UP, ball to the RIGHT
	aim := ai.faceAim(p, in, p.ball)
	if !dirsClose(aimDir(p, aim), geom.NewVec(1, 0)) {
		t.Errorf("standard faceAim should face the ball (1,0), got %v", aimDir(p, aim))
	}
}

// TestFaceAimDirectionalTransitFacesTravel: under Directional, an off-ball player FAR from the
// action target (well beyond its turn-time lead) and moving with throttle must face its TRAVEL
// direction, so the directional curve grants forward speed instead of the side/back penalty.
func TestFaceAimDirectionalTransitFacesTravel(t *testing.T) {
	// Ball 500 to the right, player facing the ball (so the lead ~= faceActionGap, small), moving UP.
	ai, p := faceFixture(t, config.MoveDirectional, geom.NewVec(0, 0), geom.NewVec(1, 0), geom.NewVec(500, 0))
	in := sim.Intent{Move: geom.NewVec(0, 1), Throttle: 1}
	aim := ai.faceAim(p, in, p.ball)
	if !dirsClose(aimDir(p, aim), geom.NewVec(0, 1)) {
		t.Errorf("directional faceAim in transit should face travel (0,1), got %v", aimDir(p, aim))
	}
	if ai.faceActioning {
		t.Error("faceActioning should be false while transiting far from the action")
	}
}

// TestFaceAimDirectionalCloseFacesAction: within the turn-time lead of the action target the player
// must turn to face it (to be aligned for the touch) even while moving elsewhere.
func TestFaceAimDirectionalCloseFacesAction(t *testing.T) {
	// Ball only 100 to the right (inside the ~150 faceActionGap lead), still moving UP.
	ai, p := faceFixture(t, config.MoveDirectional, geom.NewVec(0, 0), geom.NewVec(1, 0), geom.NewVec(100, 0))
	in := sim.Intent{Move: geom.NewVec(0, 1), Throttle: 1}
	aim := ai.faceAim(p, in, p.ball)
	if !dirsClose(aimDir(p, aim), geom.NewVec(1, 0)) {
		t.Errorf("directional faceAim near the action should face it (1,0), got %v", aimDir(p, aim))
	}
	if !ai.faceActioning {
		t.Error("faceActioning should latch true when close enough to the action")
	}
}

// TestFaceAimHysteresis: once facing the action, the policy must keep facing it through a release
// BAND (until the gap exceeds lead*faceReleaseBand), so the travel<->action decision cannot
// flip-flop tick to tick and jitter the facing.
func TestFaceAimHysteresis(t *testing.T) {
	ai, p := faceFixture(t, config.MoveDirectional, geom.NewVec(0, 0), geom.NewVec(1, 0), geom.NewVec(100, 0))
	in := sim.Intent{Move: geom.NewVec(0, 1), Throttle: 1}
	ai.faceAim(p, in, p.ball) // latch faceActioning=true (gap 100 < lead ~150)
	if !ai.faceActioning {
		t.Fatal("precondition: should have latched faceActioning")
	}
	// Move the player so the gap is in the hysteresis band: lead < gap < lead*releaseBand. With the
	// player on the ball->target line (ang 0) the lead is ~faceActionGap (150) and the band is x1.5
	// (~225). A gap of 190 sits inside the band -> must STILL face the action.
	_, pBand := faceFixture(t, config.MoveDirectional, geom.NewVec(-90, 0), geom.NewVec(1, 0), geom.NewVec(100, 0))
	aim := ai.faceAim(pBand, in, pBand.ball) // gap now 190
	if !ai.faceActioning || !dirsClose(aimDir(pBand, aim), geom.NewVec(1, 0)) {
		t.Errorf("inside the release band the policy should hold the action facing (no flip-flop); faceActioning=%v dir=%v", ai.faceActioning, aimDir(pBand, aim))
	}
	// Now well beyond the band -> release to travel.
	_, pFar := faceFixture(t, config.MoveDirectional, geom.NewVec(-400, 0), geom.NewVec(1, 0), geom.NewVec(100, 0))
	aim = ai.faceAim(pFar, in, pFar.ball) // gap now 500 >> band
	if ai.faceActioning || !dirsClose(aimDir(pFar, aim), geom.NewVec(0, 1)) {
		t.Errorf("beyond the release band the policy should return to travel facing; faceActioning=%v dir=%v", ai.faceActioning, aimDir(pFar, aim))
	}
}

// TestAimTowardProjectsFar: aimToward points at the target direction from the player, projected far
// (aimProjectDist) so the facing stays stable as the player shuffles under reaction-delay caching.
func TestAimTowardProjectsFar(t *testing.T) {
	ai, p := faceFixture(t, config.MoveDirectional, geom.NewVec(10, 20), geom.NewVec(1, 0), geom.NewVec(500, 20))
	target := geom.NewVec(310, 420) // up and to the right
	aim := ai.aimToward(p, target)
	if !dirsClose(aimDir(p, aim), target.Sub(p.me.Position())) {
		t.Errorf("aimToward direction %v should match target direction %v", aimDir(p, aim), target.Sub(p.me.Position()))
	}
	if d := geom.Dist(aim, p.me.Position()); math.Abs(d-aimProjectDist) > 1 {
		t.Errorf("aimToward should project ~%d units, got %.1f", aimProjectDist, d)
	}
}

// TestCapAimRateLimited: capAim must rotate facing toward the aimed direction by at most maxTurnRad,
// so the disk can never snap-turn instantly (a human turns at a bounded rate).
func TestCapAimRateLimited(t *testing.T) {
	ai, p := faceFixture(t, config.MoveDirectional, geom.NewVec(0, 0), geom.NewVec(1, 0), geom.NewVec(500, 0))
	// Ask to face straight up (90deg from the current facing of (1,0)).
	in := sim.Intent{Aim: p.me.Position().Add(geom.NewVec(0, 1).Scale(aimProjectDist))}
	capped := ai.capAim(p, in)
	turned := geom.AngleBetween(p.me.Facing(), aimDir(p, capped.Aim))
	if turned > ai.tune.maxTurnRad+1e-6 {
		t.Errorf("capAim turned %.4f rad in one step, exceeds maxTurnRad %.4f", turned, ai.tune.maxTurnRad)
	}
	if turned < ai.tune.maxTurnRad-1e-6 {
		t.Errorf("capAim should turn the FULL maxTurnRad %.4f toward a 90deg target, only turned %.4f", ai.tune.maxTurnRad, turned)
	}
}
