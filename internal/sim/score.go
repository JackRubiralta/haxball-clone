package sim

// Side identifies one half of the pitch (and the goal on that side). SideNone means
// no goal was scored this tick.
type Side int

const (
	SideNone Side = iota
	SideLeft
	SideRight
)

// Opponent returns the other side.
func (s Side) Opponent() Side {
	switch s {
	case SideLeft:
		return SideRight
	case SideRight:
		return SideLeft
	default:
		return SideNone
	}
}
