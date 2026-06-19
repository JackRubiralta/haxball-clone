package neural

import (
	"math"

	"phootball/internal/control"
	"phootball/internal/geom"
	"phootball/internal/policy"
	"phootball/internal/sim"
)

// decode argmax-selects each head's index, then maps the indices to a sim.Intent. Used at
// runtime; the env (cmd/env) bypasses argmax and supplies Python's chosen indices directly via
// decodeIndices / ActFromIndices.
func (c *Controller) decode(logits []float32, me sim.SelfView) sim.Intent {
	off := c.headOff
	idx := [5]int{
		policy.Argmax(logits[off[0]:off[1]]),
		policy.Argmax(logits[off[1]:off[2]]),
		policy.Argmax(logits[off[2]:off[3]]),
		policy.Argmax(logits[off[3]:off[4]]),
		policy.Argmax(logits[off[4]:off[5]]),
	}
	return c.decodeIndices(me, idx)
}

// decodeIndices turns the five factored head indices into a sim.Intent. The egoframe (set by
// build/FeaturizeFlat) maps the egocentric move direction back to world space; the aim is
// RELATIVE to current facing and bounded to +/- AimArcMax, so a snap-turn is structurally
// impossible. Cancel is masked unless a shot charge is live.
func (c *Controller) decodeIndices(me sim.SelfView, idx [5]int) sim.Intent {
	var in sim.Intent

	// Move + throttle.
	if idx[0] == IdleMove {
		in.Move = geom.Vec{}
		in.Throttle = 0
	} else {
		ang := float64(idx[0]) * (2 * math.Pi / float64(MoveDirBins))
		dir := c.frame.xhat.Scale(math.Cos(ang)).Add(c.frame.yhat.Scale(math.Sin(ang)))
		in.Move = geom.Unit(dir)
		switch idx[1] {
		case 0:
			in.Throttle = 0
		case 1:
			in.Throttle = 0.5
		default:
			in.Throttle = 1
		}
	}

	// Aim: relative to current facing, within +/- AimArcMax (cell-centered bins).
	facing := me.Facing()
	if facing == (geom.Vec{}) {
		facing = c.frame.xhat
	}
	rel := -AimArcMax + 2*AimArcMax*(float64(idx[2])+0.5)/float64(AimBins)
	aimDir := facing.Rotate(rel, geom.Vec{})
	in.Aim = me.Position().Add(aimDir.Scale(control.AimProjectDist))

	// Ability.
	switch idx[3] {
	case AbilShoot:
		in.ShootHeld = true
	case AbilTrap:
		in.Trap = true
	case AbilPush:
		in.Push = true
	}

	// Cancel: only meaningful while a shot charge is live (the sim only honors a cancel while
	// shoot reads held, so we assert ShootHeld with CancelCharge).
	if idx[4] == 1 && me.ShootCharge() > 0 {
		in.ShootHeld = true
		in.CancelCharge = true
	}
	return in
}
