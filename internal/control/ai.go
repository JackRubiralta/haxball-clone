package control

import (
	"phootball/internal/geom"
	"phootball/internal/sim"
)

// AI is a headless, utility-based controller. Each tick it builds a read-only perception
// of the match, derives a deterministic team plan (who presses, who supports), and then
// scores and executes the best action for its player -- on the ball (shoot/pass/dribble/
// clear/shield), off the ball (press/support/mark/hold shape), or in goal. It produces the
// same Intent a human does, so the simulation cannot tell them apart, and it is fully
// deterministic (no random source), so the server stays authoritative and tests replay.
type AI struct {
	ID     int
	skill  Skill
	params skillParams
	tune   aiTuning

	// Cross-tick state. None of it is shared between players, so coordination stays a pure
	// function of the shared view (see teamplan.go).
	cached         sim.Intent // last decided intent, reused during the reaction-delay window
	nextDecision   uint64     // tick at which we re-decide (reaction latency)
	haveCached     bool
	charging       bool           // a shot charge is in progress (the charge controller's state)
	shotTarget     geom.Vec       // committed aim point while charging
	shotDesired    float64        // committed charge fraction to reach before releasing
	shotAlignRad   float64        // committed base alignment tolerance (tight for shots/passes, wide for clears)
	chargeStart    uint64         // tick the current charge began, for a give-up timeout
	chargedAt      uint64         // tick the charge first reached target (0 = not yet), for aim-relax
	passReceiver   sim.PlayerView // receiver of an in-progress pass, so its target tracks the runner
	kickCooldown   uint64         // tick until which the player won't kick again (forces a real touch between kicks)
	lastDribbleDir geom.Vec       // last dribble heading, for turn-rate limiting (ball retention)
	lastOnBall     onBallKind     // last on-ball action, for decision hysteresis
	runUntil       uint64         // tick until which, having just passed, the player makes a give-and-go run
	recovering     bool           // hysteretic: facing the ball to scoop it back to the front (anti-jitter)
}

// NewAI creates an AI controller for the given player at the default skill tier.
func NewAI(id int) *AI { return NewAISkill(id, DefaultSkill) }

// NewAISkill creates an AI controller at a specific difficulty tier.
func NewAISkill(id int, skill Skill) *AI {
	return &AI{
		ID:     id,
		skill:  skill,
		params: paramsForSkill(skill),
		tune:   defaultAITuning(),
	}
}

// Intent decides this player's action for the tick.
func (a *AI) Intent(view sim.View) sim.Intent {
	me, ok := view.Me(a.ID)
	if !ok {
		return sim.Intent{}
	}

	// Reaction latency: only re-decide every reactTicks ticks, reusing the last intent in
	// between. This models human reaction time and gives lower skill tiers a real handicap.
	if a.haveCached && view.Tick() < a.nextDecision {
		return a.cached
	}

	p := perceive(view, me, a.dt(view))
	plan := assignRoles(p, a.tune)

	var in sim.Intent
	switch {
	case me.Role() == sim.RoleGoalkeeper:
		in = a.keeper(p, plan)
	case p.iControl:
		in = a.onBall(p, plan)
	case plan.presser == me.ID():
		in = a.press(p, plan)
	default:
		in = a.offBall(p, plan)
	}

	in = a.applyMoveJitter(p, in)

	a.cached = in
	a.haveCached = true
	step := a.params.reactTicks
	if step < 1 {
		step = 1
	}
	a.nextDecision = view.Tick() + uint64(step)
	return in
}

// dt estimates the simulation timestep from the match clock so ball prediction matches the
// real tick rate (60Hz locally, configurable on the server). Falls back to 1/60 at start.
func (a *AI) dt(view sim.View) float64 {
	if view.Tick() > 0 && view.Clock() > 0 {
		return view.Clock() / float64(view.Tick())
	}
	return 1.0 / 60.0
}

// applyMoveJitter adds a little skill-scaled wander to the movement direction, so players
// don't track perfectly straight lines. It never touches the kick/trap buttons.
func (a *AI) applyMoveJitter(p perception, in sim.Intent) sim.Intent {
	if a.params.moveJitter <= 0 || in.Move == (sim.Intent{}).Move {
		return in
	}
	j := a.params.moveJitter
	in.Move = in.Move.Add(perp(geom.Unit(in.Move)).Scale(noise(a.ID, p.view.Tick(), 7^p.seed) * j))
	return in
}
