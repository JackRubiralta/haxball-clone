// collision.go
package main

import "math"

// checkDiscCollision checks if two discs are colliding.
func checkDiscCollision(disc1, disc2 *Disc) bool {
	distance := Dist(disc1.Position, disc2.Position)
	return distance < (disc1.Radius + disc2.Radius)
}

// resolveCollision resolves the collision between two discs by updating their velocities.
func handleDiscToDiscCollision(disc1, disc2 *Disc) {
	if checkDiscCollision(disc1, disc2) {
		// Calculate the normal vector
		normal := disc2.Position.Sub(disc1.Position)
		distance := Norm(normal)
		if distance == 0 {
			return // Avoid division by zero
		}

		overlap := (disc1.Radius + disc2.Radius) - distance
		normal = normal.Mul(1 / distance)

		// Move discs apart based on their masses
		disc1.Position = disc1.Position.Sub(normal.Mul(overlap / 2))
		disc2.Position = disc2.Position.Add(normal.Mul(overlap / 2))

		// Adjust velocities to reflect pushing each other
		relativeVelocity := disc2.Velocity.Sub(disc1.Velocity)
		velocityAlongNormal := Dot(relativeVelocity, normal)

		if velocityAlongNormal > 0 {
			return
		}

		restitution := 0.0 // No bounce
		impulseScalar := -(1 + restitution) * velocityAlongNormal
		impulseScalar /= 1/disc1.Mass + 1/disc2.Mass

		impulse := normal.Mul(impulseScalar)

		disc1.Velocity = disc1.Velocity.Sub(impulse.Mul(1 / disc1.Mass))
		disc2.Velocity = disc2.Velocity.Add(impulse.Mul(1 / disc2.Mass))
	}
}

// handlePlayerToPlayerCollision handles collision between two players.
func handlePlayerToPlayerCollision(player1, player2 *Player) {
	handleDiscToDiscCollision(player1.Disc, player2.Disc)
}

func handleBallToBoxCollision(ball *Ball, box *Box) {
	if goal := box.CheckGoal(ball); goal != 0 {
		// Handle goal
		if goal == 1 {
			println("Goal for the left side!")
		} else if goal == 2 {
			println("Goal for the right side!")
		}
		// Reset ball position
		ball.Position = NewVec(400, 300)
		ball.Velocity = NewVec(0, 0)
	}

	if ball.Left() < box.Position.X {
		ball.Position.X = box.Position.X + ball.Radius
		ball.Velocity.X = -ball.Velocity.X
	} else if ball.Right() > box.Position.X+box.Width {
		ball.Position.X = box.Position.X + box.Width - ball.Radius
		ball.Velocity.X = -ball.Velocity.X
	}

	if ball.Top() < box.Position.Y {
		ball.Position.Y = box.Position.Y + ball.Radius
		ball.Velocity.Y = -ball.Velocity.Y
	} else if ball.Bottom() > box.Position.Y+box.Height {
		ball.Position.Y = box.Position.Y + box.Height - ball.Radius
		ball.Velocity.Y = -ball.Velocity.Y
	}
}

// handlePlayerToBoxCollision handles collision between the player and the box.
func handlePlayerToBoxCollision(player *Player, box *Box) {
	if player.Left() < box.Position.X {
		player.Position.X = box.Position.X + player.Radius
		player.Velocity.X = 0
	} else if player.Right() > box.Position.X+box.Width {
		player.Position.X = box.Position.X + box.Width - player.Radius
		player.Velocity.X = 0
	}

	if player.Top() < box.Position.Y {
		player.Position.Y = box.Position.Y + player.Radius
		player.Velocity.Y = 0
	} else if player.Bottom() > box.Position.Y+box.Height {
		player.Position.Y = box.Position.Y + box.Height - player.Radius
		player.Velocity.Y = 0
	}
}

// ballAngleDegrees returns the angle between the player's facing direction and the
// direction from the player to the ball: 0 = dead in front, 180 = directly behind.
// normal must be a unit vector pointing from the player to the ball.
func ballAngleDegrees(normal, facing Vec) float64 {
	cos := Dot(normal, facing)
	if cos > 1 {
		cos = 1
	} else if cos < -1 {
		cos = -1
	}
	return math.Acos(cos) * 180 / math.Pi
}

