package control

// aiTuning collects every behavioural constant in one place so the AI can be tuned and
// swept from tests without hunting through the logic files. The default values are tuned
// for the standard pitch (880x480) but are expressed relative to player/field sizes
// where it matters, so they scale to other geometries.
type aiTuning struct {
	// Movement / arrival.
	arriveRadius float64 // movement deadzone so players settle on a target
	slowRadius   float64 // start easing throttle within this distance of a target

	// Obstacle avoidance (anti-stuck steering).
	avoidRadius  float64 // surface gap within which a body ahead deflects the heading
	avoidLateral float64 // base strength of the sideways steer around a blocking body
	avoidPushoff float64 // surface gap within which a direct push-off kicks in
	avoidPush    float64 // strength of the direct push-off from a body we're on top of

	// Pressing / chasing.
	interceptStep    float64 // seconds between samples when searching for an intercept
	interceptHorizon float64 // how far ahead (seconds) to search for an intercept
	interceptQuantum float64 // round intercept times to this, for stable presser election
	turnPenaltyGain  float64 // turn-rate awareness: weight on the time lost rotating to face a target before useful closing (0 = ignore turn cost)

	// Formation shape.
	defenderDepth float64 // normalized depth (0=own goal,1=enemy goal) of the back line
	forwardDepth  float64 // normalized depth of the front line
	widthMin      float64 // normalized lateral band edges for spreading a line
	widthMax      float64
	ballShiftX    float64 // how far the block slides toward the ball (fraction), along/across
	ballShiftY    float64
	attackBias    float64 // extra depth pushed up when our team has the ball
	defendBias    float64 // extra depth dropped when defending
	slotJitter    float64 // small deterministic per-player slot noise (world units)

	// Shooting.
	shootRange       float64 // distance to goal under which shooting is considered at all
	tapRange         float64 // at/under this range a tap (min charge) is enough
	fullRange        float64 // at/over this range aim for a full-power charge
	shootGap         float64 // extra standoff behind the ball when lining up a radial shot
	shootAlignRad    float64 // alignment tolerance (radians) the shot must reach before releasing
	shootAlignMaxRad float64 // widest alignment tolerance (radians) once a shot has fully relaxed
	cornerInset      float64 // how far inside each post to aim (world units)
	shootBallSide    float64 // min alignment of ball-side with goal to commit a shot (else reposition)
	shootOpenBonus   float64 // flat utility bonus for a clear (open) shot, so good chances are taken
	shootAimFrac     float64 // fraction of the way to the open post the shot aims (margin so it stays on target)
	minShootCharge   float64 // floor on a shot's charge so even close shots are hit firmly

	// Passing.
	passMinAdvance    float64 // a pass must move the ball at least this far goalward to rate
	passRiskMargin    float64 // safety margin (seconds) an opponent needs to beat to kill a lane
	passReachMargin   float64 // how much later (seconds) the receiver may arrive than the ball and still collect it
	passContestMargin float64 // an opponent reaching the target within this margin of our man kills the pass
	passForwardBonus  float64 // standing preference for a safe forward pass over dribbling
	passSafetyMin     float64 // minimum lane-safety margin (seconds) to attempt a pass
	passReceiverSpace float64 // receiver must have at least this much space to be worth a pass
	throughDist       float64 // how far ahead of a runner a through ball is played (world units)
	passArriveSpeed   float64 // target ball speed at the receiver -- passes are calibrated to arrive this soft
	passSpeedMin      float64 // clamp on a calibrated pass launch speed (min)
	passSpeedMax      float64 // clamp on a calibrated pass launch speed (max)
	passDistPenalty   float64 // small score penalty per unit of pass distance (favour simple safe balls)

	// Off-ball receiving movement.
	supportForwardBias float64 // upfield bias of a supporting receiver's search (world units)
	supportRangeFrac   float64 // distance the designated short supporter holds from the ball, as a fraction of pitch width
	runForwardBias     float64 // stronger upfield bias for a give-and-go run after passing

	recvLaneWeight    float64 // weight on a clear pass lane from the ball when picking a spot
	recvSpaceWeight   float64 // weight on local open space when picking a receive spot
	recvAdvanceWeight float64 // weight on goalward advancement when picking a receive spot
	oneTwoTicks       uint64  // ticks a player keeps making its give-and-go run after passing

	separationRadius      float64 // an off-ball player repels its movement from teammates within this gap (world units)
	separationGain        float64 // strength of that repulsion blended into the move direction (0 = off)
	separationMinThrottle float64 // throttle floor so an idle-but-crowded off-ball player still drifts apart

	// Pressure / decisions.
	pressureRadius    float64 // an opponent within this distance applies pressure
	shieldPressure    float64 // pressure above which the carrier shields/clears instead of dwelling
	clearThird        float64 // normalized depth of own goal under which a clearance is favoured
	clearCharge       float64 // charge for a clearance -- low, so it fires quickly
	clearAlignRad     float64 // alignment tolerance for a clearance -- wide, so it boots it away fast
	settlePossession  float64 // build possession to this before shooting/passing (don't kick a loose touch)
	settleThrottle    float64 // throttle while nursing a fresh touch into control
	actPressure       float64 // pressure above which the carrier must act now instead of settling
	kickCooldownTicks uint64  // ticks after a kick during which the player dribbles, not kicks
	shootHurryWindow  float64 // open-window (seconds) under which the shot is hurried (less charge)
	contestMargin     float64 // intercept-time margin within which the ball is "contested" (don't trap)
	maxChargeTicks    uint64  // give-up timeout: ticks a charge can run before the attempt is abandoned
	aimRelaxTicks     uint64  // after charge, ticks over which the aim tolerance relaxes if not lined up
	turnTrapRad       float64 // dribble heading change above which the player traps and eases the turn
	maxTurnRad        float64 // max facing change per decision with a settled ball (anti-fling)
	minTurnRad        float64 // max facing change per decision with a loose ball (it lags more, turn gentler)
	turnTrapSettled   float64 // ball-settledness below which the dribbler traps to keep the ball glued
	dribbleWallAvoid  float64 // penalty weight steering a dribble heading away from carrying the ball into a wall

	// Trap usage.
	trapApproachFactor float64 // (reserved) trap to receive when ball approach speed exceeds capture*this
	trapReceiveFactor  float64 // trap to receive once an incoming ball's closing speed exceeds capture*this
	trapReceiveRange   float64 // surface gap within which the receiver sets trap for a clean touch
	stealRange         float64 // trap to steal when within this gap of an enemy fresh-touch ball
	prechargeETA       float64 // seconds-to-ball under which the presser pre-charges a clearance

	// Goalkeeper.
	keeperDepthMin    float64 // closest the keeper sits to its own goal line (world units)
	keeperDepthMax    float64 // furthest out the keeper advances to cut the angle
	keeperSweepBox    float64 // sweep loose balls within this multiple of the goal-area depth
	keeperSaveSpeed   float64 // ball speed toward goal above which the keeper anticipates the save
	keeperSweepMargin float64 // intercept-time margin the keeper must beat opponents by to sweep
}

