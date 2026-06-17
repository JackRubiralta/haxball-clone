package sim

import (
	"testing"

	"phootball/internal/config"
	"phootball/internal/geom"
)

// TestBallNotWedgedIntoCorner: a player pinning the ball against the arena corner must not be
// able to push it through the walls. The ball-player contact (and dribble pull) shoves the ball
// toward the corner every tick; resolveInteractions must re-confine it so it never ends a tick
// penetrating the corner. Regression test for the ball getting wedged into the wall corner.
func TestBallNotWedgedIntoCorner(t *testing.T) {
	m := BuildMatchFromConfig(NewStandardField(), 1, config.Default())
	f := m.Field
	r := m.Ball.Radius()
	p := m.Players[0]

	// Park everyone else at the field centre, clear of the top-left corner.
	cx, cy := (f.Min.X+f.Max.X)/2, (f.Min.Y+f.Max.Y)/2
	for _, q := range m.Players {
		if q != p {
			q.Position = geom.NewVec(cx, cy)
		}
	}

	// Player just inside the top-left corner, facing into it, with the ball pinned between it and
	// the corner (already biting past the edge). The contact overlap-resolution pushes the ball
	// toward the corner each tick.
	p.Position = geom.NewVec(f.Min.X+25, f.Min.Y+25)
	p.Facing = geom.Unit(geom.NewVec(-1, -1))
	m.Ball.Position = geom.NewVec(f.Min.X+5, f.Min.Y+5)

	const eps = 0.01
	for i := 0; i < 60; i++ {
		m.resolveInteractions(1.0 / 60)
		b := m.Ball.Position
		if b.X < f.Min.X+r-eps || b.Y < f.Min.Y+r-eps {
			t.Fatalf("ball wedged into the corner at iter %d: pos=%v, corner edge (%.2f, %.2f)",
				i, b, f.Min.X+r, f.Min.Y+r)
		}
	}
}

// TestPlayerNotOverlappingPinnedBallInCorner: a player pinning the ball into a corner must not
// end up OVERLAPPING it. The ball is confined against the two walls (it can't move), so the
// player has to be pushed out of the overlap -- it can't sit on top of the ball. The ball must
// still stay inside the arena.
func TestPlayerNotOverlappingPinnedBallInCorner(t *testing.T) {
	m := BuildMatchFromConfig(NewStandardField(), 1, config.Default())
	f := m.Field
	br := m.Ball.Radius()
	p := m.Players[0]

	cx, cy := (f.Min.X+f.Max.X)/2, (f.Min.Y+f.Max.Y)/2
	for _, q := range m.Players {
		if q != p {
			q.Position = geom.NewVec(cx, cy)
		}
	}

	// Player driving the ball into the top-left corner every tick.
	p.Position = geom.NewVec(f.Min.X+25, f.Min.Y+25)
	p.Facing = geom.Unit(geom.NewVec(-1, -1))
	m.Ball.Position = geom.NewVec(f.Min.X+5, f.Min.Y+5)

	const eps = 0.01
	minSep := p.Radius() + br
	for i := 0; i < 120; i++ {
		// Re-assert the inward push every tick (a human/AI holding the stick into the corner).
		p.Velocity = geom.NewVec(-200, -200)
		m.resolveInteractions(1.0 / 60)

		if d := geom.Dist(p.Position, m.Ball.Position); d < minSep-eps {
			t.Fatalf("player overlaps the pinned ball at iter %d: dist=%.2f, need >= %.2f", i, d, minSep)
		}
		b := m.Ball.Position
		if b.X < f.Min.X+br-eps || b.Y < f.Min.Y+br-eps {
			t.Fatalf("ball pushed outside the arena at iter %d: pos=%v", i, b)
		}
	}
}
