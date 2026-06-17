package control

import "phootball/internal/geom"

// Steering / obstacle avoidance. The biggest cause of an AI looking "stuck" is grinding
// straight into a body that sits in the direction it wants to go. avoid() takes the desired
// heading and bends it AROUND nearby players -- sidestepping the ones in the path toward the
// side with more room -- so dribbles, runs and chases flow around traffic instead of into
// it. It is folded into steer(), so every movement decision gets it for free.

// avoid returns a unit steering direction: the desired heading nudged laterally away from
// players that block the path, plus a direct push-off from any body the player is right on
// top of. When the player is barely moving (genuinely jammed), the avoidance is amplified
// and a definite escape side is chosen so it breaks free rather than oscillating.
func (a *AI) avoid(p perception, desired geom.Vec) geom.Vec {
	desired = geom.Unit(desired)
	if desired == (geom.Vec{}) {
		return desired
	}
	side := perp(desired) // unit perpendicular (desired is unit)

	// Escalate when jammed: the slower we are actually moving, the harder we steer out.
	jam := clampFloat(1-geom.Norm(p.me.Velocity())/40, 0, 1)
	lateralGain := a.tune.avoidLateral * (1 + 2*jam)

	steer := desired
	accumulate := func(qx, qy, qr float64) {
		rel := geom.NewVec(qx, qy).Sub(p.me.Position())
		d := geom.Norm(rel)
		contact := p.me.Radius() + qr
		if d < 1e-6 || d > a.tune.avoidRadius+contact {
			return
		}
		relU := rel.Scale(1 / d)
		ahead := geom.Dot(relU, desired)
		if ahead <= 0.25 {
			return // the body is beside or behind the heading, not in the way
		}
		prox := 1 - (d-contact)/a.tune.avoidRadius
		if prox < 0 {
			prox = 0
		}
		// Steer to the side of the body that is more open. Pick the side it ISN'T on; if it
		// is dead ahead, break the tie deterministically per player so mirrored players
		// don't both pick the same way and collide.
		lateral := geom.Dot(side, relU)
		s := 1.0
		switch {
		case lateral > 0.05:
			s = -1
		case lateral < -0.05:
			s = 1
		default:
			if personality(p.me.ID(), 21^p.seed) < 0 {
				s = -1
			}
		}
		steer = steer.Add(side.Scale(s * prox * ahead * lateralGain))

		// Right on top of a body: add a direct push-off so we never overlap-grind.
		if d < contact+a.tune.avoidPushoff {
			steer = steer.Sub(relU.Scale(prox * a.tune.avoidPush))
		}
	}
	for _, q := range p.teammates {
		accumulate(q.Position().X, q.Position().Y, q.Radius())
	}
	for _, q := range p.opponents {
		accumulate(q.Position().X, q.Position().Y, q.Radius())
	}
	return geom.Unit(steer)
}
