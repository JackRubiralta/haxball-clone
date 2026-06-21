package main

// rewardProfile is the per-scenario weighting of every reward term. The sparse goal term dominates;
// the dense terms are small per-tick (or per-event) bonuses the tracker accumulates, with the whole
// dense sum clamped below the ±1 goal in reward() so none of them can be farmed. A curriculum stage
// selects a profile by id so each drill shapes for its own lesson.
type rewardProfile struct {
	name string

	goal     float64 // sparse: +1 goal for / -1 against (×goal)
	ballProg float64 // potential-based ball-progress weight (the GRF checkpoint idea)

	possess     float64 // per-tick reward while OUR team carries the ball
	turnover    float64 // per-event penalty when we lose possession (negative)
	turnoverOwn float64 // multiplier (>=1) applied when we lose it in our OWN third (worst)

	passBase    float64 // per-completed-pass base reward
	passPerLen  float64 // extra reward per (pass length / field width) -- longer passes are worth more
	passMinLen  float64 // world-unit floor below which a handover is NOT counted as a pass (kills micro-pokes)
	progressive float64 // bonus when a pass advances the ball toward the attacking goal

	dawdleAfter float64 // seconds a single carrier may hold before the dawdle penalty begins
	dawdle      float64 // per-tick penalty while one carrier holds beyond dawdleAfter (negative)

	crowd       float64 // per-tick penalty per teammate beyond crowdMax within crowdRadius of the ball (negative)
	crowdRadius float64
	crowdMax    int

	stayBack float64 // per-tick reward while at least one outfielder holds a deep covering position

	gkBox float64 // per-tick penalty per NON-keeper teammate inside our own goal area (negative)

	shot      float64 // per recorder shot
	shotOnTgt float64 // per recorder shot-on-target

	push float64 // per effective push (a clear/escape that moved a ball in reach)
	trap float64 // per effective trap reception (caught an incoming ball cleanly)

	receive      float64 // ONE-SHOT reward for cleanly receiving a MOVING ball (good first touch)
	feedFloor    float64 // min ball speed (world u/s) for a gained ball to count as a received feed
	forwardAlign float64 // per-tick reward for the carrier moving aligned with its facing (directional)
	releaseMate  float64 // small bonus for releasing the ball toward an open teammate (a pass ATTEMPT)

	// noScore marks a keep-away / possession drill where scoring is NOT the objective. With it set,
	// goal must be 0 (no reward for scoring) AND the env re-arranges the drill if a stray ball crosses
	// the line, so a goal cannot disrupt the rondo via the engine's kickoff reset. This closes the
	// reward-hacking shortcut where a keep-away policy just boots the ball into the net to farm goals.
	noScore bool

	denseClamp float64 // |dense reward| ceiling per step
}

// profileFull is the full tiki-taka objective used for free-play and full-game self-play: keep the
// ball, pass it (longer = better, no micro-pokes), don't dawdle, don't crowd, keep a player back,
// keep the box clear, and finish. Losing it in our own third hurts most.
func profileFull() rewardProfile {
	return rewardProfile{
		name: "full", goal: 1.0, ballProg: 0.05,
		possess: 0.003, turnover: -0.05, turnoverOwn: 2.2,
		passBase: 0.03, passPerLen: 0.06, passMinLen: 70, progressive: 0.02,
		dawdleAfter: 2.5, dawdle: -0.0025,
		crowd: -0.003, crowdRadius: 130, crowdMax: 2,
		stayBack: 0.002,
		gkBox:    -0.004,
		shot:     0.01, shotOnTgt: 0.05,
		push: 0.005, trap: 0.015, // push small -- it's a defensive clear, not a primary action
		receive: 0.04, feedFloor: 80, forwardAlign: 0.001, releaseMate: 0.012,
		denseClamp: 0.15,
	}
}

// profileReceive (collect/trap drills): reward cleanly gaining a moving ball (one-shot first touch)
// and holding it; little else. The lesson is reception, not finishing.
func profileReceive() rewardProfile {
	p := profileFull()
	p.name = "receive"
	p.goal, p.noScore = 0, true // collecting a loose ball is not about scoring; ignore the net
	p.passBase, p.passPerLen, p.progressive, p.shot, p.shotOnTgt, p.crowd, p.stayBack, p.gkBox = 0, 0, 0, 0, 0, 0, 0, 0
	p.ballProg = 0.0 // don't let it farm progress by booting the loose ball goalward
	p.possess = 0.003
	p.receive = 0.10  // dominant: a clean reception edge (speed-drop scaled)
	p.feedFloor = 35  // a friction-slowed rolling ball still counts; a settled ball does not
	p.trap = 0.0      // no per-tick trap farming; receiving is the one-shot edge above
	p.releaseMate = 0 // collecting is not about passing
	p.dawdleAfter, p.dawdle = 3.0, -0.002
	return p
}

// profileHold (shield drill): reward retaining the ball under pressure; dawdle bites only late.
func profileHold() rewardProfile {
	p := profileFull()
	p.name = "hold"
	p.goal, p.noScore = 0, true // shielding drill: scoring is irrelevant, retention is the lesson
	p.passBase, p.passPerLen, p.progressive, p.shot, p.shotOnTgt, p.gkBox = 0, 0, 0, 0, 0, 0
	p.possess = 0.005
	p.receive = 0.04
	p.turnover, p.turnoverOwn = -0.06, 1.0
	p.dawdleAfter, p.dawdle = 3.5, -0.002 // shielding is allowed; only punish holding *forever*
	p.forwardAlign = 0.0
	return p
}

