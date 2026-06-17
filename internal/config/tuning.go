package config

// Tuning collects the physics constants the simulation reads, so a match can be
// retuned from one place instead of through scattered package constants. It records the
// ball's body, how much energy the walls and goal frame absorb on a bounce, and the
// obstacle/net give. The simulation begins consuming these values once the rules layer
// is wired in; the defaults reproduce today's hand-tuned constants.
type Tuning struct {
	BallRadius   float64
	BallFriction float64
	BallMass     float64

	BallWallRestitution   float64 // speed the ball keeps off a pitch wall / goal frame
	PlayerWallRestitution float64 // speed a player keeps off a wall (damped harder)
	ObstacleRestitution   float64 // bounce off fixed cone obstacles
	NetRestitution        float64 // low, so the net catches the ball instead of springing it
}

// DefaultTuning returns the baseline physics values that match the original game.
func DefaultTuning() Tuning {
	return Tuning{
		BallRadius:            7.5,
		BallFriction:          -0.3,
		BallMass:              1.5,
		BallWallRestitution:   0.90,
		PlayerWallRestitution: 0.50,
		ObstacleRestitution:   0.5,
		NetRestitution:        0.2,
	}
}
