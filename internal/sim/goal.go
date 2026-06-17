package sim

import (
	"phootball/internal/geom"
	"phootball/internal/physics"
)

// Goal is one of the two goals. Mouth is the goal line the ball must fully cross;
// Center is the aim point an attacker (or AI) shoots at; Posts are the two solid,
// immovable posts at the ends of the mouth that the ball and players bounce off.
type Goal struct {
	Side   Side
	Center geom.Vec
	Mouth  physics.Segment
	Posts  [2]*physics.Body // solid posts at the ends of the mouth
	Net    []*physics.Body  // back, top and bottom of the net (solid segments)
}
