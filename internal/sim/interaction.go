package sim

import (
	"math"

	"phootball/internal/geom"
)

// ballAngle returns the angle in radians between the player's facing direction and the
// direction from the player to the ball: 0 = dead in front, pi = directly behind.
// normal must be a unit vector pointing from the player to the ball.
func ballAngle(normal, facing geom.Vec) float64 {
	cos := geom.Dot(normal, facing)
	if cos > 1 {
		cos = 1
	} else if cos < -1 {
		cos = -1
	}
	return math.Acos(cos)
}

// handleBallToPlayerInteraction resolves everything between the ball and a player
// except shooting: the attraction that lets the player dribble, and the contact,
// which either sticks the ball or bounces it off.
//
// The ball never moves the player -- the player is immovable in this interaction, so
// only the ball is affected (this path is deliberately never routed through the
// generic physics resolver). A contact slower than the angle-dependent capture speed
// is absorbed completely, so the ball sticks on the first touch instead of bouncing a
// few times before it settles; a faster contact bounces off with the angle's
// restitution.
// It returns whether the ball was actually in contact this tick (a touch), and the
// approach speed of a hard bounce (0 for a soft capture), so the caller can record the
// touch for scoring and play a ball-hit sound scaled by the impact.
func handleBallToPlayerInteraction(ball *Ball, player *Player, deltaTime float64) (touched bool, bounce float64) {
	toBall := ball.Position.Sub(player.Position)
	distance := geom.Norm(toBall)
	if distance == 0 {
		return false, 0 // sharing the same centre; nothing sensible to resolve
	}
	normal := toBall.Scale(1 / distance) // points from the player to the ball

	// One-way dribble forces: these only ever change the ball's velocity.
	handleBallToPlayerAttraction(ball, player, deltaTime)

	overlap := (player.Radius() + ball.Radius()) - distance
	if overlap > 0 {
		angle := ballAngle(normal, player.Facing)

		// Push only the ball out of the overlap -- the player is never moved.
		ball.Position = ball.Position.Add(normal.Scale(overlap))

		// Decide between sticking and bouncing from the impact speed. Below the
		// capture speed the inbound velocity is absorbed completely (restitution 0),
		// so the ball sticks first time; above it the ball bounces with the angle's
		// restitution.
		relativeNormal := geom.Dot(ball.Velocity.Sub(player.Velocity), normal)
		if relativeNormal < 0 {
			approachSpeed := -relativeNormal

			// Touch quality: the team possession charge scales how cleanly this contact is
			// taken (published per-tick into player.touchCoef). A clean touch (the owning team
			// receiving/carrying its built-up possession) absorbs at a higher speed and bounces
			// off less; the conceding team's touch (an opponent blocking a shot) captures less
			// and springs off harder, so the ball flies further. Coefficient 0 = baseline.
			quality := player.touchCoef

			// Cone factor: 1 inside the reliable capture cone, ramping to 0 over the
			// soft falloff past it. Outside the cone the effective capture speed drops
			// to the side/back floor, so the ball bounces off; trapping raises it back.
			// The team possession buff widens the cone slightly for the owning team (and
			// narrows it for the conceding team) via the touch coefficient.
			coneRadians := player.Stats.captureConeRadians(quality)
			cone := 1.0
			if over := angle - coneRadians; over > 0 {
				if player.Stats.CaptureConeSoft <= 0 {
					cone = 0
				} else if cone = 1 - over/player.Stats.CaptureConeSoft; cone < 0 {
					cone = 0
				}
			}

			side := player.Stats.CaptureSpeed.Back
			captureSpeed := side + (player.Stats.CaptureSpeed.Eval(angle)-side)*cone
			captureSpeed *= player.Stats.TouchQuality.captureMul(quality)
			captureSpeed += player.Stats.TrapCaptureBonus * player.trapCharge

			restitution := 0.0
			if approachSpeed > captureSpeed {
				// Bounce livelier the further off-front it is; a held trap deadens the bounce
				// (scaled by TrapRestitutionFactor, fully killing it before a full trap), and
				// the touch quality scales it too -- a clean touch deadens it, a cold one livens it.
				trapDeaden := 1 - math.Min(1, player.trapCharge*player.Stats.TrapRestitutionFactor)
				restitution = player.Stats.Restitution.Eval(angle) * (1 + (1 - cone)) * trapDeaden
				restitution *= player.Stats.TouchQuality.restitutionMul(quality)
				if restitution > 0.95 {
					restitution = 0.95
				}
				bounce = approachSpeed // a hard bounce, not a soft capture
			}

			// Collision mass: in a real collision a stationary ball (mass m_b) struck by
			// the player (mass m_p) only takes on a fraction m_p/(m_p+m_b) of the impulse,
			// so a heavier ball is harder to launch by bumping it. That ratio is applied to
			// the contact impulse, but ONLY off-front: inside the capture cone (cone->1) the
			// full impulse is kept so a front touch still absorbs cleanly and sticks
			// first-time; off-front (cone->0), where a lively bounce would otherwise FLING
			// the ball, the mass ratio takes over (heavier ball = less fling off a back/side
			// bump). The player is still never moved (only the ball's velocity changes).
			massRatio := player.Stats.Mass / (player.Stats.Mass + ball.Mass())
			impulseScale := massRatio + (1-massRatio)*cone
			ball.Velocity = ball.Velocity.Sub(normal.Scale((1 + restitution) * relativeNormal * impulseScale))
		}
		return true, bounce
	}
	return false, 0
}

