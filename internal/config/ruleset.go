package config

import (
	"fmt"
	"strings"
)

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

	// Positional rules (off by default).
	OffsideEnabled bool            // anti-camp: hold a player behind the offside line
	OffsideFrac    float64         // line position as a fraction of the pitch from a team's own goal
	GKBoxEnabled   bool            // limit how many of a team's players may sit in its goal area
	GKBoxMax       int             // max players allowed in the goal area at once
	Enforcement    EnforcementMode // how violations are corrected
	EvictGrace     float64         // seconds of tolerance before a warn-evict clamp
}

// DefaultRuleset returns the friendly, never-ending ruleset that matches the original
// game: no win condition, no clock, a three-second kickoff countdown.
func DefaultRuleset() Ruleset {
	return Ruleset{
		Win:                WinFriendly,
		CelebrationSeconds: 3.0,
	}
}

// QuickRuleset wins for the first team to reach target goals (default 3).
func QuickRuleset(target int) Ruleset {
	if target < 1 {
		target = 3
	}
	r := DefaultRuleset()
	r.Win = WinFirstToScore
	r.ScoreTarget = target
	return r
}

// TimedRuleset plays for seconds of regulation; whoever leads at the whistle wins, and a
// level score is a draw (unless an OnDraw chain is added).
func TimedRuleset(seconds float64) Ruleset {
	r := DefaultRuleset()
	r.Win = WinTimed
	r.RegulationSeconds = seconds
	return r
}

// GoldenGoalRuleset wins for the first goal from kickoff.
func GoldenGoalRuleset() Ruleset { return QuickRuleset(1) }

// CupRuleset is a timed match that, if drawn, plays extra time and then a penalty
// shootout.
func CupRuleset(seconds float64) Ruleset {
	r := TimedRuleset(seconds)
	r.OnDraw = []Continuation{ContinueExtraTime, ContinuePenalties}
	r.ExtraTimeSeconds = seconds / 3
	r.Penalties = DefaultPenalties()
	return r
}

// RulesetForMode builds the base ruleset for a named mode. The draw-decider chain and
// the positional rules are layered on by the caller (the CLI flags or the menu).
func RulesetForMode(mode string, minutes float64, winScore int) (Ruleset, error) {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", "friendly":
		return DefaultRuleset(), nil
	case "quick":
		return QuickRuleset(winScore), nil
	case "timed":
		return TimedRuleset(minutes * 60), nil
	case "cup":
		return CupRuleset(minutes * 60), nil
	case "golden", "golden-goal":
		return GoldenGoalRuleset(), nil
	default:
		return Ruleset{}, fmt.Errorf("unknown mode %q (want friendly, quick, timed, cup, or golden)", mode)
	}
}
