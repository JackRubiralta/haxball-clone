package config

// WinCondition selects how a match is decided.
type WinCondition int

const (
	// WinFriendly never ends: the match plays on forever (the original behaviour).
	WinFriendly WinCondition = iota
	// WinFirstToScore ends when a team reaches ScoreTarget goals.
	WinFirstToScore
	// WinTimed ends when the regulation clock expires; whoever leads then wins (or the
	// draw is resolved by the OnDraw chain).
	WinTimed
	// WinFirstAndTimed is a hybrid: the match ends EARLY when a team reaches ScoreTarget,
	// OR when the regulation clock expires (whoever leads then wins, otherwise the OnDraw
	// chain resolves the draw) -- whichever comes first.
	WinFirstAndTimed
)

// Continuation is one stage applied, in order, when regulation ends level.
type Continuation int

const (
	ContinueExtraTime  Continuation = iota // play a fixed extra-time period
	ContinueGoldenGoal                     // sudden death: the next goal wins
	ContinuePenalties                      // a penalty shootout
)

// PenaltyRules describes a shootout.
type PenaltyRules struct {
	BestOf      int  // kicks per side in the first round (e.g. 5)
	SuddenDeath bool // after BestOf, continue one pair at a time until decided
}

// EnforcementMode selects how a positional rule (offside, keeper box) is applied.
type EnforcementMode int

const (
	// EnforceClamp pushes an offending player back to the boundary immediately.
	EnforceClamp EnforcementMode = iota
	// EnforceWarnEvict tolerates a brief incursion (EvictGrace seconds) before clamping.
	EnforceWarnEvict
)

// DefaultPenalties is the standard best-of-five shootout with sudden death.
func DefaultPenalties() PenaltyRules { return PenaltyRules{BestOf: 5, SuddenDeath: true} }

// Ruleset is the plain-data description of how a match is run: the win condition, the
// kickoff celebration, and how a draw is resolved. The simulation reads this data; it
// never lives in the renderer or the command line.
type Ruleset struct {
	Win                WinCondition
	ScoreTarget        int     // goals needed to win when Win is WinFirstToScore
	RegulationSeconds  float64 // length of normal time when Win is WinTimed (0 = untimed)
	CelebrationSeconds float64 // pause-free kickoff countdown after a goal

	OnDraw            []Continuation // applied in order if regulation ends level
	ExtraTimeSeconds  float64        // length of one extra-time period
	GoldenGoalSeconds float64        // sudden-death time limit (0 = until a goal is scored)
	Penalties         PenaltyRules

	// Positional rules (off by default). Each box is capped per box; a player counts against a
	// cap the moment ANY part of its body overlaps the box, and a full box becomes a barrier.
	// The defending team (the box's own side) and the opponent (attackers) are capped SEPARATELY;
	// 0 means that side has no limit in that box.
	OffsideEnabled         bool            // anti-camp: hold a player behind the offside line
	OffsideFrac            float64         // line position as a fraction of the pitch from a team's own goal
	PenaltyBoxMaxPlayers   int             // max DEFENDING players allowed in their penalty area (0 = off)
	PenaltyBoxMaxOpponents int             // max OPPONENT (attacking) players allowed in a penalty area (0 = off)
	GoalAreaMaxPlayers     int             // max DEFENDING players allowed in their goal area (0 = off)
	GoalAreaMaxOpponents   int             // max OPPONENT (attacking) players allowed in a goal area (0 = off)
	GoalAreaKeeperOnly     bool            // if set, ONLY the box-owner's keeper may enter the goal area (everyone else, own team and opponents, is walled out); overrides the numeric goal-area caps
	Enforcement            EnforcementMode // how violations are corrected
	EvictGrace             float64         // seconds of tolerance before a warn-evict clamp
}

// DefaultRuleset returns the friendly, never-ending ruleset that matches the original
// game: no win condition, no clock, a three-second kickoff countdown.
func DefaultRuleset() Ruleset {
	return Ruleset{
		Win:                WinFriendly,
		CelebrationSeconds: 3.0,
	}
}

// TimedRuleset plays for seconds of regulation; whoever leads at the whistle wins, and a
// level score is a draw (unless an OnDraw chain is added).
func TimedRuleset(seconds float64) Ruleset {
	r := DefaultRuleset()
	r.Win = WinTimed
	r.RegulationSeconds = seconds
	return r
}

// HybridRuleset is a timed match that also ends early once a team reaches the target
// score: it ends on the target OR the clock, whichever comes first. A level score at the
// whistle is a draw unless an OnDraw chain is added.
func HybridRuleset(seconds float64, target int) Ruleset {
	if target < 1 {
		target = 3
	}
	r := DefaultRuleset()
	r.Win = WinFirstAndTimed
	r.RegulationSeconds = seconds
	r.ScoreTarget = target
	return r
}
