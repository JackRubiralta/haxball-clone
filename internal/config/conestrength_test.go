package config

import (
	"math"
	"testing"
)

// TestConeQuantitiesFlatInsideCone locks the design invariant shared by every cone-gated ball
// quantity: it is at FULL strength (its Front peak) EVERYWHERE inside its cone, then tapers only
// PAST the cone edge. Stickiness / Control / CenterPull get this for free because their evaluators
// take the cone edge as the curve start (so curveProgress is 0 throughout [0, coneEdge]). Capture
// speed enforces the same flat-in-cone shape in handleBallToPlayerInteraction (see
// TestOffAxisCaptureInCone) -- a ball that sticks dead-on must stick anywhere in the cone.
func TestConeQuantitiesFlatInsideCone(t *testing.T) {
	s := DefaultPlayerTuning()
	const coneEdge = 0.4 // a representative cone half-angle (radians)
	just := coneEdge + 0.1

	cases := []struct {
		name  string
		at    func(coneEdge, angle float64) float64
		front float64
	}{
		{"Stickiness", s.StickinessAt, s.Stickiness.Front},
		{"Control", s.ControlAt, s.Control.Front},
		{"CenterPull", s.CenterPullAt, s.CenterPull.Front},
	}
	for _, c := range cases {
		for _, a := range []float64{0, coneEdge / 2, coneEdge} {
			if got := c.at(coneEdge, a); math.Abs(got-c.front) > 1e-9 {
				t.Errorf("%s at angle %.3f (inside cone) = %.4f, want flat front peak %.4f", c.name, a, got, c.front)
			}
		}
		// Just past the cone edge it must have started tapering -- strictly below the front peak.
		if got := c.at(coneEdge, just); !(got < c.front) {
			t.Errorf("%s just past the cone (angle %.3f) = %.4f, should be below the front peak %.4f", c.name, just, got, c.front)
		}
	}
}