// defaultAITuning returns the baseline behavioural tuning.
func defaultAITuning() aiTuning {
	return aiTuning{
		arriveRadius: 6,
		slowRadius:   40,

		avoidRadius:  42,
		avoidLateral: 0.9,
		avoidPushoff: 6,
		avoidPush:    0.8,

		interceptStep:    0.05,
		interceptHorizon: 2.5,
		interceptQuantum: 0.05,
		turnPenaltyGain:  0.40,

		defenderDepth: 0.22,
		forwardDepth:  0.80,
		widthMin:      0.14,
		widthMax:      0.86,
		ballShiftX:    0.32,
		ballShiftY:    0.55,
		attackBias:    0.12,
		defendBias:    0.14,
		slotJitter:    14,

		shootRange:       360,
		tapRange:         120,
		fullRange:        260,
		shootGap:         3,
		shootAlignRad:    0.08726646259971647,
		shootAlignMaxRad: 0.10471975511965977,
		cornerInset:      16,
		shootBallSide:    -0.1,
		shootOpenBonus:   0.6,
		shootAimFrac:     0.35,
		minShootCharge:   0.5,

		passMinAdvance:    40,
		passRiskMargin:    0.12,
		passReachMargin:   0.4,
		passContestMargin: 0.3,
		passForwardBonus:  0.5,
		passSafetyMin:     0.16,
		passReceiverSpace: 30,
		throughDist:       110,
		passArriveSpeed:   215,
		passSpeedMin:      185,
		passSpeedMax:      430,
		passDistPenalty:   0.0004,

		supportForwardBias:    40,
		supportRangeFrac:      0.3,
		runForwardBias:        150,
		recvLaneWeight:        60,
		recvSpaceWeight:       0.6,
		recvAdvanceWeight:     0.45,
		oneTwoTicks:           50,
		separationRadius:      44, // just above the 36 contact gap: only fires to avert a near-collision
		separationGain:        0.6,
		separationMinThrottle: 0.3, // a near-collision pulls an idle player off the spot to step apart

		pressureRadius:    70,
		shieldPressure:    0.55,
		clearThird:        0.32,
		clearCharge:       0.45,
		clearAlignRad:     0.4886921905584123,
		settlePossession:  0.45,
		settleThrottle:    0.72,
		actPressure:       0.55,
		kickCooldownTicks: 22,
		shootHurryWindow:  0.45,
		contestMargin:     0.1,
		maxChargeTicks:    96,
		aimRelaxTicks:     22,
		turnTrapRad:       0.4363323129985824,
		maxTurnRad:        0.22689280275926285,
		minTurnRad:        0.08726646259971647,
		turnTrapSettled:   0.5,
		dribbleWallAvoid:  3.0,

		trapApproachFactor: 0.7,
		trapReceiveFactor:  0.4,
		trapReceiveRange:   44,
		stealRange:         10,
		prechargeETA:       0.33,

		keeperDepthMin:    24,
		keeperDepthMax:    60,
		keeperSweepBox:    1.1,
		keeperSaveSpeed:   85, // anticipate the predictive save on more shots (was 110)
		keeperSweepMargin: 0.12,
	}
}

