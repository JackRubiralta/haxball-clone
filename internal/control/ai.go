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
	charging       bool             // a shot charge is in progress (the charge controller's state)
	shotTarget     geom.Vec         // committed aim point while charging
	shotDesired    float64          // committed charge fraction to reach before releasing
	shotAlignRad   float64          // committed base alignment tolerance (tight for shots/passes, wide for clears)
	chargeStart    uint64           // tick the current charge began, for a give-up timeout
	chargedAt      uint64           // tick the charge first reached target (0 = not yet), for aim-relax
	passReceiver   sim.ObservedView // receiver of an in-progress pass, so its target tracks the runner
	kickCooldown   uint64           // tick until which the player won't kick again (forces a real touch between kicks)
	lastDribbleDir geom.Vec         // last dribble heading, for turn-rate limiting (ball retention)
	lastOnBall     onBallKind       // last on-ball action, for decision hysteresis
	runUntil       uint64           // tick until which, having just passed, the player makes a give-and-go run
	holdStart      uint64           // tick (+1; 0 = not holding) the player gained control, to measure how long it has been on the ball -- a release valve forces it to move the ball on rather than hoard it. Own state only (a human knows how long it has held the ball), so it's within the AI<=human boundary.
	holdSpot       geom.Vec         // a supporter's CURRENT held receiving spot -- kept stable so a pass to it lands where the receiver still is (drifting receivers were the dominant over-hit failure)
	holdSpotBall   geom.Vec         // ball position when holdSpot was last (re)picked, to detect when the held spot has gone stale
	holdSpotOK     bool             // holdSpot is valid (a supporter is holding it)
	recovering     bool             // hysteretic: facing the ball to scoop it back to the front (anti-jitter)
	recoverTrap    bool             // previous tick we were trapping to scoop the ball home mid-commit (for the cancel/release sequencing)
	faceActioning  bool             // hysteretic (faceAim): currently facing the ACTION (ball) vs facing travel direction, with a release band so the directional facing policy doesn't flip-flop and jitter
	aimTarget      geom.Vec         // off-ball: the DESIRED facing point (uncapped), turned toward at maxTurnRad every tick (incl. reaction-cache ticks) so reaction latency delays the decision, not the turn
	aimSmooth      bool             // the last decision was an off-ball one whose aim should be smoothly turn-rate-tracked toward aimTarget on cached ticks

	// Diagnostic-only snapshot of the last pass this controller committed to, read ONLY by the
	// failed-pass classifier in the package's internal tests. It is WRITE-ONLY from the AI's
	// perspective: no decision path ever reads these fields back, so they cannot influence play
	// and do not widen what the AI perceives. Recording the AI's OWN intended pass is not a
	// boundary violation -- a human knows where they meant to pass; this is the controller's own
	// decision, not hidden opponent state. See recordPassIntent.
	diagPassTarget geom.Vec // the aim point of the committed pass
	diagPassRecvID int      // intended receiver's player ID (-1 if none/unknown)
	diagPassTick   uint64   // tick the pass intent was last recorded, to detect staleness
	diagPassSet    bool     // a pass intent has been recorded at least once
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
// enforceAbilityExclusivity clamps an intent so at most ONE of Trap > Push > Shoot is active,
// exactly as the human controller does -- a player has three mouse buttons and cannot use more than
// one ability at a time (the ai-capability-boundary rule: the AI may do only what a human can input).
// When a trap or push takes over a charging shot, the shot is CANCELLED rather than fired: ShootHeld
// is kept asserted with CancelCharge set, because the sim only honours a cancel while shoot reads
// held (a bare release would instead fire the charged shot -- see Match.applyIntent).
func enforceAbilityExclusivity(in sim.Intent) sim.Intent {
	switch {
	case in.Trap: // trap takes precedence over both push and shoot
		in.Push = false
		if in.ShootHeld {
			in.CancelCharge = true
		}
	case in.Push: // push takes precedence over shoot
		if in.ShootHeld {
			in.CancelCharge = true
		}
	}
	return in
}

