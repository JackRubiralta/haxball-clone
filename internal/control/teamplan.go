package control

import (
	"math"

	"phootball/internal/sim"
)

// teamPlan is the deterministic division of labour a player derives from the shared view.
// Because every teammate computes it from the identical match state with the identical
// tie-breaks, they all agree on who does what without sharing any mutable state -- the
// core of the anti-swarm design (exactly one presser; everyone else holds shape).
type teamPlan struct {
	presser    int // own-team PlayerID elected to go for the ball
	support    int // own-team PlayerID that is second nearest (first support/cover)
	oppPresser int // nearest opponent to the ball (their likely chaser)
}

// keeperPressPenalty is added to a keeper's intercept cost so an outfielder is preferred
// as the presser; the keeper still wins if it is clearly closest (e.g. the ball is loose
// in its own box) or if the team has no outfielders.
const keeperPressPenalty = 0.7

// assignRoles elects the presser/support for both teams from the perception. It uses
// intercept TIME (not raw distance) so a player already moving toward the ball is
// preferred, quantises the cost so near-ties are stable, and breaks exact ties by
// PlayerID -- a total order every teammate evaluates the same way.
func assignRoles(p perception, tune aiTuning) teamPlan {
	plan := teamPlan{presser: -1, support: -1, oppPresser: -1}

	first, second := electPresser(p, p.view.Squad(p.me), tune)
	plan.presser, plan.support = first, second

	oppFirst, _ := electPresser(p, p.opponents, tune)
	plan.oppPresser = oppFirst
	return plan
}

// electPresser returns the PlayerID of the best and second-best chaser among players,
// by quantised intercept cost with a PlayerID tie-break.
func electPresser(p perception, players []sim.PlayerView, tune aiTuning) (best, second int) {
	best, second = -1, -1
	bestCost, secondCost := math.Inf(1), math.Inf(1)
	hasOutfield := false
	for _, q := range players {
		if q.Role() != sim.RoleGoalkeeper {
			hasOutfield = true
			break
		}
	}
	for _, q := range players {
		cost := interceptCost(p, q, tune)
		if q.Role() == sim.RoleGoalkeeper && hasOutfield {
			cost += keeperPressPenalty
		}
		if cost < bestCost || (cost == bestCost && (best == -1 || q.ID() < best)) {
			second, secondCost = best, bestCost
			best, bestCost = q.ID(), cost
		} else if cost < secondCost || (cost == secondCost && (second == -1 || q.ID() < second)) {
			second, secondCost = q.ID(), cost
		}
	}
	return best, second
}

// interceptCost is the quantised seconds a player needs to reach the ball; the quantum
// gives the election a built-in hysteresis so the presser does not flicker between two
// near-equal players from tick to tick.
func interceptCost(p perception, q sim.PlayerView, tune aiTuning) float64 {
	reach := q.Radius() + p.ballRadius
	t := interceptTime(q.Position(), q.Stats().MaxSpeed, q.Stats().TurnRate, q.Heading(), reach, p.ball, p.ballVel, p.friction, p.dt, tune)
	if tune.interceptQuantum > 0 {
		t = math.Round(t/tune.interceptQuantum) * tune.interceptQuantum
	}
	return t
}
