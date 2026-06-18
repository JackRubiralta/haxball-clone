package sim

import (
	"phootball/internal/config"
	"phootball/internal/geom"
	"phootball/internal/physics"
)

// Field is the arena: a rectangular play area with a goal opening on the left and
// right sides, a kickoff spot at the centre, and any fixed obstacles. It carries the
// config.Geometry it was built from, so the renderer and the positional rules read box
// sizes and markings from the same single source of truth.
type Field struct {
	Geo        config.Geometry // the dimensions this field was built from
	Min        geom.Vec        // top-left corner of the play area
	Max        geom.Vec        // bottom-right corner
	GoalWidth  float64         // how far each goal pocket extends past the side wall
	GoalHeight float64         // height of the goal mouth
	CenterSpot geom.Vec
	LeftGoal   *Goal
	RightGoal  *Goal
	Obstacles  []*Obstacle // fixed obstacles; no mode adds any today
}

// GoalPostRadius is the radius of the solid goal posts.
const GoalPostRadius = 6

// NewStandardField builds the default pitch in fixed logical coordinates, so the
// world (and therefore a client's aim) is identical on every machine.
func NewStandardField() *Field {
	return NewFieldFromGeometry(config.StandardGeometry())
}

// NewField builds a field spanning min..max with a goal of the given mouth size on each
// side. It reconstructs the equivalent geometry (centring the play area in a surface
// with matching margins) so every field, however it is made, still carries a complete
// config.Geometry; box markings default to the standard sizes.
func NewField(min, max geom.Vec, goalWidth, goalHeight float64) *Field {
	g := config.StandardGeometry()
	g.Name = "custom"
	g.PlayWidth = max.X - min.X
	g.PlayHeight = max.Y - min.Y
	g.ScreenWidth = g.PlayWidth + 2*min.X
	g.ScreenHeight = g.PlayHeight + 2*min.Y
	g.GoalPocketDepth = goalWidth
	g.GoalMouthWidth = goalHeight
	return NewFieldFromGeometry(g)
}

