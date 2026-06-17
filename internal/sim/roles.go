package sim

// Role identifies a player's position, which selects a stats preset and informs the
// AI's behaviour.
type Role int

const (
	RoleGoalkeeper Role = iota
	RoleMidfielder
	RoleStriker
)

// GoalkeeperStats: less bounce everywhere, a weaker centre pull (less sticky), a
// touch slower but larger to block shots, and a clearing rather than scoring shot.
func GoalkeeperStats() PlayerStats {
	s := DefaultStats(450)
	s.Radius = 22
	s.MaxSpeed = 126
	s.Acceleration = 340
	s.Restitution = CurveSpec{QuadraticCurve, 0.05, 0.35}
	s.CaptureSpeed = CurveSpec{LinearCurve, 360, 60}
	s.CenterPull = CurveSpec{QuadraticCurve, 500, 0}
	s.Stickiness = CurveSpec{InverseQuadraticCurve, 260, 0}
	s.Shoot = CurveSpec{LinearCurve, 450, 135}
	s.CaptureConeDegrees = 22 // a keeper has "safe hands": a wider reliable catch cone
	s.GripFloor = 0.45        // harder to dispossess a keeper holding the ball
	s.TrapPullBonus = 1.0     // keepers don't dribble-steal
	return s
}

// MidfielderStats: balanced, with a more powerful shot.
func MidfielderStats() PlayerStats {
	s := DefaultStats(620)
	s.MaxSpeed = 147
	s.Shoot = CurveSpec{LinearCurve, 620, 200}
	return s
}

// StrikerStats: fast, with stickier control for dribbling and a strong front shot.
func StrikerStats() PlayerStats {
	s := DefaultStats(560)
	s.MaxSpeed = 168
	s.Acceleration = 320
	s.CenterPull = CurveSpec{QuadraticCurve, 1200, 0}
	s.Stickiness = CurveSpec{InverseQuadraticCurve, 520, 0}
	s.Control = CurveSpec{LinearCurve, 1700, 350}
	s.PossessionBuildSeconds = 1.2 // settles possession faster
	s.TrapPullBonus = 1.8          // stronger steal pull
	s.TrapRangeBonus = 18          // longer steal reach
	return s
}

// StatsForRole returns the stats preset for a role.
func StatsForRole(r Role) PlayerStats {
	switch r {
	case RoleGoalkeeper:
		return GoalkeeperStats()
	case RoleStriker:
		return StrikerStats()
	default:
		return MidfielderStats()
	}
}