// profileCarry (dribble A->B): reward carrying the ball toward goal (progress) efficiently.
func profileCarry() rewardProfile {
	p := profileFull()
	p.name = "carry"
	p.passBase, p.passPerLen, p.progressive, p.crowd, p.stayBack, p.gkBox = 0, 0, 0, 0, 0, 0
	p.ballProg = 0.12
	p.possess = 0.004
	p.receive = 0.04
	p.forwardAlign = 0.002 // reward moving where you face (fast under the directional model)
	p.shot, p.shotOnTgt = 0.01, 0.04
	p.dawdleAfter, p.dawdle = 2.0, -0.003
	return p
}

// profileShooting (stage 1): get to the ball, advance it, and finish -- shots and goals dominate.
func profileShooting() rewardProfile {
	p := profileFull()
	p.name = "shooting"
	// Motor lesson: carry the ball TOWARD GOAL and finish. Ball-progress + shooting dominate; a small
	// possession term rewards control but is too small to make static hoarding worthwhile, and the
	// dawdle penalty bites quickly so sitting on the ball is punished. No push (it scatters the ball).
	p.possess, p.passBase, p.passPerLen, p.progressive = 0.0015, 0, 0, 0
	p.crowd, p.stayBack, p.gkBox = 0, 0, 0
	p.ballProg = 0.14
	p.dawdleAfter, p.dawdle = 1.0, -0.006
	p.shot, p.shotOnTgt = 0.05, 0.15
	p.push, p.trap = 0.0, 0.02
	return p
}

// profilePassing (stage 2 -- the rondo, the heart of tiki-taka): completed length-gated passes and
// retention dominate; turnovers and dawdling are punished; no shooting bias.
func profilePassing() rewardProfile {
	p := profileFull()
	p.name = "passing"
	// THE rondo fix: scoring is NOT a path to reward here. Previously every profile inherited goal:1.0
	// from profileFull, so the keep-away policy learned to just boot the ball into the net and farm
	// goals (pass/min pinned at 0). Zero the goal term and re-arrange on a stray goal so the ONLY way
	// to earn reward is retention + length-gated passing.
	p.goal, p.noScore = 0, true
	p.ballProg, p.shot, p.shotOnTgt, p.gkBox = 0, 0, 0, 0
	// Passing must STRICTLY dominate hoarding (the run found a hoard local optimum): keep the per-tick
	// possession term small, make a completed pass worth much more, and bite the dawdle penalty sooner
	// and harder so sitting on the ball loses reward. BC toward the teacher is the primary lever; this
	// makes the reward gradient point the same way.
	p.possess = 0.0015
	p.passBase, p.passPerLen, p.progressive = 0.08, 0.14, 0.03
	p.turnover, p.turnoverOwn = -0.05, 1.0 // a little less turnover fear so it dares to pass
	p.dawdleAfter, p.dawdle = 1.0, -0.008  // move the ball on quickly (it's a rondo)
	p.push, p.trap = 0.0, 0.03             // no push (it scatters the ball); value clean reception (trap) most
	p.releaseMate = 0.05                   // reward releasing toward a mate (a pass attempt) -- gradient toward passing
	return p
}

// profilePossession (stage 3): build up and progress the ball through the thirds without losing it;
// an own-third turnover is the worst outcome.
func profilePossession() rewardProfile {
	p := profileFull()
	p.name = "possession"
	p.goal, p.noScore = 0, true // build-up/progression drill: win it by keeping & advancing the ball, not by scoring
	p.possess = 0.004
	p.ballProg = 0.06
	p.passBase, p.passPerLen, p.progressive = 0.025, 0.06, 0.03
	p.turnover, p.turnoverOwn = -0.05, 3.0
	p.dawdleAfter, p.dawdle = 2.2, -0.003
	p.shot, p.shotOnTgt = 0.01, 0.04
	p.push = 0.0 // build out with the feet, not pokes
	return p
}

// profileDefense (stage 4): shape -- don't all crowd the ball, keep a player back, keep the box
// clear (keeper only), and win the ball back; conceding hurts.
func profileDefense() rewardProfile {
	p := profileFull()
	p.name = "defense"
	p.passBase, p.passPerLen, p.progressive, p.shot, p.shotOnTgt = 0, 0, 0, 0, 0
	p.possess = 0.002
	p.turnover, p.turnoverOwn = -0.04, 2.5 // regaining possession (positive on the carrier flip) is the win
	p.crowd, p.crowdRadius, p.crowdMax = -0.006, 130, 2
	p.stayBack = 0.004
	p.gkBox = -0.008
	p.push = 0.03 // clearances
	return p
}

// profileByID maps the scenario's reward-profile byte to a profile.
func profileByID(id byte) rewardProfile {
	switch id {
	case 1:
		return profileShooting()
	case 2:
		return profilePassing()
	case 3:
		return profilePossession()
	case 4:
		return profileDefense()
	case 5:
		return profileReceive()
	case 6:
		return profileHold()
	case 7:
		return profileCarry()
	default:
		return profileFull()
	}
}
