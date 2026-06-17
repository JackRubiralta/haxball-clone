package control

import (
	"testing"

	"phootball/internal/geom"
)

// TestInterceptTurnRateAware proves the AI's interception accounts for the player's
// turn-rate limit: two identical movers equidistant from the same ball reach it at
// different estimated times depending on which way they are already heading -- the one
// pointed away is slower because it must spend time turning before it can close. With the
// turn penalty disabled (zero turn rate or zero/absent heading) the estimate is direction
// independent, which keeps kickoff election (heading is zero then) unchanged.
func TestInterceptTurnRateAware(t *testing.T) {
	tune := defaultAITuning()
	const (
		maxSpeed = 140.0
		turnRate = 14.0
		reach    = 20.0
		friction = -0.3
		dt       = 1.0 / 60.0
	)
	from := geom.NewVec(0, 0)
	ballPos := geom.NewVec(100, 0) // 100 away; a still ball so the target is fixed
	ballVel := geom.NewVec(0, 0)

	toward := geom.NewVec(1, 0) // already pointed at the ball: no turn needed
	away := geom.NewVec(-1, 0)  // pointed directly away: a full half-turn to close

	tToward := interceptTime(from, maxSpeed, turnRate, toward, reach, ballPos, ballVel, friction, dt, tune)
	tAway := interceptTime(from, maxSpeed, turnRate, away, reach, ballPos, ballVel, friction, dt, tune)
	if !(tAway > tToward) {
		t.Errorf("turn-rate awareness failed: mover pointed away (%.3fs) is not slower than one pointed at the ball (%.3fs)", tAway, tToward)
	}

	// Zero turn rate disables the penalty -> direction independent.
	if a, b := interceptTime(from, maxSpeed, 0, away, reach, ballPos, ballVel, friction, dt, tune),
		interceptTime(from, maxSpeed, 0, toward, reach, ballPos, ballVel, friction, dt, tune); a != b {
		t.Errorf("turnRate=0 should disable the penalty, but away=%.3f != toward=%.3f", a, b)
	}

	// A zero heading (no committed direction, e.g. at kickoff) also applies no penalty, so
	// it matches the penalty-free estimate -- this is why kickoff election is unchanged.
	zero := interceptTime(from, maxSpeed, turnRate, geom.Vec{}, reach, ballPos, ballVel, friction, dt, tune)
	noPenalty := interceptTime(from, maxSpeed, 0, toward, reach, ballPos, ballVel, friction, dt, tune)
	if zero != noPenalty {
		t.Errorf("zero heading should apply no penalty (got %.3f, want %.3f)", zero, noPenalty)
	}
}
