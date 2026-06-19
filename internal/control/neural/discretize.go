package neural

import (
	"math"

	"phootball/internal/geom"
	"phootball/internal/sim"
)

// Discretize maps a teacher's continuous sim.Intent to the factored discrete head indices
// [MoveDir, Throttle, AimBin, Ability, Cancel], the labels datagen writes alongside each obs.
// It is the inverse of decode: Decode(Discretize(intent)) reproduces the intent semantically
// (up to bin resolution). The egoframe is recomputed from the view so this is a pure function
// of (view, me, intent) and needs no controller state.
func Discretize(view sim.View, me sim.SelfView, in sim.Intent) [5]int {
	f := makeFrame(view, me)
	var out [5]int

	// Move direction (egocentric) + idle.
	if geom.Norm(in.Move) < 1e-6 || in.Throttle <= 0 {
		out[0] = IdleMove
	} else {
		lx := geom.Dot(in.Move, f.xhat)
		ly := geom.Dot(in.Move, f.yhat)
		ang := math.Atan2(ly, lx)
		bin := int(math.Round(ang / (2 * math.Pi / float64(MoveDirBins))))
		out[0] = ((bin % MoveDirBins) + MoveDirBins) % MoveDirBins
	}

	out[1] = throttleBin(in.Throttle)

	// Relative aim bin.
	facing := me.Facing()
	desired := in.Aim.Sub(me.Position())
	if geom.Norm(desired) < 1e-6 || geom.Norm(facing) < 1e-6 {
		out[2] = aimBinForRel(0)
	} else {
		out[2] = aimBinForRel(geom.SignedAngle(facing, desired))
	}

	// Ability, matching enforceAbilityExclusivity precedence (Trap > Push > Shoot).
	switch {
	case in.Trap:
		out[3] = AbilTrap
	case in.Push:
		out[3] = AbilPush
	case in.ShootHeld:
		out[3] = AbilShoot
	default:
		out[3] = AbilNone
	}

	if in.CancelCharge {
		out[4] = 1
	}
	return out
}

func throttleBin(t float64) int {
	switch {
	case t < 0.25:
		return 0
	case t < 0.75:
		return 1
	default:
		return 2
	}
}

// aimBinForRel clamps a relative angle to +/- AimArcMax and maps it to the nearest cell-centered
// AimBin (bin i center = -AimArcMax + 2*AimArcMax*(i+0.5)/AimBins).
func aimBinForRel(rel float64) int {
	if rel > AimArcMax {
		rel = AimArcMax
	}
	if rel < -AimArcMax {
		rel = -AimArcMax
	}
	i := int(math.Round((rel+AimArcMax)*float64(AimBins)/(2*AimArcMax) - 0.5))
	if i < 0 {
		i = 0
	}
	if i >= AimBins {
		i = AimBins - 1
	}
	return i
}
