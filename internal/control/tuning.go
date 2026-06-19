package control

import "phootball/internal/config"

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

	// Opponent/teammate motion estimates. A controller may NOT read another player's hidden
	// speed/turn-rate tuning (that is not visible to a human), so it assumes these nominal
	// values for everyone else. Defaulted to the shared field-player MaxSpeed/TurnRate, so the
	// nominal case matches what the AI used to read directly.
	assumedOppSpeed float64 // assumed top speed of any other player (px/s)
	assumedOppTurn  float64 // assumed turn rate of any other player (rad/s)
	leadGain        float64 // 0..1: how much of the assumed-speed facing lead to apply when passing to a team-mate (velocity is hidden, so this is a conservative observable estimate)

	// Formation shape. Each outfielder's depth band is chosen by its authoritative sim.Role()
	// (see roleSlot), so the four roles are behaviourally distinct lines.
	defenderDepth   float64 // normalized depth (0=own goal,1=enemy goal) of the defender line
	midfielderDepth float64 // normalized depth of the midfield line (between the defenders and attackers)
	forwardDepth    float64 // normalized depth of the attacker line
	widthMin        float64 // normalized lateral band edges for spreading a line
	widthMax        float64
	ballShiftX      float64 // how far the block slides toward the ball (fraction), along/across
	ballShiftY      float64
	attackBias      float64 // extra depth pushed up when our team has the ball
	defendBias      float64 // extra depth dropped when defending
	slotJitter      float64 // small deterministic per-player slot noise (world units)

	// Shooting.
	shootRange       float64 // distance to goal under which shooting is considered at all
	tapRange         float64 // at/under this range a tap (min charge) is enough
	fullRange        float64 // at/over this range aim for a full-power charge
	shootAlignRad    float64 // alignment tolerance (radians) the shot must reach before releasing
	shootAlignMaxRad float64 // widest alignment tolerance (radians) once a shot has fully relaxed
	cornerInset      float64 // base safety margin inside the goal opening the shot aims (world units)
	cornerRangeInset float64 // extra aim margin added at max shooting range (world units), scaled by distance
	shootBallSide    float64 // min alignment of ball-side with goal to commit a shot (else reposition)
	shootOpenBonus   float64 // flat utility bonus for a clear (open) shot, so good chances are taken
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
	passLaunchVelComp float64 // 0..1: fraction of the ball's existing along-target velocity subtracted from the pass impulse, so the TOTAL launch (impulse ADDS to ball velocity in sim) lands on the calibrated speed instead of overshooting
	// bestPass scoring weights (per unit of each term) for ranking pass candidates.
	passAdvanceWeight float64 // weight on goalward advancement of the pass
	passSpaceWeight   float64 // weight on the receiver's open space
	passSafetyWeight  float64 // weight on the lane-safety margin
	passOpenWeight    float64 // weight on how long the receive spot stays open
	passRecycleCap    float64 // score ceiling for a non-forward (recycle) pass, so it never out-rates real progress
	recycleFreely     bool    // offer a safe lateral/back (recycle) pass as a standing retention option, not only under pressure: velComp makes a back/lateral pass launch at the soft calibrated speed (only FORWARD passes hit the hot ~201 floor), so recycling to keep possession completes cleanly -- true tiki-taka. Capped by passRecycleCap so it never out-rates real forward progress (shots/scored guard against degenerate backward passing).

	// Off-ball receiving movement.
	supportHoldBallMove float64 // a standing supporter HOLDS its receiving spot (presents a stationary target so passes don't over-hit a drifting receiver) until the ball moves more than this from where the spot was picked, or the spot stops being safe/open -- then it re-picks
	supportForwardBias  float64 // upfield bias of a supporting receiver's search (world units)
	supportRangeFrac    float64 // distance the designated short supporter holds from the ball, as a fraction of pitch width
	runForwardBias      float64 // stronger upfield bias for a give-and-go run after passing

	receiveControlFrac   float64 // the receiver meets an incoming pass where it has slowed to this FRACTION of its own CaptureSpeed (the stick-speed); derived from the live capture so it auto-tracks the capture physics rather than a stale constant. See AI.receiveControlSpeed.
	receiveMinSpeed      float64 // ball speed above which a loose ball counts as an in-flight pass to glide onto (vs a near-stopped ball to win)
	receiveDeepenHot     bool    // when a hot pass never slows below clean-capture within reach, meet it at the DEEPEST reachable point (running WITH the ball, low relative impact) instead of the earliest (near head-on, high relative impact that bounces off)
	receiveMatch         bool    // steer a receiver to run ALONG the ball's line (moving WITH the ball, low relative impact) instead of across/into it -- fixes the receiver overshooting/mis-aligning and the ball bouncing off or sailing past. See steerReceive.
	receiveThrottleFloor float64 // throttle floor while pace-matching a slower ball, so the receiver eases to the ball's pace but never stalls into a head-on contact
	receiveSlowRadius    float64 // off-line distance over which the receiver blends from "run onto the ball's line" to "run along it" (with the ball)
	receiveOntoMax       float64 // cap on the sideways "onto the line" pull (0..1), so there is ALWAYS a with-the-ball forward component -- never a pure sideways/backward lunge into the ball

	recvLaneWeight    float64 // weight on a clear pass lane from the ball when picking a spot
	recvSpaceWeight   float64 // weight on local open space when picking a receive spot
	recvAdvanceWeight float64 // weight on goalward advancement when picking a receive spot
	oneTwoTicks       uint64  // ticks a player keeps making its give-and-go run after passing

	separationRadius      float64 // an off-ball player repels its movement from teammates within this gap (world units)
	separationGain        float64 // strength of that repulsion blended into the move direction (0 = off)
	separationMinThrottle float64 // throttle floor so an idle-but-crowded off-ball player still drifts apart

	// Pressure / decisions.
	pressureRadius float64 // an opponent within this distance applies pressure
	shieldPressure float64 // pressure above which the carrier shields/clears instead of dwelling
	clearThird     float64 // normalized depth of own goal under which a clearance is favoured
	clearCharge    float64 // charge for a clearance -- low, so it fires quickly
	clearAlignRad  float64 // alignment tolerance for a clearance -- wide, so it boots it away fast
	// Middle-click push (instant, no-charge, no-aim radial jab) usage.
	pushPressure        float64 // pressure above which the carrier/keeper jabs the ball away (push) instead of charging a clear/shot
	pushClearMinForward float64 // min radial component along our attack axis for a push to count as clearing (never push it back toward our own goal)
	pokeSteal           bool    // the presser may nick the ball off an opponent it is pressing with a quick middle-click poke (a tackle), when a push reaches it and the radial sends it AWAY from our goal
	pokeStealRange      float64 // surface gap within which the presser pokes an enemy-controlled ball (very close -- a real tackle, not a hopeful lunge)
	settlePossession    float64 // build possession to this before shooting/passing (don't kick a loose touch)
	settleThrottle      float64 // throttle while nursing a fresh touch into control
	actPressure         float64 // pressure above which the carrier must act now instead of settling
	stickBonus          float64 // hysteresis bonus to repeating last tick's on-ball action, to stop flip-flopping
	recoverThrottle     float64 // throttle scale while recovering a loose ball (trap-first, ease the chase)
	kickCooldownTicks   uint64  // ticks after a kick during which the player dribbles, not kicks
	// Hold-time release valve: a carrier that has been on the ball too long must MOVE IT ON (a safe
	// pass/offload) rather than dribble forever. A pressure ramps 0->1 from holdEaseTicks to
	// holdForceTicks; as it rises it allows a recycle, relaxes the pass gates toward a safety floor,
	// and boosts the best pass over the dribble baseline. Most holds are short and untouched; only a
	// stuck carrier feels it. kickCooldownTicks (the ~0.37s post-kick dribble) is unaffected.
	holdEaseTicks    uint64  // hold ticks at which the release valve starts ramping (most holds finish before this)
	holdForceTicks   uint64  // hold ticks at which the valve fully forces an offload (the hoarding cap)
	passHoldUrgency  float64 // score the best pass gains at full hold pressure, so it decisively beats the 1.0 dribble baseline
	holdSpaceFloor   float64 // receiver-space requirement at full hold pressure (relaxed from passReceiverSpace, never below this -- still a real option, not a hoof)
	holdContestFloor float64 // contest margin at full hold pressure (relaxed from passContestMargin toward this, never gifting a clear interception)
	shootHurryWindow float64 // open-window (seconds) under which the shot is hurried (less charge)
	contestMargin    float64 // intercept-time margin within which the ball is "contested" (don't trap)
	maxChargeTicks   uint64  // give-up timeout: ticks a charge can run before the attempt is abandoned
	aimRelaxTicks    uint64  // after charge, ticks over which the aim tolerance relaxes if not lined up
	turnTrapRad      float64 // dribble heading change above which the player traps and eases the turn
	maxTurnRad       float64 // max facing change per decision with a settled ball (anti-fling)
	minTurnRad       float64 // max facing change per decision with a loose ball (it lags more, turn gentler)
	recoverConeRad   float64 // front half-angle the ball must stay within; past it the AI scoops it back to the front (recovery state + dribble turn cap)
	dribbleWallAvoid float64 // penalty weight steering a dribble heading away from carrying the ball into a wall

	// Trap usage.
	trapReceiveFactor float64 // trap to receive once an incoming ball's closing speed exceeds capture*this
	trapReceiveRange  float64 // surface gap within which the receiver sets trap for a clean touch
	stealRange        float64 // trap to steal when within this gap of an enemy fresh-touch ball
	prechargeETA      float64 // seconds-to-ball under which the presser pre-charges a clearance

	// Goalkeeper.
	keeperDepthMin    float64 // closest the keeper sits to its own goal line (world units)
	keeperDepthMax    float64 // furthest out the keeper advances to cut the angle
	keeperSweepBox    float64 // sweep loose balls within this multiple of the goal-area depth
	keeperSaveSpeed   float64 // ball speed toward goal above which the keeper anticipates the save
	keeperSweepMargin float64 // intercept-time margin the keeper must beat opponents by to sweep
}

