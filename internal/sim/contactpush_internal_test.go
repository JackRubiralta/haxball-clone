package sim

import (
	"testing"

	"phootball/internal/geom"
)

// TestHardHitPushesPlayer: a really hard ball impact shoves the player back (along the ball's
// travel), scaled so a harder hit pushes more, while a dribble/soft-speed contact never moves
// the player and a ball moving AWAY (a shot leaving the foot) doesn't either.
func TestHardHitPushesPlayer(t *testing.T) {
	const ballR = 10.0

	// hit fires a ball straight at a stationary player (facing +x) at the given impact speed and
	// returns the player's velocity after the contact.
	hit := func(impact float64) geom.Vec {
		p := NewPlayer(1, geom.NewVec(0, 0), DefaultPlayerTuning(500), &Team{Side: SideLeft})
		p.Facing = geom.NewVec(1, 0)
		b := NewBall(geom.NewVec(p.Radius()+ballR-0.5, 0), ballR) // overlapping in front
		b.Velocity = geom.NewVec(-impact, 0)                      // straight at the player (-x travel)
		handleBallToPlayerInteraction(b, p, 1.0/60)
		return p.Velocity
	}

	// A hard hit knocks the player back along the ball's travel (-x here).
	if v := hit(600); !(v.X < -1) {
		t.Errorf("a hard hit should push the player back (vx < 0), got %.2f", v.X)
	}
	// A soft / dribble-speed contact (below ballPushThreshold) leaves the player planted.
	if v := hit(100); v != (geom.Vec{}) {
		t.Errorf("a soft contact should not move the player, got %v", v)
	}
	// Mass/speed means something: a harder hit pushes more.
	if !(geom.Norm(hit(700)) > geom.Norm(hit(400))) {
		t.Errorf("a harder hit should push the player more: |v(700)|=%.2f |v(400)|=%.2f", geom.Norm(hit(700)), geom.Norm(hit(400)))
	}

	// A ball moving AWAY from the player (e.g. just shot) does not shove it.
	p := NewPlayer(1, geom.NewVec(0, 0), DefaultPlayerTuning(500), &Team{Side: SideLeft})
	p.Facing = geom.NewVec(1, 0)
	b := NewBall(geom.NewVec(p.Radius()+ballR-0.5, 0), ballR)
	b.Velocity = geom.NewVec(600, 0) // moving +x, away from the player
	handleBallToPlayerInteraction(b, p, 1.0/60)
	if p.Velocity != (geom.Vec{}) {
		t.Errorf("a ball moving away should not push the player, got %v", p.Velocity)
	}
}
