package sim

import (
	"math"

	"phootball/internal/config"
	"phootball/internal/geom"
)

// moveRelativeToFacing rotates a screen-frame WASD vector (where W = (0,-1) "up") into world space
// so that "up" points along facing: W drives forward (toward facing), S backward, and A/D strafe.
// This is the "heading-locked" human control scheme. The transform is an orthonormal rotation
// (determinant +1), so the move magnitude is preserved; a zero move or facing is returned unchanged.
func moveRelativeToFacing(move, facing geom.Vec) geom.Vec {
	if move == (geom.Vec{}) || facing == (geom.Vec{}) {
		return move
	}
	f := geom.Unit(facing)
	// Maps (0,-1) -> f and (1,0) -> right-of-f (in the Y-down world frame).
	return geom.NewVec(-move.X*f.Y-move.Y*f.X, move.X*f.X-move.Y*f.Y)
}

// directionalSpeedMul returns the speed/acceleration multiplier for moving in `move` while facing
// `facing` under a directional movement model: full ahead, easing to MoveSide at 90deg off facing
// and MoveBack at 180deg (straight back), interpolated linearly through those three anchors. It
// returns 1 when there is no movement or facing is unknown, so an idle or just-spawned player is
// never penalised. Callers apply it only when Tuning.MoveModel != MoveStandard.
func directionalSpeedMul(move, facing geom.Vec, t config.Tuning) float64 {
	if geom.Norm(move) == 0 || geom.Norm(facing) == 0 {
		return 1
	}
	cos := geom.Dot(geom.Unit(move), geom.Unit(facing))
	switch {
	case cos > 1:
		cos = 1
	case cos < -1:
		cos = -1
	}
	angle := math.Acos(cos) // 0 = straight ahead, pi = straight back
	if angle <= math.Pi/2 {
		x := angle / (math.Pi / 2)
		return t.MoveForward + (t.MoveSide-t.MoveForward)*x
	}
	x := (angle - math.Pi/2) / (math.Pi / 2)
	return t.MoveSide + (t.MoveBack-t.MoveSide)*x
}
