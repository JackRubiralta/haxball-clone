package control

import (
	"testing"

	"phootball/internal/config"
	"phootball/internal/geom"
	"phootball/internal/sim"
)

// TestReceivePointOnTrajectory checks the receiver meets an incoming pass ON the ball's
// predicted path (collinear with ball + ballVel, ahead of the ball) and at a point where the
// ball has slowed to a controllable speed -- so it glides onto the trajectory for a clean
// touch rather than charging the fast ball head-on.
func TestReceivePointOnTrajectory(t *testing.T) {
	field := sim.NewStandardField()
	m := sim.BuildMatchFromConfig(field, 3, config.Default())
	me := m.Players[1]
	me.Position = geom.NewVec(540, 240)
	// A pass played along +X, above the control speed at launch but slowing as it rolls, so by
	// the time it reaches the receiver (who can stand still and let it come) it is controllable.
	m.Ball.Position = geom.NewVec(300, 240)
	m.Ball.Velocity = geom.NewVec(300, 0)
	ai := NewAISkill(me.PlayerID, SkillHard)
	p := perceive(m.View(), viewMe(m, me), 1.0/60)

	pt := ai.receivePoint(p)

	// On the trajectory: the meeting point lies along the ball's velocity ray from the ball.
	rel := pt.Sub(p.ball)
	if geom.Norm(rel) < 1 {
		t.Fatalf("receive point %v sits on top of the ball -- expected a point ahead on the path", pt)
	}
	if cos := geom.Dot(geom.Unit(rel), geom.Unit(p.ballVel)); cos < 0.999 {
		t.Errorf("receive point not on the ball's trajectory: cos(angle to ballVel)=%.4f (want ~1)", cos)
	}

	// Controllable: the ball's speed where we meet it is at/under the control threshold (it has
	// slowed), unless the path never slowed enough in range (then it is the earliest reachable).
	along := geom.Norm(rel)
	tMeet := ballTravelTime(along, geom.Norm(p.ballVel), p.friction)
	if cs := ai.receiveControlSpeed(p); ballSpeedAt(p.ballVel, tMeet, p.friction, p.dt) > cs+5 {
		t.Errorf("met the ball at speed %.0f > control speed %.0f -- not a clean reception", ballSpeedAt(p.ballVel, tMeet, p.friction, p.dt), cs)
	}

	// It must still be reachable: we don't run to a point farther than the earliest interceptable
	// one would be unreachable -- sanity that the point is in front of us along +X (toward the ball).
	if pt.X <= p.ball.X {
		t.Errorf("receive point %v is not ahead of the ball on its path", pt)
	}
}

// TestReceivingPassGate checks the gate that distinguishes an incoming pass to glide onto from
// a near-stopped loose ball, a firmly-held ball, or a contested 50/50.
func TestReceivingPassGate(t *testing.T) {
	setup := func() (*AI, *sim.Match, *sim.Player) {
		field := sim.NewStandardField()
		m := sim.BuildMatchFromConfig(field, 3, config.Default())
		me := m.Players[1] // left side
		me.Position = geom.NewVec(440, 240)
		// Park everyone else far, so by default the ball is uncontested and ours to collect.
		for _, q := range m.Players {
			if q != me {
				q.Position = geom.NewVec(-1e5, float64(q.PlayerID)*40)
				q.Velocity = geom.Vec{}
			}
		}
		m.Ball.Position = geom.NewVec(640, 240) // clearly loose (well beyond my touch range)
		m.Ball.Velocity = geom.NewVec(-250, 0)  // a played ball rolling toward me, I'm nearest
		return NewAISkill(me.PlayerID, SkillHard), m, me
	}

	// A loose, moving, uncontested ball our side is nearest to -> receiving a pass.
	ai, m, me := setup()
	p := perceive(m.View(), viewMe(m, me), 1.0/60)
	if !ai.receivingPass(p) {
		t.Errorf("a loose, fast, uncontested ball we are nearest to should read as an incoming pass")
	}

	// A near-stopped ball is a loose ball to win, not a pass to glide onto.
	ai, m, me = setup()
	m.Ball.Velocity = geom.NewVec(20, 0)
	p = perceive(m.View(), viewMe(m, me), 1.0/60)
	if ai.receivingPass(p) {
		t.Errorf("a near-stopped ball should NOT read as an incoming pass")
	}

	// An opponent right on the ball makes it contested -> win it at the earliest point, not glide.
	ai, m, me = setup()
	for _, q := range m.Players {
		if q.Team.Side != me.Team.Side {
			q.Position = m.Ball.Position // an opponent sitting on the ball
			break
		}
	}
	p = perceive(m.View(), viewMe(m, me), 1.0/60)
	if ai.receivingPass(p) {
		t.Errorf("a contested ball should NOT read as an incoming pass (win it, don't dawdle)")
	}
}