// handleBallToPlayerInteraction resolves everything between the ball and a player
// except shooting: the attraction that lets the player dribble, and the contact,
// which either sticks the ball or bounces it off.
//
// The ball never moves the player -- the player is immovable in this collision, so
// only the ball is affected. A contact slower than the (angle-dependent) capture
// speed is absorbed completely, so the ball sticks on the first touch instead of
// bouncing a few times before it settles; a faster contact bounces off with the
// angle's restitution.
func handleBallToPlayerInteraction(ball *Ball, player *Player, deltaTime float64) {
	toBall := ball.Position.Sub(player.Position)
	distance := Norm(toBall)
	if distance == 0 {
		return // sharing the same centre; nothing sensible to resolve
	}
	normal := toBall.Mul(1 / distance) // points from the player to the ball

	// One-way dribble forces: these only ever change the ball's velocity.
	handleBallToPlayerAttraction(ball, player, deltaTime)

	overlap := (player.Radius + ball.Radius) - distance
	if overlap > 0 {
		angle := ballAngleDegrees(normal, player.Facing)

		// Push only the ball out of the overlap -- the player is never moved by the contact.
		ball.Position = ball.Position.Add(normal.Mul(overlap))

		// Decide between sticking and bouncing from the impact speed. Below the capture
		// speed the inbound velocity is absorbed completely (restitution 0), so the ball
		// sticks first time; above it the ball bounces with the angle's restitution.
		relativeNormal := Dot(ball.Velocity.Sub(player.Velocity), normal)
		if relativeNormal < 0 {
			approachSpeed := -relativeNormal
			restitution := 0.0
			captureSpeed := player.CaptureSpeedCurve(player.FrontCaptureSpeed, player.BackCaptureSpeed, 0, 180, angle)
			if approachSpeed > captureSpeed {
				restitution = player.RestitutionCurve(player.FrontRestitution, player.BackRestitution, 0, 180, angle)
			}
			ball.Velocity = ball.Velocity.Sub(normal.Mul((1 + restitution) * relativeNormal))
		}
	}
}

// handleBallToPlayerAttraction applies the two dribbling forces to the ball (and
// only the ball). Force 1 is a radial pull toward the player centre that reaches the
// ball within PullRange -- stronger the closer it is and stronger toward the front --
// which holds the ball so it sticks. Force 2 is a tangential pull, active only while
// touching, that rolls the ball around to the front (0 deg); its length is sin(angle),
// so it eases to nothing right at the front.
func handleBallToPlayerAttraction(ball *Ball, player *Player, deltaTime float64) {
	toBall := ball.Position.Sub(player.Position)
	distance := Norm(toBall)
	if distance == 0 {
		return
	}
	normal := toBall.Mul(1 / distance)
	gap := distance - player.Radius - ball.Radius
	cos := Dot(normal, player.Facing)
	if cos > 1 {
		cos = 1
	} else if cos < -1 {
		cos = -1
	}
	angle := math.Acos(cos) * 180 / math.Pi

	// Force 1: a spring pull toward the player centre, scaled by the distance from the
	// SURFACE (the gap -- not the centre distance). It is zero at the surface and grows
	// with the gap, so it draws a drifting ball back in but never shoves a settled ball
	// into the player (that constant inward push was what caused the jitter). PullRange
	// is the reach.
	if gap > 0 && gap < player.PullRange {
		strength := player.CenterPullCurve(player.FrontCenterPull, player.BackCenterPull, 0, 180, angle)
		ball.Velocity = ball.Velocity.Add(normal.Mul(-strength * (gap / player.PullRange) * deltaTime))
	}

	// Within the control zone: ease the INWARD radial speed only (so the ball settles
	// instead of slamming in and bouncing; outward motion like a shot is never damped),
	// and carry the ball with the player's INPUT acceleration so it keeps pace.
	// player.Acceleration is set only by Move (WASD); a collision changes the player's
	// velocity directly, not its acceleration, so a knocked player never drags the ball.
	if gap < player.PullRange {
		radialVel := Dot(ball.Velocity.Sub(player.Velocity), normal)
		if radialVel < 0 {
			d := player.ApproachDamping * deltaTime
			if d > 1 {
				d = 1
			}
			ball.Velocity = ball.Velocity.Sub(normal.Mul(radialVel * d))
		}
		ball.Velocity = ball.Velocity.Add(player.Acceleration.Mul(deltaTime))
	}

	// Force 2: tangential roll to the front, only while the ball is ACTUALLY touching.
	if gap < player.TouchRange {
		strength := player.ControlCurve(player.FrontControl, player.BackControl, 0, 180, angle)
		tangential := player.Facing.Sub(normal.Mul(cos))
		ball.Velocity = ball.Velocity.Add(tangential.Mul(strength * deltaTime))

		// Damp the sideways (orbital) velocity so the ball settles at the front instead
		// of oscillating, while leaving the radial direction untouched.
		relative := ball.Velocity.Sub(player.Velocity)
		sideways := relative.Sub(normal.Mul(Dot(relative, normal)))
		damping := player.ControlDamping * deltaTime
		if damping > 1 {
			damping = 1
		}
		ball.Velocity = ball.Velocity.Sub(sideways.Mul(damping))
	}
}

// shoot kicks the ball in the direction the player is facing if the ball is close
// enough to be under the player's control. The power comes from the player's
// ShootCurve over the ball's angle, so a shot struck from the front is stronger than
// one nudged from behind.
func shoot(player *Player, ball *Ball) {
	toBall := ball.Position.Sub(player.Position)
	distance := Norm(toBall)
	gap := distance - player.Radius - ball.Radius
	if gap >= player.TouchRange {
		return
	}

	angle := 0.0 // dead front if the ball sits exactly on the player
	if distance > 0 {
		angle = ballAngleDegrees(toBall.Mul(1/distance), player.Facing)
	}

	power := player.ShootCurve(player.FrontShootForce, player.BackShootForce, 0, 180, angle)
	ball.Velocity = ball.Velocity.Add(player.Facing.Mul(power))
}
