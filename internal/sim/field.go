package sim

import (
	"phootball/internal/geom"
	"phootball/internal/physics"
)

// Field is the arena: a rectangular play area with a goal opening on the left and
// right sides, a kickoff spot at the centre, and any fixed obstacles.
type Field struct {
	Min        geom.Vec // top-left corner of the play area
	Max        geom.Vec // bottom-right corner
	GoalWidth  float64  // how far each goal pocket extends past the side wall
	GoalHeight float64  // height of the goal mouth
	CenterSpot geom.Vec
	LeftGoal   *Goal
	RightGoal  *Goal
	Obstacles  []*Obstacle
}

// GoalPostRadius is the radius of the solid goal posts.
const GoalPostRadius = 6

// NewStandardField builds the default pitch in fixed logical coordinates, so the
// world (and therefore a client's aim) is identical on every machine.
func NewStandardField() *Field {
	return NewField(geom.NewVec(60, 100), geom.NewVec(940, 580), 40, 100)
}

// NewField builds a field spanning min..max with a goal of the given mouth size on
// each side.
func NewField(min, max geom.Vec, goalWidth, goalHeight float64) *Field {
	center := min.Add(max).Scale(0.5)
	f := &Field{
		Min:        min,
		Max:        max,
		GoalWidth:  goalWidth,
		GoalHeight: goalHeight,
		CenterSpot: center,
	}
	top, bot := f.goalMouthRange()
	leftBack := min.X - goalWidth
	f.LeftGoal = &Goal{
		Side:   SideLeft,
		Center: geom.NewVec(min.X, center.Y),
		Mouth:  physics.Segment{A: geom.NewVec(min.X, top), B: geom.NewVec(min.X, bot)},
		Posts: [2]*physics.Body{
			physics.NewStaticCircle(geom.NewVec(min.X, top), GoalPostRadius),
			physics.NewStaticCircle(geom.NewVec(min.X, bot), GoalPostRadius),
		},
		Net: []*physics.Body{
			physics.NewStaticSegment(geom.NewVec(leftBack, top), geom.NewVec(leftBack, bot)), // back
			physics.NewStaticSegment(geom.NewVec(leftBack, top), geom.NewVec(min.X, top)),     // top
			physics.NewStaticSegment(geom.NewVec(leftBack, bot), geom.NewVec(min.X, bot)),     // bottom
		},
	}
	rightBack := max.X + goalWidth
	f.RightGoal = &Goal{
		Side:   SideRight,
		Center: geom.NewVec(max.X, center.Y),
		Mouth:  physics.Segment{A: geom.NewVec(max.X, top), B: geom.NewVec(max.X, bot)},
		Posts: [2]*physics.Body{
			physics.NewStaticCircle(geom.NewVec(max.X, top), GoalPostRadius),
			physics.NewStaticCircle(geom.NewVec(max.X, bot), GoalPostRadius),
		},
		Net: []*physics.Body{
			physics.NewStaticSegment(geom.NewVec(rightBack, top), geom.NewVec(rightBack, bot)), // back
			physics.NewStaticSegment(geom.NewVec(max.X, top), geom.NewVec(rightBack, top)),       // top
			physics.NewStaticSegment(geom.NewVec(max.X, bot), geom.NewVec(rightBack, bot)),       // bottom
		},
	}
	return f
}

// Goals returns both goals.
func (f *Field) Goals() [2]*Goal {
	return [2]*Goal{f.LeftGoal, f.RightGoal}
}

// Width returns the play-area width.
func (f *Field) Width() float64 { return f.Max.X - f.Min.X }

// Height returns the play-area height.
func (f *Field) Height() float64 { return f.Max.Y - f.Min.Y }

// goalMouthRange returns the top and bottom Y of the goal openings.
func (f *Field) goalMouthRange() (top, bot float64) {
	top = f.Min.Y + (f.Height()-f.GoalHeight)/2
	bot = f.Min.Y + (f.Height()+f.GoalHeight)/2
	return top, bot
}

// GoalOn returns the goal on the given side.
func (f *Field) GoalOn(side Side) *Goal {
	if side == SideLeft {
		return f.LeftGoal
	}
	return f.RightGoal
}

