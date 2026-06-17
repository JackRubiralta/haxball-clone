package sim

import "phootball/internal/config"

func NewDefaultConfigForProbe() config.Config { return config.Default() }

func firstOnP(m *Match, side Side) *Player {
	for _, p := range m.Players {
		if p.Team.Side == side {
			return p
		}
	}
	return nil
}
func secondOnP(m *Match, side Side) *Player {
	seen := false
	for _, p := range m.Players {
		if p.Team.Side == side {
			if seen {
				return p
			}
			seen = true
		}
	}
	return nil
}