// handleBallToPlayerAttraction applies the dribbling forces to the ball (and only the
// ball):
//   - Centre-pull: while the ball is near but not yet touching, a gap-scaled spring
//     draws it toward the player so it makes contact.
//   - Sticky hold: while the ball is touching, its separation from the player is
//     resisted up to a capped, angle-dependent stickiness, so the ball clings to the
//     surface until a strong enough push (a shot or a bump) overcomes it and frees it.
//   - Control: while touching, a tangential pull rolls the ball around to the front.
//
// Inward-only approach damping and sideways control damping let the ball settle
// without slamming in or orbiting.
func handleBallToPlayerAttraction(ball *Ball, player *Player, deltaTime float64) {
	toBall := ball.Position.Sub(player.Position)
	distance := geom.Norm(toBall)
	if distance == 0 {
		return
	}
	normal := toBall.Scale(1 / distance)
	gap := distance - player.Radius() - ball.Radius()
	cos := geom.Dot(normal, player.Facing)
	if cos > 1 {
		cos = 1
	} else if cos < -1 {
		cos = -1
	}
	angle := math.Acos(cos) // radians

	// Possession grip, split so possession affects the two hold forces only mildly and in
	// OPPOSITE directions: the centre-pull rises a little with possession (most of it is
	// always present -- CenterPullGripFloor is high), while stickiness is, if anything, very
	// slightly REDUCED by possession (StickinessPossessionDebuff). Plus the trap modifiers
	// (a held trap strengthens and lengthens the centre-pull).
	centerPullGrip := player.Stats.centerPullGrip(player.possession)
	stickinessGrip := player.Stats.stickinessGrip(player.possession)
	trapPullMul := 1 + player.Stats.TrapPullBonus*player.trapCharge
	pullRange := player.Stats.PullRange + player.Stats.TrapRangeBonus*player.trapCharge

	// Centre-pull: a gap-scaled spring toward the player centre, active only while the
	// ball is near but NOT yet touching, scaled by the centre-pull grip and the trap. It
	// draws a drifting (or an opponent's loose) ball in to make contact; once the ball is
	// touching, the sticky hold below takes over instead.
	if gap >= player.Stats.TouchRange && gap < pullRange {
		strength := player.Stats.CenterPull.Eval(angle) * centerPullGrip * trapPullMul
		ball.Velocity = ball.Velocity.Add(normal.Scale(-strength * (gap / pullRange) * deltaTime))
	}

	// Carry: move the ball with the player's input acceleration, capped at the player's
	// own speed along that direction, so the ball paces the player while dribbling.
	// Scaled by closeness (1 at the surface, 0 by TouchRange) so it is strong only at
	// contact and super weak otherwise -- approaching the ball does not nudge it away.
	// Gated on input accel (a knocked player never drags the ball) and one-sided (a shot
	// is never braked). There is no separate inward "approach damping": a captured ball
	// is absorbed by the restitution-0 contact, and the sticky + control damping below
	// keep it with the player, so no damping acts during the approach (no repulsion).
	if gap < pullRange {
		closeness := 1 - gap/player.Stats.TouchRange
		if closeness < 0 {
			closeness = 0
		} else if closeness > 1 {
			closeness = 1
		}
		if closeness > 0 {
			if accel := geom.Norm(player.Acceleration); accel > 0 {
				carryDir := player.Acceleration.Scale(1 / accel)
				lag := geom.Dot(player.Velocity.Sub(ball.Velocity), carryDir)
				if lag > 0 {
					step := accel * closeness * deltaTime
					if step > lag {
						step = lag
					}
					ball.Velocity = ball.Velocity.Add(carryDir.Scale(step))
				}
			}
		}
	}

	// While the ball is ACTUALLY touching: hold it, roll it to the front, and seat it.
	if gap < player.Stats.TouchRange {
		// Sticky hold (radial): resist the ball separating from the player up to a
		// capped holding impulse, scaled by the stickiness grip (near-constant, a hair
		// lower at full possession). Below the cap the separation is cancelled (the ball
		// clings); a push past it -- a shot or a hard bump -- frees the ball with the
		// excess. Strongest at the front, with a small baseline hold even at the back.
		separating := geom.Dot(ball.Velocity.Sub(player.Velocity), normal)
		holdCap := player.Stats.Stickiness.Eval(angle) * stickinessGrip *
			(1 + player.Stats.TrapStickinessBonus*player.trapCharge) * deltaTime

		// RETENTION measures how well the player's FULL hold contains the ball this
		// frame: the sticky cap above PLUS the centripetal stick's full inward pull
		// (which scales with how fast the ball is orbiting). It is 1 whenever that hold
		// can contain the ball, so every case the player keeps the ball -- including a
		// very fast rotation -- behaves EXACTLY as before: the centripetal stick keeps
		// its full strength and the settling forces run in full. Only once the ball
		// overcomes the WHOLE hold -- genuinely flinging off faster than even the
		// centripetal stick can arrest -- does retention ease below 1, and then it fades
		// ONLY the settling forces (roll-to-front, sideways damping, seat), never the
		// centripetal pull itself. So the anti-fling keeps its original strength while a
		// ball that truly breaks away leaves carrying its orbital momentum instead of
		// having it bled off at the surface as it goes.
		orbitVel := ball.Velocity.Sub(player.Velocity)
		orbitSpeed := geom.Norm(orbitVel.Sub(normal.Scale(geom.Dot(orbitVel, normal))))
		bindCap := holdCap + player.Stats.OrbitStick*orbitSpeed*deltaTime
		retention := 1.0
		if separating > bindCap {
			retention = bindCap / separating
		}

		// Apply the sticky hold (cancel the outward radial speed up to the cap).
		if separating > 0 {
			hold := holdCap
			if hold > separating {
				hold = separating
			}
			ball.Velocity = ball.Velocity.Sub(normal.Scale(hold))
		}

		// Control (tangential): roll the ball around to the front (0 rad), then damp
		// the sideways (orbital) velocity so it settles there instead of oscillating.
		// Trapping strengthens this, snapping the ball to the front for a clean touch.
		// Both fade with retention, so a ball that is decisively breaking away is neither
		// steered toward the front nor slowed -- it keeps the orbital momentum it has.
		// Control (roll-to-front) gets the trap bonus AND a small per-player possession boost
		// (1 + PossessionControlBonus*possession), so a settled carrier rolls the ball to its
		// front a touch more crisply -- a player boost, independent of the team charge.
		strength := player.Stats.Control.Eval(angle) *
			(1 + player.Stats.TrapControlBonus*player.trapCharge) *
			(1 + player.Stats.PossessionControlBonus*player.possession)
		tangential := player.Facing.Sub(normal.Scale(cos))
		ball.Velocity = ball.Velocity.Add(tangential.Scale(strength * deltaTime * retention))

		relative := ball.Velocity.Sub(player.Velocity)
		sideways := relative.Sub(normal.Scale(geom.Dot(relative, normal)))
		damping := player.Stats.ControlDamping * deltaTime
		if damping > 1 {
			damping = 1
		}
		ball.Velocity = ball.Velocity.Sub(sideways.Scale(damping * retention))

		// Centripetal stick: pull the ball inward in proportion to how fast it is still
		// orbiting the player, so a hard/fast turn curves the ball around the player
		// instead of flinging it off the surface. FULL strength always -- this IS the
		// anti-fling and keeps its original holding power (never scaled down). It
		// vanishes on its own once the ball settles (orbit -> 0), so it never disturbs a
		// resting ball.
		orbit := ball.Velocity.Sub(player.Velocity)
		orbitNow := geom.Norm(orbit.Sub(normal.Scale(geom.Dot(orbit, normal))))
		ball.Velocity = ball.Velocity.Sub(normal.Scale(player.Stats.OrbitStick * orbitNow * deltaTime))

		// Seat: gently draw the ball flush to the surface so there is no visible gap.
		// Position-based and proportional to the gap, so it vanishes at the surface (no
		// constant inward pull -> no jitter), capped so it never creates overlap, and
		// faded with retention so a ball that is leaving is not re-seated against the
		// player.
		if gap > 0 {
			seat := gap * player.Stats.SeatStrength * deltaTime * retention
			if seat > gap {
				seat = gap
			}
			ball.Position = ball.Position.Sub(normal.Scale(seat))
		}
	}
}