// Skill is the AI difficulty tier. Higher tiers react faster, aim more accurately, and
// make better decisions; lower tiers are slower and noisier so a human can compete.
type Skill int

const (
	SkillEasy Skill = iota
	SkillNormal
	SkillHard
	// SkillImpossible plays as close to perfect as the model allows: instant reactions, no
	// aim or decision error, an almost-unbeatable keeper. Intended for testing and for
	// showcasing clean passing/dribbling/flow rather than for a fair human match.
	SkillImpossible
)

// DefaultSkill is the tier used when none is specified: a strong, competitive AI.
const DefaultSkill = SkillHard

// skillParams scales the AI's competence for a difficulty tier.
type skillParams struct {
	reactTicks  int     // decision latency: re-decide only every N ticks (1 = every tick)
	aimNoiseRad float64 // 1-sigma aim error (radians) added to shots/passes
	scoreNoise  float64 // magnitude of decision-score jitter (fraction of a score unit)
	moveJitter  float64 // off-target movement wander (world units)
	chargeSlack float64 // tolerance on reaching the desired shot charge before releasing
	keeperError float64 // 1-sigma keeper mis-read of a shot's crossing point (world units at speed)
}

// paramsForSkill returns the competence scaling for a tier.
func paramsForSkill(s Skill) skillParams {
	switch s {
	case SkillEasy:
		return skillParams{reactTicks: 10, aimNoiseRad: 0.19198621771937624, scoreNoise: 0.35, moveJitter: 16, chargeSlack: 0.25, keeperError: 60}
	case SkillNormal:
		return skillParams{reactTicks: 5, aimNoiseRad: 0.10471975511965977, scoreNoise: 0.2, moveJitter: 8, chargeSlack: 0.15, keeperError: 42}
	case SkillHard:
		return skillParams{reactTicks: 2, aimNoiseRad: 0, scoreNoise: 0.06, moveJitter: 2, chargeSlack: 0.08, keeperError: 28}
	default: // SkillImpossible -- perfect execution
		return skillParams{reactTicks: 1, aimNoiseRad: 0, scoreNoise: 0, moveJitter: 0, chargeSlack: 0.04, keeperError: 12}
	}
}

// SkillFromString maps a difficulty name to a Skill, defaulting to DefaultSkill for an
// empty or unknown string. The bool reports whether the name was recognised.
func SkillFromString(s string) (Skill, bool) {
	switch s {
	case "", "default":
		return DefaultSkill, true
	case "easy":
		return SkillEasy, true
	case "normal", "medium":
		return SkillNormal, true
	case "hard", "pro":
		return SkillHard, true
	case "impossible", "perfect":
		return SkillImpossible, true
	default:
		return DefaultSkill, false
	}
}
