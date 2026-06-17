package sim

// Role identifies a player's position, which selects a stats preset and informs the
// AI's behaviour.
type Role int

const (
	RoleGoalkeeper Role = iota
	RoleMidfielder
	RoleStriker
)

// fieldPlayerStats is the single preset every position currently uses. The user asked
// for all positions -- goalkeeper, midfielder, attacker (and a future defender) -- to
// play IDENTICALLY for now, based on the field player being tuned in solo (plain
// DefaultStats with shootForce 500). Each position-varying stat is written out and set
// to that field-player value, with the per-position value to restore later in the
// trailing comment, ordered [gk | mid | attack]. There is no separate defender role
// yet; a defender would currently resolve to this same preset.
func fieldPlayerStats() PlayerStats {
	s := DefaultStats(500)
	s.Radius = 18                                                // [gk 22 | mid 18 | attack 18]
	s.MaxSpeed = 140                                             // [gk 126 | mid 147 | attack 168]
	s.Acceleration = 300                                         // [gk 340 | mid 300 | attack 320]
	s.Shoot = CurveSpec{LinearCurve, 500, 150}                   // [gk 450/135 | mid 620/200 | attack 560/168]
	s.Restitution = CurveSpec{InverseQuadraticCurve, 0.08, 0.35} // [gk {Quad 0.05/0.35} | mid+attack {InvQuad 0.08/0.35}]
	s.CaptureSpeed = CurveSpec{LinearCurve, 280, 70}             // front lowered (was 320): the capture cone helps less
	s.CenterPull = CurveSpec{InverseQuadraticCurve, 950, 0}      // front nerfed a touch (was 1000)
	s.Stickiness = CurveSpec{InverseQuadraticCurve, 420, 30}     // front restored to 420; small baseline hold at the back (was 0)
	s.Control = CurveSpec{LinearCurve, 1500, 300}                // [gk+mid {Lin 1500/300} | attack {Lin 1700/350}]
	s.CaptureConeRadians = 0.3839724354387525                    // ~22deg, widened (bigger cone; was ~16deg)
	s.TrapPullBonus = 1.5                                        // [gk 1.0 | mid 1.5 | attack 1.8]
	s.TrapRangeBonus = 10                                        // [gk 10 | mid 10 | attack 18]
	s.PossessionBuildSeconds = 1.5                               // [gk 1.5 | mid 1.5 | attack 1.2]
	return s
}

// GoalkeeperStats, MidfielderStats and StrikerStats all currently return the same shared
// field-player preset (see fieldPlayerStats), so every position plays identically for
// now. Re-differentiate the positions by restoring the per-position values noted there.
func GoalkeeperStats() PlayerStats { return fieldPlayerStats() }
func MidfielderStats() PlayerStats { return fieldPlayerStats() }
func StrikerStats() PlayerStats    { return fieldPlayerStats() }

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
