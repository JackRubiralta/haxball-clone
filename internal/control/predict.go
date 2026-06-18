package control

import (
	"math"

	"phootball/internal/geom"
	"phootball/internal/sim"
)

// Ball prediction and intercept math. The ball integrates with linear drag: each tick
// v *= (1 + friction*dt) (see physics.Body.Update and sim.NewBall, friction -0.3). That
// makes the future position a geometric series with a closed form, so the AI can aim at
// where the ball WILL be rather than where it is.

// predictBall returns the ball's position t seconds from now given its current position,
// velocity, the per-tick drag friction (negative), and the timestep dt.
func predictBall(p0, v0 geom.Vec, t, friction, dt float64) geom.Vec {
	if t <= 0 {
		return p0
	}
	r := 1 + friction*dt // per-tick velocity multiplier (~0.995 for -0.3 at 60Hz)
	n := math.Round(t / dt)
	var factor float64
	if math.Abs(1-r) < 1e-9 {
		factor = dt * n // no drag: straight line
	} else {
		// physics.Body.Update decays velocity BEFORE moving, so the displacement sums
		// v0*r^1..r^n (not r^0..r^(n-1)); the extra r matches the sim exactly (no over-lead).
		factor = dt * r * (1 - math.Pow(r, n)) / (1 - r)
	}
	return p0.Add(v0.Scale(factor))
}

// ballSpeedAt returns the ball's speed t seconds from now: the launch speed decayed by the
// per-tick drag multiplier (1 + friction*dt) over the elapsed ticks. Used to find the point
// on a pass's path where the ball has slowed enough to receive cleanly.
func ballSpeedAt(v0 geom.Vec, t, friction, dt float64) float64 {
	s := geom.Norm(v0)
	if t <= 0 || s == 0 {
		return s
	}
	r := 1 + friction*dt
	if r <= 0 {
		return 0
	}
	return s * math.Pow(r, math.Round(t/dt))
}

// interceptTime estimates the earliest time (seconds) a mover starting at `from` with the
// given top speed can reach the moving ball, searching forward in small steps. It returns
// a value just past the horizon if the ball cannot be caught in time. reachSlack is added
// to the mover's reach (e.g. player+ball radius) so a graze counts as contact.
//
// It is TURN-RATE AWARE: a mover cannot redirect instantly (the sim rotates its movement
// heading toward the input at turnRate rad/s -- see sim.Player.Move), so a mover pointed
// away from the ball loses real time turning before it makes useful ground. heading is the
// mover's current committed direction: for SELF this is its true steering heading
// (SelfView.Heading()), but for any OTHER player that is hidden, so the caller passes the
// player's VISIBLE facing instead. The time to rotate it to face the target is psi/turnRate,
// weighted by turnPenaltyGain and subtracted from the usable closing time. A zero heading or
// zero turn rate means "no committed direction" (e.g. stationary at a kickoff) and applies no
// penalty, so kickoff election is unchanged.
func interceptTime(from geom.Vec, maxSpeed, turnRate float64, heading geom.Vec, reachSlack float64, ballPos, ballVel geom.Vec, friction, dt float64, tune aiTuning) float64 {
	penalize := tune.turnPenaltyGain > 0 && turnRate > 0 && geom.Norm(heading) > 1e-9
	for t := 0.0; t <= tune.interceptHorizon; t += tune.interceptStep {
		target := predictBall(ballPos, ballVel, t, friction, dt)
		usable := t
		if penalize {
			psi := geom.AngleBetween(heading, target.Sub(from))
			usable = t - tune.turnPenaltyGain*psi/turnRate
			if usable < 0 {
				usable = 0
			}
		}
		if geom.Dist(from, target)-reachSlack <= maxSpeed*usable {
			return t
		}
	}
	return tune.interceptHorizon + tune.interceptStep
}

// interceptPoint returns the predicted ball position at the estimated intercept time --
// the spot a chaser should run toward (lead the ball, don't trail it).
func interceptPoint(from geom.Vec, maxSpeed, turnRate float64, heading geom.Vec, reachSlack float64, ballPos, ballVel geom.Vec, friction, dt float64, tune aiTuning) geom.Vec {
	t := interceptTime(from, maxSpeed, turnRate, heading, reachSlack, ballPos, ballVel, friction, dt, tune)
	return predictBall(ballPos, ballVel, t, friction, dt)
}

// laneSafe reports whether a pass from `from` to `to` reaches its target before any
// opponent can step into the passing lane. The ball DECELERATES as it rolls (linear drag:
// dv/dx = friction, so v(x) = passSpeed + friction*x), which matters a lot over long
// passes -- the ball is far slower at the far end than at launch -- so the time to each
// lane point is the real decelerating travel time, not distance/launch-speed (which would
// wave through long balls that actually get cut out late). Returns the safety margin in
// seconds (positive = safe); a pass that stops short of a lane point is treated as beaten.
func laneSafe(from, to geom.Vec, passSpeed, ballRadius, friction float64, opponents []sim.ObservedView, tune aiTuning) float64 {
	laneLen := geom.Dist(from, to)
	if laneLen < 1e-6 {
		return -1
	}
	dir := to.Sub(from).Scale(1 / laneLen)
	worst := math.Inf(1)
	for _, o := range opponents {
		// Project the opponent onto the lane; the ball reaches that point after its real
		// (decelerating) travel time, the opponent after a straight run at top speed.
		rel := o.Position().Sub(from)
		along := clampFloat(geom.Dot(rel, dir), 0, laneLen)
		lanePt := from.Add(dir.Scale(along))
		ballT := ballTravelTime(along, passSpeed, friction)
		// An opponent's top speed is hidden (not rendered) -> assume the nominal value.
		oppT := (geom.Dist(o.Position(), lanePt) - o.Radius() - ballRadius) / tune.assumedOppSpeed
		margin := oppT - ballT // how much sooner the ball arrives than the opponent
		if margin < worst {
			worst = margin
		}
	}
	if math.IsInf(worst, 1) {
		return 1 // no opponents to worry about
	}
	return worst - tune.passRiskMargin
}

// ballTravelTime returns how long the ball takes to roll arc-length s from launch at speed
// v0, decelerating with linear drag (dv/dx = friction, friction < 0): integrating dt =
// dx/v(x) gives t = ln(v0/(v0+friction*s)) / (-friction). If the ball stops before s
// (v0+friction*s <= 0), it never arrives -> +Inf.
func ballTravelTime(s, v0, friction float64) float64 {
	if s <= 0 || v0 <= 0 {
		return 0
	}
	k := -friction
	if k <= 1e-9 {
		return s / v0 // no drag: constant speed
	}
	rem := v0 - k*s
	if rem <= 0 {
		return math.Inf(1) // the ball stops short of this point
	}
	return math.Log(v0/rem) / k
}