func (a *AI) Intent(view sim.View) sim.Intent {
	// Defensive: a nil or foreign view, or one that does not contain this controller's player,
	// yields a neutral (idle) intent rather than panicking.
	if view == nil {
		return sim.Intent{}
	}
	me, ok := view.Me(a.ID)
	if !ok {
		return sim.Intent{}
	}

	// Reaction latency: only re-decide every reactTicks ticks, reusing the last intent in
	// between. This models human reaction time and gives lower skill tiers a real handicap.
	// The reaction delay applies to the DECISION (what to aim at), NOT to the turning itself: a
	// human keeps rotating toward their last-decided target at TurnRate every tick. So for off-ball
	// aims (instant aimToward), keep advancing the facing toward the stored target at maxTurnRad on
	// the cached ticks too -- otherwise the facing would either snap (cheat) or, if frozen between
	// decisions, crawl at half the human turn rate (sluggish). The on-ball carrier is left alone (its
	// aimKeepingBall turn is already modulated for ball control and is replayed as-is).
	if a.haveCached && view.Tick() < a.nextDecision {
		out := a.cached
		if a.aimSmooth && a.aimTarget != (geom.Vec{}) {
			out.Aim = a.turnAimToward(me, a.aimTarget)
		}
		return out
	}

	p := perceive(view, me, a.dt(view))
	plan := assignRoles(p, a.tune)

	// Track how long this player has continuously been on the ball (own state), so the on-ball
	// decision can force it to move the ball on rather than hoard it. holdStart is a tick stamp
	// (+1 so 0 means "not holding"); a non-control tick clears it. See AI.heldTicks / holdPressure.
	if p.iControl {
		if a.holdStart == 0 {
			a.holdStart = view.Tick() + 1
		}
	} else {
		a.holdStart = 0
	}

	var in sim.Intent
	switch {
	case me.Role() == sim.RoleKeeper:
		in = a.keeper(p, plan)
	case p.iControl:
		in = a.onBall(p, plan)
	case plan.presser == me.ID():
		in = a.press(p, plan)
	default:
		in = a.offBall(p, plan)
	}

	// Capability boundary: a human uses three mutually-exclusive mouse buttons, so it can do only ONE
	// of {trap, push, shoot} at a time (see input.Human and the ai-capability-boundary rule). Enforce
	// the same precedence (Trap > Push > Shoot) on the AI -- it must never trap-while-charging or
	// push-while-charging. A higher-priority ability CANCELS a live shot charge (dropped, not fired).
	in = enforceAbilityExclusivity(in)

	in = a.applyMoveJitter(p, in)
	// Capability boundary: the AI's facing is set INSTANTLY in the sim (match.go FaceTowards), unlike a
	// human whose cursor turn is rate-limited to TurnRate. So the AI must rate-limit its OWN aim, or it
	// snap-turns (measured: a ~180 deg flip in one tick, both by a presser crowding the ball AND by the
	// carrier's shield() facing, which uses instant aimToward). Fix uniformly: record the DESIRED aim
	// target and turn the facing toward it by at most maxTurnRad -- here AND on the cached ticks above --
	// so the AI tracks at the human rate every tick and never snaps. This is a NO-OP for aims already
	// within maxTurnRad of the facing (aimKeepingBall's dribble/shoot/pass turn, with its gentler
	// ball-control modulation, is preserved); it only reins in the instant aimToward paths (shield,
	// presser, receiver, keeper, off-ball). Reaction latency thus delays the DECISION, not the turn.
	if in.Aim != (geom.Vec{}) {
		a.aimTarget = in.Aim
		a.aimSmooth = true
		in.Aim = a.turnAimToward(me, in.Aim)
	} else {
		a.aimSmooth = false
	}

	a.cached = in
	// A push is an instant one-shot edge action, not a held button: never let the reaction-delay
	// replay re-fire it on the cached ticks (that would compound the jab). Fire it only this tick.
	a.cached.Push = false
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
	if a.params.moveJitter <= 0 || in.Move == (geom.Vec{}) {
		return in
	}
	j := a.params.moveJitter
	in.Move = in.Move.Add(perp(geom.Unit(in.Move)).Scale(noise(a.ID, p.view.Tick(), 7^p.seed) * j))
	return in
}