// defaultAITuning returns the baseline behavioural tuning.
func defaultAITuning() aiTuning {
	pt := config.DefaultPlayerTuning()
	return aiTuning{
		arriveRadius: 6,
		slowRadius:   40,

		avoidRadius:  42,
		avoidLateral: 0.9,
		avoidPushoff: 6,
		avoidPush:    0.8,

		interceptStep:    0.05,
		assumedOppSpeed:  pt.MaxSpeed, // shared field-player top speed; a controller may not read another player's hidden tuning, so it assumes the nominal value
		assumedOppTurn:   pt.TurnRate, // shared field-player turn rate (same rationale)
		leadGain:         0,           // no lead: a mate's velocity is hidden, so aim at where it IS (set >0 to lead along its visible facing)
		interceptHorizon: 2.5,
		interceptQuantum: 0.05,
		turnPenaltyGain:  0.40,

		defenderDepth:   0.22,
		midfielderDepth: 0.51, // midway between the defender and attacker bands -- matches the old interpolated middle-line depth, so introducing role-keyed depths is shape-preserving before any band is retuned
		forwardDepth:    0.80,
		widthMin:        0.14,
		widthMax:        0.86,
		ballShiftX:      0.32,
		ballShiftY:      0.55,
		attackBias:      0.12,
		defendBias:      0.14,
		slotJitter:      14,

		shootRange:       360,
		tapRange:         120,
		fullRange:        260,
		shootAlignRad:    0.06981317007977318, // 4deg: tighter lineup so corner shots fly true
		shootAlignMaxRad: 0.10471975511965977, // 6deg: still relaxes to fire if the lineup drags
		cornerInset:      4,
		cornerRangeInset: 16,
		shootBallSide:    -0.1,
		shootOpenBonus:   0.6,
		minShootCharge:   0.5,

		passMinAdvance:    40,
		passRiskMargin:    0.12,
		passReachMargin:   0.4,
		passContestMargin: 0.6, // raised 0.3->0.6: an opponent reaching the target within this margin of our man kills the pass -- the AI only plays balls the receiver clearly wins, so far fewer are cut out (lifts completion past the 70% target)
		passForwardBonus:  0.5,
		passSafetyMin:     0.16,
		passReceiverSpace: 30,
		throughDist:       110,
		passArriveSpeed:   175, // a pass arrives gently here, well under the live ~276 capture, so it STICKS on the receiver's first touch instead of skidding through. Kept at 175 (not softened further): with the buffed capture a firm-ish ball already sticks, and a robust 30-seed sweep showed softening the LAUNCH actually LOWERS completion (a slower ball spends longer in the lane, so it is more interceptable -- see laneSafe). The soft FEEL of a short ball instead comes from the RECEIVER meeting it deeper on its path (receivePoint) where it has slowed. NOTE the hard launch floor: a charged kick can't fire slower than the ~201 tap (Shoot.Front*MinShootFactor=575*0.35), so a SHORT pass fires at that floor regardless of this value. (TestPassCompletionLargeMap's 6-seed metric is chaotic -- validate tuning over >=30 seeds.)
		passSpeedMin:      150, // floor on the calibrated launch speed; moot below the ~201 tap floor (the kick can't fire slower than a tap), kept as a sane lower bound
		passSpeedMax:      430,
		passDistPenalty:   0.0004,
		passLaunchVelComp: 1.0, // fully compensate: the sim ADDS the impulse to the ball's current velocity, so a pass off a moving/dribbled ball would launch ~2-3x passSpeedFor (the dominant over-hit / too-fast-to-control cause). Subtracting the existing along-target pace makes the TOTAL launch match the calibrated arrive speed. Floored by the ~201 tap, so a very fast ball still can't be passed gently (settle first).
		passAdvanceWeight: 0.009,
		passSpaceWeight:   0.006,
		passSafetyWeight:  0.5,
		passOpenWeight:    0.4,
		passRecycleCap:    1.12,
		recycleFreely:     false, // OFF: a back/lateral pass off a forward-dribbled ball picks up the ball's perpendicular momentum and drifts off-line (under-hits), so freely recycling raised volume but LOWERED the completion rate over a 30-seed sweep. Kept as a tunable -- only the FORWARD-momentum case misfires; from a settled ball recycling is clean.

		supportHoldBallMove:  50, // hold the receiving spot until the ball has moved ~50u from where it was picked (then re-pick) -- a stationary target so passes land true; best of a 30-seed sweep (+~5% completion, tighter variance, fewer total fails vs re-picking every tick)
		supportForwardBias:   40,
		supportRangeFrac:     0.3,
		runForwardBias:       150,
		receiveControlFrac:   0.88, // meet the pass where it has slowed to 88% of the stick-speed -- well under capture so it sticks, tracking the live CaptureSpeed (so a buffed capture lets the receiver take it sooner/faster automatically)
		receiveMinSpeed:      90,   // a loose ball faster than this is an in-flight pass to glide onto (lowered 110->90 so a softened, decaying pass keeps registering as a ball to run onto, not flip to "loose ball, charge it head-on")
		receiveDeepenHot:     true, // meet a too-fast pass deep (running with the ball) so the relative impact is low and it sticks -- targets the dominant receiver-miscontrol failures created by the ~201 launch floor
		receiveMatch:         true, // run along the ball's line (with the ball) so the relative impact is low and the ball sticks -- the scrub showed receivers mis-aligned (moving across/into the ball, align ~0.5 or negative) which is the real overshoot/over-hit/miscontrol cause
		receiveThrottleFloor: 0.35, // a paced receiver still moves at >=35% pace so it is travelling (not stationary) at contact -- a stationary receiver takes the ball head-on (high relative impact)
		receiveSlowRadius:    50,   // within ~50u off the line, blend from running onto the line to running along it
		receiveOntoMax:       0.55, // sweet spot from a 30-seed sweep: enough sideways pull to align the receiver with the ball (over-hit/overshoot down, completion +~6%) without over-retaining -- 0.7 raised completion more but pulled receivers so onto the line that the team stopped creating shots (goals 2.0->1.5); 0.55 holds scoring

		recvLaneWeight:        60,
		recvSpaceWeight:       0.6,
		recvAdvanceWeight:     0.45,
		oneTwoTicks:           50,
		separationRadius:      44, // just above the 36 contact gap: only fires to avert a near-collision
		separationGain:        0.6,
		separationMinThrottle: 0.3, // a near-collision pulls an idle player off the spot to step apart

		pressureRadius:      70,
		shieldPressure:      0.55,
		clearThird:          0.32,
		clearCharge:         0.45,
		clearAlignRad:       0.4886921905584123,
		pushPressure:        0.62,  // an opponent within ~46u (centre-to-centre): boot it now, no time to charge
		pushClearMinForward: 0.15,  // only push-clear when the radial sends the ball clearly upfield/wide
		pokeSteal:           false, // OFF: a poke-tackle is a visible use of the middle-click ability, but over 50 seeds it dragged pass completion (71.8->69.8) and goals (1.7->1.5) -- winning the ball with a hopeful jab starves our own build-up. Kept as a tunable; the AI still uses middle-click for quick clears/close shots under pressure (pushClears/pushShotOn).
		pokeStealRange:      16,    // only at very close range (a real tackle), so the radial reliably sends the ball upfield off the opponent
		settlePossession:    0.45,
		settleThrottle:      0.72,
		actPressure:         0.55,
		stickBonus:          0.15,
		recoverThrottle:     0.6,
		kickCooldownTicks:   22,
		holdEaseTicks:       150, // ~2.5s: holds up to ~2.5s are fine (the user's "3s most of the time"); the valve only starts ramping past this
		holdForceTicks:      240, // ~4s: by here the carrier is forced to offload -- caps the hoarding (the "holds 10s" bug); forcing this soon kept the worst-case single hold well down in sweeps
		passHoldUrgency:     1.0, // at full hold pressure the best pass gains +1.0, decisively beating the 1.0 dribble baseline so the ball moves on
		holdSpaceFloor:      12,  // a forced offload still needs ~12u of space (a real teammate, not a hoof into nobody)
		holdContestFloor:    0.2, // relax the contest margin toward 0.2 (from 0.6) under hold pressure -- play a tighter but still-winnable ball, never an obvious interception
		shootHurryWindow:    0.45,
		contestMargin:       0.1,
		maxChargeTicks:      96,
		aimRelaxTicks:       22,
		turnTrapRad:         0.4363323129985824,
		maxTurnRad:          0.22689280275926285,
		minTurnRad:          0.08726646259971647,
		recoverConeRad:      0.8726646259971648, // ~50deg: ball past this off-front -> scoop it back
		dribbleWallAvoid:    3.0,

		trapReceiveFactor: 0.4,
		trapReceiveRange:  44,
		stealRange:        10,
		prechargeETA:      0.33,

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

// ValidSkill reports whether s names a known difficulty tier (any accepted alias, or the
// empty/"default" string). It is the single source of truth for difficulty validation -- the
// cmd layer calls it instead of config keeping its own hand-copied copy of the name set
// (config cannot import control without an import cycle).
func ValidSkill(s string) bool {
	_, ok := SkillFromString(s)
	return ok
}

// SkillNames returns the canonical tier names for help text and validation messages.
func SkillNames() []string {
	return []string{"easy", "normal", "hard", "impossible"}
}