// NewFieldFromGeometry builds a field from a geometry description -- the single source
// of truth for every pitch dimension. The geometry is normalised first so a partially
// specified config is completed sensibly.
func NewFieldFromGeometry(g config.Geometry) *Field {
	g = g.Normalize()
	min, max := g.Min(), g.Max()
	center := g.Center()
	f := &Field{
		Geo:        g,
		Min:        min,
		Max:        max,
		GoalWidth:  g.GoalPocketDepth,
		GoalHeight: g.GoalMouthWidth,
		CenterSpot: center,
	}
	top, bot := f.goalMouthRange()
	post := g.PostRadius
	leftBack := min.X - g.GoalPocketDepth
	f.LeftGoal = &Goal{
		Side:   SideLeft,
		Center: geom.NewVec(min.X, center.Y),
		Mouth:  physics.Segment{A: geom.NewVec(min.X, top), B: geom.NewVec(min.X, bot)},
		Posts: [2]*physics.Body{
			physics.NewStaticCircle(geom.NewVec(min.X, top), post),
			physics.NewStaticCircle(geom.NewVec(min.X, bot), post),
		},
		Net: []*physics.Body{
			physics.NewStaticSegment(geom.NewVec(leftBack, top), geom.NewVec(leftBack, bot)), // back
			physics.NewStaticSegment(geom.NewVec(leftBack, top), geom.NewVec(min.X, top)),    // top
			physics.NewStaticSegment(geom.NewVec(leftBack, bot), geom.NewVec(min.X, bot)),    // bottom
		},
	}
	rightBack := max.X + g.GoalPocketDepth
	f.RightGoal = &Goal{
		Side:   SideRight,
		Center: geom.NewVec(max.X, center.Y),
		Mouth:  physics.Segment{A: geom.NewVec(max.X, top), B: geom.NewVec(max.X, bot)},
		Posts: [2]*physics.Body{
			physics.NewStaticCircle(geom.NewVec(max.X, top), post),
			physics.NewStaticCircle(geom.NewVec(max.X, bot), post),
		},
		Net: []*physics.Body{
			physics.NewStaticSegment(geom.NewVec(rightBack, top), geom.NewVec(rightBack, bot)), // back
			physics.NewStaticSegment(geom.NewVec(max.X, top), geom.NewVec(rightBack, top)),     // top
			physics.NewStaticSegment(geom.NewVec(max.X, bot), geom.NewVec(rightBack, bot)),     // bottom
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

// PenaltyArea returns the outer penalty box rectangle for the goal on the given side.
func (f *Field) PenaltyArea(side Side) config.Rect {
	return f.boxRect(side, f.Geo.PenaltyWidth, f.Geo.PenaltyDepth)
}

// GoalArea returns the inner goal-area ("six-yard") box rectangle for the given side.
func (f *Field) GoalArea(side Side) config.Rect {
	return f.boxRect(side, f.Geo.GoalAreaWidth, f.Geo.GoalAreaDepth)
}

// boxRect builds a box of the given across-pitch width and into-pitch depth, anchored
// on the goal line of the given side.
func (f *Field) boxRect(side Side, width, depth float64) config.Rect {
	cy := f.CenterSpot.Y
	if side == SideLeft {
		return config.Rect{Min: geom.NewVec(f.Min.X, cy-width/2), Max: geom.NewVec(f.Min.X+depth, cy+width/2)}
	}
	return config.Rect{Min: geom.NewVec(f.Max.X-depth, cy-width/2), Max: geom.NewVec(f.Max.X, cy+width/2)}
}

// PenaltySpot returns the penalty spot for the given side, midway between the goal-area
// edge and the penalty-area edge.
func (f *Field) PenaltySpot(side Side) geom.Vec {
	spotD := (f.Geo.GoalAreaDepth + f.Geo.PenaltyDepth) / 2
	if side == SideLeft {
		return geom.NewVec(f.Min.X+spotD, f.CenterSpot.Y)
	}
	return geom.NewVec(f.Max.X-spotD, f.CenterSpot.Y)
}

// CenterCircleRadius returns the centre-circle radius from the geometry.
func (f *Field) CenterCircleRadius() float64 { return f.Geo.CenterCircleRadius }

// GoalAreaBox returns the inner goal-area box as a ZoneRect for the positional rules, or
// a degenerate (empty) rect when the goal area is disabled, so a cap on a non-existent
// box is a no-op.
func (f *Field) GoalAreaBox(side Side) ZoneRect {
	if !f.Geo.HasGoalArea {
		return ZoneRect{}
	}
	r := f.GoalArea(side)
	return ZoneRect{Min: r.Min, Max: r.Max}
}

// PenaltyAreaBox returns the outer penalty box as a ZoneRect for the positional rules,
// or a degenerate rect when the penalty area is disabled.
func (f *Field) PenaltyAreaBox(side Side) ZoneRect {
	if !f.Geo.HasPenaltyArea {
		return ZoneRect{}
	}
	r := f.PenaltyArea(side)
	return ZoneRect{Min: r.Min, Max: r.Max}
}

// OffsideLineX returns the anti-camp line for a team, measured as a fraction of the
// pitch from that team's own goal toward the opponent. A left-attacking team is held
// below the line; a right-attacking team above it.
func (f *Field) OffsideLineX(attacking Side, frac float64) float64 {
	if attacking == SideLeft {
		return f.Min.X + frac*f.Width()
	}
	return f.Max.X - frac*f.Width()
}

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

// PitchLineWidth is the world-space width of every painted pitch line -- the perimeter, the goal
// line, the penalty/goal boxes and the centre markings all share it, so the pitch lines are
// uniform. It lives here, in the simulation, because goal detection depends on it: a goal counts
// only once the whole ball has fully cleared the DRAWN goal line (not merely the mouth), and the
// renderer reads this same value so the white line the player sees is exactly the line the ball
// must visibly cross.
const PitchLineWidth = 3.0

// CheckGoal reports which goal the ball has fully crossed the line of -- the whole ball is past the
// FAR edge of the drawn goal line (so it is visibly entirely over the white line), between the
// posts -- or SideNone. The goal line is painted just inside each mouth (its inner edge on the goal
// line at Min.X / Max.X, extending PitchLineWidth into the net), so the ball must clear that full
// width to score.
func (f *Field) CheckGoal(ball *Ball) Side {
	top, bot := f.goalMouthRange()
	if ball.Position.Y <= top || ball.Position.Y >= bot {
		return SideNone
	}
	if ball.Right() < f.Min.X-PitchLineWidth {
		return SideLeft
	}
	if ball.Left() > f.Max.X+PitchLineWidth {
		return SideRight
	}
	return SideNone
}

// ConfineBall bounces the ball off the arena walls. The left and right walls open at
// the goal mouth so the ball can enter the goal; the top and bottom always reflect. It
// returns the speed of the strongest wall impact this call (0 if the ball did not hit a
// wall), so the caller can play a ball-hit sound scaled by the impact.
func (f *Field) ConfineBall(ball *Ball, wallRestitution float64) float64 {
	r := ball.Radius()
	top, bot := f.goalMouthRange()
	inMouth := ball.Position.Y > top && ball.Position.Y < bot
	impact := 0.0
	hit := func(speed float64) {
		if speed > impact {
			impact = speed
		}
	}

	// Reflect off each wall, but only the velocity component pointing INTO it, and keep
	// just wallRestitution of it (the wall absorbs the rest) so the ball comes off a
	// touch slower each time instead of bouncing forever.
	if !inMouth {
		if ball.Position.X-r < f.Min.X {
			ball.Position.X = f.Min.X + r
			if ball.Velocity.X < 0 {
				hit(-ball.Velocity.X)
				ball.Velocity.X = -wallRestitution * ball.Velocity.X
			}
		} else if ball.Position.X+r > f.Max.X {
			ball.Position.X = f.Max.X - r
			if ball.Velocity.X > 0 {
				hit(ball.Velocity.X)
				ball.Velocity.X = -wallRestitution * ball.Velocity.X
			}
		}
	}
	if ball.Position.Y-r < f.Min.Y {
		ball.Position.Y = f.Min.Y + r
		if ball.Velocity.Y < 0 {
			hit(-ball.Velocity.Y)
			ball.Velocity.Y = -wallRestitution * ball.Velocity.Y
		}
	} else if ball.Position.Y+r > f.Max.Y {
		ball.Position.Y = f.Max.Y - r
		if ball.Velocity.Y > 0 {
			hit(ball.Velocity.Y)
			ball.Velocity.Y = -wallRestitution * ball.Velocity.Y
		}
	}
	return impact
}

// ConfinePlayer keeps a player inside the playable area by EDGE-clamping it against
// every solid boundary, so its drawn edge always rests exactly on the surface and can
// never overlap or penetrate it. This is the universal rule -- it covers the pitch
// walls AND, once a player has stepped through a goal mouth, the net box (back wall and
// net top/bottom) -- so the goal net behaves just like the walls. The mouth itself
// stays open so the player can come and go. A player BOUNCES off these surfaces, keeping
// playerWallRestitution of the speed it carried into them (the rest is absorbed) instead
// of dead-stopping, so hitting a wall costs real momentum.
func (f *Field) ConfinePlayer(p *Player, wallRestitution float64) {
	r := p.Radius()
	top, bot := f.goalMouthRange()

	// Top and bottom of the pitch always block (these never coincide with a goal).
	if p.Top() < f.Min.Y {
		p.Position.Y = f.Min.Y + r
		if p.Velocity.Y < 0 {
			p.Velocity.Y = -wallRestitution * p.Velocity.Y
		}
	} else if p.Bottom() > f.Max.Y {
		p.Position.Y = f.Max.Y - r
		if p.Velocity.Y > 0 {
			p.Velocity.Y = -wallRestitution * p.Velocity.Y
		}
	}

	switch {
	case p.Position.X < f.Min.X: // inside the left goal
		f.confineToNet(p, f.Min.X-f.GoalWidth, true, top, bot, r, wallRestitution)
	case p.Position.X > f.Max.X: // inside the right goal
		f.confineToNet(p, f.Max.X+f.GoalWidth, false, top, bot, r, wallRestitution)
	default: // on the pitch: side walls block except across the open goal mouth
		if p.Position.Y <= top || p.Position.Y >= bot {
			if p.Left() < f.Min.X {
				p.Position.X = f.Min.X + r
				if p.Velocity.X < 0 {
					p.Velocity.X = -wallRestitution * p.Velocity.X
				}
			} else if p.Right() > f.Max.X {
				p.Position.X = f.Max.X - r
				if p.Velocity.X > 0 {
					p.Velocity.X = -wallRestitution * p.Velocity.X
				}
			}
		}
	}
}

// confineToNet edge-clamps a player that is behind a goal line inside the net box: the
// back wall (at backX) and the net top/bottom (the mouth height). The mouth side, back
// toward the pitch, is left open. leftGoal selects which side the back wall sits on.
// Because it clamps the player's EDGE (never its centre), the player rests flush on the
// net surface and never penetrates -- so it can never overlap the net frame.
func (f *Field) confineToNet(p *Player, backX float64, leftGoal bool, top, bot, r, wallRestitution float64) {
	if leftGoal {
		if p.Left() < backX {
			p.Position.X = backX + r
			if p.Velocity.X < 0 {
				p.Velocity.X = -wallRestitution * p.Velocity.X
			}
		}
	} else {
		if p.Right() > backX {
			p.Position.X = backX - r
			if p.Velocity.X > 0 {
				p.Velocity.X = -wallRestitution * p.Velocity.X
			}
		}
	}
	if p.Top() < top {
		p.Position.Y = top + r
		if p.Velocity.Y < 0 {
			p.Velocity.Y = -wallRestitution * p.Velocity.Y
		}
	} else if p.Bottom() > bot {
		p.Position.Y = bot - r
		if p.Velocity.Y > 0 {
			p.Velocity.Y = -wallRestitution * p.Velocity.Y
		}
	}
}