// updatePossession maintains two related states from the ball's position relative to the
// player, writing only the player (the ball and the body are untouched):
//
//   - possession: built toward 1 whenever the ball is TOUCHING the player anywhere (at any
//     angle), decayed otherwise. This is the "ball at my feet" state -- it drives the grip
//     on the ball (centre-pull and stickiness, so the ball sticks to the player) and pairs
//     with the touch test in applyIntent for the carry slowdown. Its EFFECTS are unchanged;
//     only its gate is the full touch range now, not the front arc.
//   - control: built toward 1 while the ball is touching AND within the front
//     PossessionArcRadians, decayed otherwise. This is the tighter "ball under control out
//     in front" state. It is TRACKED but currently UNUSED -- no mechanic reads it yet.
func updatePossession(ball *Ball, player *Player, deltaTime float64) {
	toBall := ball.Position.Sub(player.Position)
	dist := geom.Norm(toBall)
	touching := false // ball in contact anywhere -> possession
	inArc := false    // ball in contact within the front arc -> control
	if dist > 0 {
		gap := dist - player.Radius() - ball.Radius()
		if gap < player.Stats.TouchRange {
			touching = true
			angle := ballAngle(toBall.Scale(1/dist), player.Facing)
			inArc = angle <= player.Stats.PossessionArcRadians
		}
	}

	if touching {
		player.possession += deltaTime / player.Stats.PossessionBuildSeconds
		if player.possession > 1 {
			player.possession = 1
		}
	} else {
		player.possession -= deltaTime / player.Stats.PossessionReleaseSeconds
		if player.possession < 0 {
			player.possession = 0
		}
	}

	if inArc {
		player.control += deltaTime / player.Stats.PossessionBuildSeconds
		if player.control > 1 {
			player.control = 1
		}
	} else {
		player.control -= deltaTime / player.Stats.PossessionReleaseSeconds
		if player.control < 0 {
			player.control = 0
		}
	}
}

