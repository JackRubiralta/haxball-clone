package sim

import (
	"math"
	"testing"

	"phootball/internal/config"
	"phootball/internal/geom"
)

// fireOffCentre shoots a ball straight along -x at the stationary, +x-facing player, offset
// perpendicularly by impact parameter b (0 = dead-on). It steps the real per-tick physics so a
// fast ball penetrates naturally, then returns the contact position angle and whether the ball
// ended up SEATED on the player (stayed within reach at low speed) versus glancing/bouncing away.
func fireOffCentre(b, speed float64) (contactAngleDeg float64, seated bool) {
	const dt = 1.0 / 60
	p := NewPlayer(1, geom.NewVec(0, 0), config.DefaultPlayerTuning(), &Team{Side: SideLeft})
	p.Facing = geom.NewVec(1, 0) // facing the incoming ball

	const ballRadius = 7.5
	R := p.Radius() + ballRadius
	ball := NewBall(geom.NewVec(R+45, b), ballRadius)
	ball.Velocity = geom.NewVec(-speed, 0)

	contacted := false
	for tick := 0; tick < 90; tick++ {
		ball.Update(dt)
		if !contacted {
			toBall := ball.Position.Sub(p.Position)
			if d := geom.Norm(toBall); d > 0 && (p.Radius()+ball.Radius())-d > 0 {
				contacted = true
				contactAngleDeg = ballAngle(toBall.Scale(1/d), p.Facing) * 180 / math.Pi
			}
		}
		handleBallToPlayerInteraction(ball, p, dt)
	}
	// After settling: a SEATED ball is held at the (stationary) player's surface, so it is still
	// within the pull radius and barely moving; a ball that glanced or bounced has departed.
	gap := geom.Norm(ball.Position.Sub(p.Position)) - p.Radius() - ball.Radius()
	seated = gap < p.Tuning.PullRange && geom.Norm(ball.Velocity) < 80
	return contactAngleDeg, seated
}

// TestOffCentreCaptureSeatsInCone is the regression for the off-centre capture bug: a ball caught
// anywhere INSIDE the reliable capture cone seats on the player exactly like a dead-on catch,
// instead of keeping its sideways (tangential) glide and sliding off. Before the fix, only a
// perfectly dead-on catch stuck; a ball arriving a touch off-centre -- still well within the cone --
// bounced/slid out because the capture only cancelled the inbound NORMAL velocity and left the
// tangential momentum untouched.
func TestOffCentreCaptureSeatsInCone(t *testing.T) {
	s := config.DefaultPlayerTuning()
	coneEdgeDeg := s.CaptureCone(0, 0) * 180 / math.Pi
	speed := s.Shoot.Front * 0.7 // the middle-click push speed (~402), well under the front capture

	// Sweep impact parameters whose contact lands inside the cone. Every one must seat, like dead-on.
	for _, b := range []float64{0, 4, 8, 12, 16, 20} {
		ang, seated := fireOffCentre(b, speed)
		if ang > coneEdgeDeg {
			t.Fatalf("test setup: b=%.0f contacts at %.1fdeg, past the %.1fdeg cone -- pick a smaller offset", b, ang, coneEdgeDeg)
		}
		if !seated {
			t.Errorf("ball caught off-centre at %.1fdeg (b=%.0f, inside the %.1fdeg cone) should SEAT like a dead-on catch, but it escaped",
				ang, b, coneEdgeDeg)
		}
	}
}

// TestPastConeContactStillEscapes guards the other side of the fix: the in-cone tangential
// absorption must NOT magnetise a ball in once the contact is PAST the cone. A glancing contact
// beyond the cone edge keeps its sideways momentum and slides off, so the player cannot vacuum up
// balls clipping its side -- the cone still bounds where a catch sticks.
func TestPastConeContactStillEscapes(t *testing.T) {
	s := config.DefaultPlayerTuning()
	coneEdgeDeg := s.CaptureCone(0, 0) * 180 / math.Pi
	speed := s.Shoot.Front * 0.7

	// A large offset makes the ball clip the player's side, contacting well past the cone edge.
	ang, seated := fireOffCentre(24.5, speed)
	if ang <= coneEdgeDeg {
		t.Fatalf("test setup: contact at %.1fdeg is not past the %.1fdeg cone -- use a larger offset", ang, coneEdgeDeg)
	}
	if seated {
		t.Errorf("a glancing contact past the cone (%.1fdeg, cone %.1fdeg) should slide off, not be captured", ang, coneEdgeDeg)
	}
}
