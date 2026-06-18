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
	s.Shoot = CurveSpec{LinearCurve, 575, 172.5}                 // +15% power at max (was 500/150)
	s.Restitution = CurveSpec{InverseQuadraticCurve, 0.23, 0.24} // front 0.23: controlled front touch (still >0.20, so a full pass deflects off rather than sticking); back 0.24: springier behind. Buff/debuff multipliers unchanged -> buffed ~0.19, debuffed ~0.43, still tamed
	s.CaptureSpeed = CurveSpec{LinearCurve, 230, 30}             // baseline front 230 (left as-is): the buff endpoint (~236) is barely above it, so raising baseline would invert the capture buff; baseline capture is improved via restitution+control instead
	s.CenterPull = CurveSpec{InverseQuadraticCurve, 800, 0}      // power reduced (was 950)
	s.Stickiness = CurveSpec{InverseQuadraticCurve, 420, 30}     // front restored to 420; small baseline hold at the back (was 0)
	s.Control = CurveSpec{LinearCurve, 1850, 340}                // roll-to-front speed raised further (1700->1850) to help capture
	s.CaptureConeRadians = 0.3839724354387525                    // ~22deg, widened (bigger cone; was ~16deg)
	s.TrapPullBonus = 1.0                                        // reduced (was 1.5)
	s.TrapRangeBonus = 6                                         // reduced (was 10)
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