// shoot kicks the ball radially away from the player centre (wherever the ball is
// sitting relative to the player -- NOT along the facing direction) if the ball is
// close enough to be under control. Power comes from the shoot curve over the ball's
// angle (front shots strongest) and is scaled by the charge between a tap
// (MinShootFactor) and a full hold (full power).
// It returns whether a kick was actually applied (the ball was close enough), so the
// caller can record the kick as a touch for scoring attribution.
func shoot(player *Player, ball *Ball) bool {
	toBall := ball.Position.Sub(player.Position)
	distance := geom.Norm(toBall)
	gap := distance - player.Radius() - ball.Radius()
	if gap >= player.Stats.TouchRange {
		return false
	}

	dir := player.Facing // fallback if the ball sits exactly on the player centre
	angle := 0.0
	if distance > 0 {
		dir = toBall.Scale(1 / distance)
		angle = ballAngle(dir, player.Facing)
	}

	charge := NormShootCharge(player.shootCharge)
	factor := player.Stats.MinShootFactor + (1-player.Stats.MinShootFactor)*charge
	// Power follows the radial angle (front shots strongest); the LAUNCH DIRECTION is the
	// radial nudged toward the facing by the aim assist, so the shot goes where the player
	// aims when the ball is in the front cone and fires straight out (radial) at the side/back.
	power := player.Stats.Shoot.Eval(angle) * factor
	if distance > 0 {
		dir = player.Stats.ShootDirection(dir, player.Facing)
	}

	ball.Velocity = ball.Velocity.Add(dir.Scale(power))
	player.possession = 0
	player.control = 0
	return true
}
