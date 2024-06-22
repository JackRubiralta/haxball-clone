// collision.go
package main

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
		impulseScalar /= 1/disc1.Radius + 1/disc2.Radius

		impulse := normal.Mul(impulseScalar)

		disc1.Velocity = disc1.Velocity.Sub(impulse.Mul(1 / disc1.Radius))
		disc2.Velocity = disc2.Velocity.Add(impulse.Mul(1 / disc2.Radius))
	}
}

// handleBallPlayerCollision handles collision between the ball and a player.
func handleBallToPlayerCollision(ball *Ball, player *Player) {
	handleDiscToDiscCollision(player.Disc, ball.Disc)
}

// handlePlayerPlayerCollision handles collision between two players.
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

// handlePlayerBoxCollision handles collision between the player and the box.
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

// attractBall applies an attraction force to the ball if it is within the player's attraction radius.
func attractBall(player *Player, ball *Ball) {
	distanceToBall := Dist(player.Position, ball.Position) - ball.Radius
	if distanceToBall < player.AttractionRadius {
		// Apply a slight attraction force to the ball
		attractionForce := player.Position.Sub(ball.Position).Mul(0.003) // Arbitrary small force value
		ball.Velocity = ball.Velocity.Add(attractionForce)
	}
}

// shoot shoots the ball if it is within the player's attraction radius.
func shoot(player *Player, ball *Ball) {
	distanceToBall := Dist(player.Position, ball.Position) - ball.Radius
	if distanceToBall < player.AttractionRadius {
		// Apply a shooting force to the ball
		shootingForce := ball.Position.Sub(player.Position).Mul(player.ShootForce) // Arbitrary shooting force value
		ball.Velocity = ball.Velocity.Add(shootingForce)
	}
}