// AddObstacle places a fixed obstacle (such as a cone) on the field.
func (f *Field) AddObstacle(o *Obstacle) { f.Obstacles = append(f.Obstacles, o) }

// CheckGoal reports which goal the ball has fully crossed the line of (the whole
// ball is past the goal line, between the posts), or SideNone.
func (f *Field) CheckGoal(ball *Ball) Side {
	top, bot := f.goalMouthRange()
	if ball.Position.Y <= top || ball.Position.Y >= bot {
		return SideNone
	}
	if ball.Right() < f.Min.X {
		return SideLeft
	}
	if ball.Left() > f.Max.X {
		return SideRight
	}
	return SideNone
}

// ConfineBall bounces the ball off the arena walls. The left and right walls open
// at the goal mouth so the ball can enter the goal; the top and bottom always
// reflect.
func (f *Field) ConfineBall(ball *Ball) {
	r := ball.Radius()
	top, bot := f.goalMouthRange()
	inMouth := ball.Position.Y > top && ball.Position.Y < bot

	if !inMouth {
		if ball.Position.X-r < f.Min.X {
			ball.Position.X = f.Min.X + r
			ball.Velocity.X = -ball.Velocity.X
		} else if ball.Position.X+r > f.Max.X {
			ball.Position.X = f.Max.X - r
			ball.Velocity.X = -ball.Velocity.X
		}
	}
	if ball.Position.Y-r < f.Min.Y {
		ball.Position.Y = f.Min.Y + r
		ball.Velocity.Y = -ball.Velocity.Y
	} else if ball.Position.Y+r > f.Max.Y {
		ball.Position.Y = f.Max.Y - r
		ball.Velocity.Y = -ball.Velocity.Y
	}
}

// ConfinePlayer keeps a player inside the playable area by EDGE-clamping it against
// every solid boundary, so its drawn edge always rests exactly on the surface and can
// never overlap or penetrate it. This is the universal rule -- it covers the pitch
// walls AND, once a player has stepped through a goal mouth, the net box (back wall and
// net top/bottom) -- so the goal net behaves just like the walls. The mouth itself
// stays open so the player can come and go.
func (f *Field) ConfinePlayer(p *Player) {
	r := p.Radius()
	top, bot := f.goalMouthRange()

	// Top and bottom of the pitch always block (these never coincide with a goal).
	if p.Top() < f.Min.Y {
		p.Position.Y = f.Min.Y + r
		p.Velocity.Y = 0
	} else if p.Bottom() > f.Max.Y {
		p.Position.Y = f.Max.Y - r
		p.Velocity.Y = 0
	}

	switch {
	case p.Position.X < f.Min.X: // inside the left goal
		f.confineToNet(p, f.Min.X-f.GoalWidth, true, top, bot, r)
	case p.Position.X > f.Max.X: // inside the right goal
		f.confineToNet(p, f.Max.X+f.GoalWidth, false, top, bot, r)
	default: // on the pitch: side walls block except across the open goal mouth
		if p.Position.Y <= top || p.Position.Y >= bot {
			if p.Left() < f.Min.X {
				p.Position.X = f.Min.X + r
				p.Velocity.X = 0
			} else if p.Right() > f.Max.X {
				p.Position.X = f.Max.X - r
				p.Velocity.X = 0
			}
		}
	}
}

// confineToNet edge-clamps a player that is behind a goal line inside the net box: the
// back wall (at backX) and the net top/bottom (the mouth height). The mouth side, back
// toward the pitch, is left open. leftGoal selects which side the back wall sits on.
// Because it clamps the player's EDGE (never its centre), the player rests flush on the
// net surface and never penetrates -- so it can never overlap the net frame.
func (f *Field) confineToNet(p *Player, backX float64, leftGoal bool, top, bot, r float64) {
	if leftGoal {
		if p.Left() < backX {
			p.Position.X = backX + r
			p.Velocity.X = 0
		}
	} else {
		if p.Right() > backX {
			p.Position.X = backX - r
			p.Velocity.X = 0
		}
	}
	if p.Top() < top {
		p.Position.Y = top + r
		p.Velocity.Y = 0
	} else if p.Bottom() > bot {
		p.Position.Y = bot - r
		p.Velocity.Y = 0
	}
}
