package sim

// Phase is the current stage of a match.
type Phase int

const (
	PhasePlaying    Phase = iota // normal play (regulation)
	PhaseExtraTime               // an extra-time period after a drawn regulation
	PhaseGoldenGoal              // sudden death: the next goal wins
	PhasePenalties               // a playable penalty shootout
	PhaseFinished                // the match is over
)

// String returns the scoreboard label for a phase (empty during ordinary play).
func (p Phase) String() string {
	switch p {
	case PhaseExtraTime:
		return "EXTRA TIME"
	case PhaseGoldenGoal:
		return "GOLDEN GOAL"
	case PhasePenalties:
		return "PENALTIES"
	case PhaseFinished:
		return "FULL TIME"
	default:
		return ""
	}
}

// MatchState tracks where a match is in its rules progression.
type MatchState struct {
	Phase      Phase
	PhaseStart float64 // m.Clock when the current phase began (for period timing)
	ChainIndex int     // index into Ruleset.OnDraw while resolving a draw
	Winner     Side    // set when Phase is PhaseFinished (SideNone is a draw)
}
