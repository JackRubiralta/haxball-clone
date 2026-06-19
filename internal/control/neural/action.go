package neural

// The factored discrete action space, shared by the runtime decoder, the datagen label
// discretizer, and (via dataset_meta.json) the Python policy heads. Five independent
// categorical heads; total logit width = 9+3+16+4+2 = 34.
const (
	MoveDirBins     = 8               // 8 compass directions in the egocentric frame
	MoveHeadSize    = MoveDirBins + 1 // + 1 idle slot
	ThrottleBins    = 3               // {0.0, 0.5, 1.0}
	AimBins         = 16              // relative-to-facing angle bins within +/- AimArcMax
	AbilityHeadSize = 4               // {none, shoot-hold, trap, push}
	CancelHeadSize  = 2               // {no, cancel-charge}
	IdleMove        = MoveDirBins     // index of the "stand still" move slot
)

// Ability head indices.
const (
	AbilNone  = 0
	AbilShoot = 1
	AbilTrap  = 2
	AbilPush  = 3
)

// AimArcMax is the half-width (radians) of the relative-aim arc the AimBin head spans. Setting
// it to the AI's per-decision turn cap (control.DefaultMaxTurnRad) makes a snap-turn STRUCTURALLY
// impossible: the head can only ever request a facing change of at most this many radians per
// tick, so the policy cannot reverse instantly regardless of its weights. Kept in sync with
// control.DefaultMaxTurnRad (defaultAITuning().maxTurnRad = 0.22689280275926285).
const AimArcMax = 0.22689280275926285

// HeadSizes returns the factored head widths in order. It is the single source of truth the
// weight file, the decoder, and the Python trainer must all agree on.
func HeadSizes() []int {
	return []int{MoveHeadSize, ThrottleBins, AimBins, AbilityHeadSize, CancelHeadSize}
}

// TotalLogits is the summed width of all heads.
func TotalLogits() int {
	t := 0
	for _, s := range HeadSizes() {
		t += s
	}
	return t
}

// headOffsets returns cumulative head offsets: head h occupies logits[off[h]:off[h+1]].
func headOffsets() []int {
	hs := HeadSizes()
	off := make([]int, len(hs)+1)
	for i, s := range hs {
		off[i+1] = off[i] + s
	}
	return off
}
