package sim

import "image/color"

// Team is one side of the match: a colour, a roster, and a running score. Side
// determines which goal the team defends (its own) and attacks (the other).
type Team struct {
	Side    Side
	Name    string
	Color   color.RGBA
	Players []*Player
	Score   int
}
