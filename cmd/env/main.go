// Command env is a gym-like reinforcement-learning bridge for the neural controller, speaking a
// length-prefixed binary protocol over stdin/stdout (no JSON on the hot path). Go owns the
// simulation, featurization, action decode, opponent policies, and reward; the Python learner
// sends only action indices and receives the next observation, reward, done, and action mask for
// each controlled agent. Opponents run IN-PROCESS in pure Go (rule AI or a frozen weights
// snapshot), so an env worker needs no GPU — many workers feed one GPU learner.
//
// Wire framing: every message is uint32 little-endian length, then that many payload bytes; the
// first payload byte is the opcode. All multi-byte fields are little-endian.
//
//	RESET 0x01: teamSize u8, field u8(0=small,1=med,2=large), offside u8, frameSkip u8,
//	            seed i64, controlledSide u8(0=left,1=right), oppKind u8(0..3 rule tiers, 4 frozen),
//	            [if frozen] pathLen u16 + path bytes                      -> OBS
//	STEP  0x02: per controlled agent (sorted by PlayerID): 5 x i32 head indices   -> OBS
//	CLOSE 0x04:                                                           -> CLOSED 0x05
//	OBS   0x03: numAgents u16; per agent: playerID i32, FlatDim x f32 obs, reward f32, done u8,
//	            maskLen u16 + mask bytes; then tick u32
package main

import (
	"bufio"
	"encoding/binary"
	"io"
	"log"
	"math"
	"os"
	"sort"

	"phootball/internal/config"
	"phootball/internal/control"
	"phootball/internal/control/neural"
	"phootball/internal/eval"
	"phootball/internal/geom"
	"phootball/internal/policy"
	"phootball/internal/sim"
)

const (
	opReset  = 0x01
	opStep   = 0x02
	opObs    = 0x03
	opClose  = 0x04
	opClosed = 0x05
)

// Reward shaping constants. The sparse goal term dominates; every dense term is potential-based
// or a tiny capped event bonus (see plan section 7).
const (
	gamma         = 0.99
	phiWeight     = 0.05 // ball-progress potential weight
	rPassDone     = 0.02
	rTurnover     = -0.03
	rShot         = 0.01
	rShotOnTarget = 0.05
	rPossess      = 0.004 // per-step reward for holding the ball (dense signal; capped by denseClamp)
	denseClamp    = 0.15
)

var le = binary.LittleEndian

type env struct {
	m          *sim.Match
	controlled []int                      // controlled player IDs, sorted
	ctrl       map[int]*neural.Controller // controlled (learner-driven) controllers
	opp        map[int]control.Controller // opponent controllers (rule AI or frozen net)
	ctrlSide   sim.Side
	ctrlTeam   *sim.Team
	frameSkip  int

	prevGF, prevGA     int
	prevShots, prevSOT int
	prevProg           float64
	lastCarrier        sim.Side
}

func main() {
	log.SetFlags(0)
	r := bufio.NewReaderSize(os.Stdin, 1<<16)
	w := bufio.NewWriterSize(os.Stdout, 1<<16)
	var e *env
	for {
		msg, err := readMsg(r)
		if err == io.EOF {
			return
		}
		if err != nil {
			log.Fatalf("env: read: %v", err)
		}
		if len(msg) == 0 {
			continue
		}
		switch msg[0] {
		case opReset:
			e = handleReset(msg[1:])
			writeMsg(w, e.obs())
		case opStep:
			if e == nil {
				log.Fatal("env: STEP before RESET")
			}
			e.step(msg[1:])
			writeMsg(w, e.obs())
		case opClose:
			writeMsg(w, []byte{opClosed})
			w.Flush()
			return
		default:
			log.Fatalf("env: unknown opcode 0x%02x", msg[0])
		}
		if err := w.Flush(); err != nil {
			log.Fatalf("env: flush: %v", err)
		}
	}
}

func handleReset(p []byte) *env {
	cur := newCursor(p)
	teamSize := int(cur.u8())
	fieldIdx := cur.u8()
	offside := cur.u8() != 0
	frameSkip := int(cur.u8())
	if frameSkip < 1 {
		frameSkip = 1
	}
	seed := int64(cur.u64())
	ctrlSideByte := cur.u8()
	oppKind := cur.u8()
	var frozenPath string
	if oppKind == 4 {
		n := int(cur.u16())
		frozenPath = string(cur.take(n))
	}

	ctrlSide := sim.SideLeft
	if ctrlSideByte == 1 {
		ctrlSide = sim.SideRight
	}
	field := fieldPreset(fieldIdx)
	mutate := func(cfg *config.Config) {
		cfg.Geometry = field
		cfg.Ruleset.OffsideEnabled = offside
		if offside && cfg.Ruleset.OffsideFrac == 0 {
			cfg.Ruleset.OffsideFrac = 0.5
		}
	}

	// Controlled controllers (learner) get the embedded net only to size their Workspace; their
	// actions come from Python via ActFromIndices, not the net's argmax. Opponents act on their
	// own (rule AI or a frozen snapshot net).
	embedded, err := policy.LoadDefault()
	if err != nil {
		log.Fatalf("env: load embedded net: %v", err)
	}
	if err := neural.ValidateNet(embedded); err != nil {
		log.Fatalf("env: %v", err)
	}
	var frozenNet *policy.Net
	if oppKind == 4 {
		frozenNet, err = loadFile(frozenPath)
		if err != nil {
			log.Fatalf("env: load frozen %s: %v", frozenPath, err)
		}
		if err := neural.ValidateNet(frozenNet); err != nil {
			log.Fatalf("env: frozen %v", err)
		}
	}

	e := &env{ctrl: map[int]*neural.Controller{}, opp: map[int]control.Controller{}, ctrlSide: ctrlSide, frameSkip: frameSkip, lastCarrier: sim.SideNone}
	em := eval.BuildWith(teamSize, seed, mutate, func(id int, side sim.Side) control.Controller {
		if side == ctrlSide {
			c := neural.New(id, embedded)
			e.ctrl[id] = c
			e.controlled = append(e.controlled, id)
			return c
		}
		var oc control.Controller
		switch oppKind {
		case 4:
			oc = neural.New(id, frozenNet)
		default:
			oc = control.NewAISkill(id, ruleSkill(oppKind))
		}
		e.opp[id] = oc
		return oc
	})
	e.m = em.M
	e.m.EnableRecording()
	sort.Ints(e.controlled)
	for _, t := range e.m.Teams {
		if t.Side == ctrlSide {
			e.ctrlTeam = t
		}
	}

	// Prime obs + reward baselines.
	e.featurizeControlled()
	e.prevGF, e.prevGA = e.scores()
	e.prevShots, e.prevSOT = e.shots()
	e.prevProg = e.ballProgress()
	return e
}

// step applies the learner's action indices (with frame-skip action repeat), advances the sim,
// and refreshes observations.
func (e *env) step(p []byte) {
	cur := newCursor(p)
	idxByID := make(map[int][5]int, len(e.controlled))
	for _, id := range e.controlled {
		var idx [5]int
		for h := 0; h < 5; h++ {
			idx[h] = int(int32(cur.u32()))
		}
		idxByID[id] = idx
	}
	for s := 0; s < e.frameSkip; s++ {
		view := e.m.View()
		intents := make(map[int]sim.Intent, len(e.controlled)+len(e.opp))
		for _, id := range e.controlled {
			me, ok := view.Me(id)
			if !ok {
				continue
			}
			intents[id] = e.ctrl[id].ActFromIndices(view, me, idxByID[id])
		}
		for id, oc := range e.opp {
			intents[id] = oc.Intent(view)
		}
		e.m.Step(intents, eval.DT)
	}
	e.featurizeControlled()
}

// featurizeControlled refreshes each controlled controller's feature buffers (and velocity
// memory) for the current view, so obs() and the next ActFromIndices use the current frame.
func (e *env) featurizeControlled() {
	view := e.m.View()
	for _, id := range e.controlled {
		me, ok := view.Me(id)
		if !ok {
			continue
		}
		e.ctrl[id].Featurize(view, me)
	}
}

// reward computes the shared team reward for this step: sparse goal term + potential-based
// ball-progress shaping + tiny carrier-transition events, with the dense part clamped.
func (e *env) reward() float64 {
	gf, ga := e.scores()
	sparse := float64((gf - e.prevGF) - (ga - e.prevGA))

	prog := e.ballProgress()
	shaped := gamma*phiWeight*prog - phiWeight*e.prevProg

	event := 0.0
	if c := e.m.BallCarrier(); c != nil {
		side := c.Team.Side
		if e.lastCarrier != sim.SideNone && side != e.lastCarrier {
			if side == e.ctrlSide {
				event += rPassDone // we (re)gained possession
			} else if e.lastCarrier == e.ctrlSide {
				event += rTurnover // we lost it
			}
		}
		if side == e.ctrlSide {
			event += rPossess // dense per-step possession signal (sparse goals can't shape 6v6 large)
		}
		e.lastCarrier = side
	}

	// Shot events: directly incentivize getting shots away and on target, so high possession
	// converts into goal threat (the sparse goal term alone is too rare to shape scoring). Tiny,
	// and the whole dense term is clamped, so it can't outweigh actual goals.
	sh, sot := e.shots()
	event += rShot*float64(sh-e.prevShots) + rShotOnTarget*float64(sot-e.prevSOT)
	e.prevShots, e.prevSOT = sh, sot

	dense := shaped + event
	if dense > denseClamp {
		dense = denseClamp
	} else if dense < -denseClamp {
		dense = -denseClamp
	}

	e.prevGF, e.prevGA = gf, ga
	e.prevProg = prog
	return sparse + dense
}

func (e *env) scores() (gf, ga int) {
	for _, t := range e.m.Teams {
		if t.Side == e.ctrlSide {
			gf = t.Score
		} else {
			ga = t.Score
		}
	}
	return
}

// shots returns the controlled team's cumulative Shots and ShotsOnTarget from the recorder.
func (e *env) shots() (shots, sot int) {
	st := e.m.Stats()
	for i := range st.Teams {
		if st.Teams[i].Side == e.ctrlSide {
			return st.Teams[i].Shots, st.Teams[i].ShotsOnTarget
		}
	}
	return 0, 0
}

func (e *env) ballProgress() float64 {
	att := e.m.AttackingGoal(e.ctrlTeam).Center
	def := e.m.DefendingGoal(e.ctrlTeam).Center
	axis := att.Sub(def)
	l2 := geom.Dot(axis, axis)
	if l2 < 1e-9 {
		return 0
	}
	prog := geom.Dot(e.m.Ball.Position.Sub(def), axis) / l2
	if prog < 0 {
		prog = 0
	} else if prog > 1 {
		prog = 1
	}
	return prog
}

// obs builds the OBS message for the current state. Reward is computed once (shared) and attached
// to every controlled agent (parameter sharing, team reward).
func (e *env) obs() []byte {
	rew := float32(e.reward())
	view := e.m.View()
	var b []byte
	b = append(b, opObs)
	b = appendU16(b, uint16(len(e.controlled)))
	for _, id := range e.controlled {
		me, ok := view.Me(id)
		b = appendI32(b, int32(id))
		if !ok {
			// Player gone (shouldn't happen): emit zeros.
			b = appendF32s(b, make([]float32, neural.FlatDim))
			b = appendF32(b, 0)
			b = append(b, 0)
			b = appendU16(b, 0)
			continue
		}
		self, ball, global, team, opp := e.ctrl[id].Featurize(view, me)
		flat := flatten(self, ball, global, team, opp, len(team)/neural.EntDim, len(opp)/neural.EntDim)
		b = appendF32s(b, flat)
		b = appendF32(b, rew)
		b = append(b, 0) // done: friendly match never terminates; Python truncates at horizon
		mask := e.ctrl[id].ActionMaskBytes(view, me)
		b = appendU16(b, uint16(len(mask)))
		b = append(b, mask...)
	}
	b = appendU32(b, uint32(e.m.Tick))
	return b
}

// flatten lays out the block slices into the FlatDim vector exactly as FeaturizeFlat does, so the
// learner sees the same layout as the datagen shards.
func flatten(self, ball, global, team, opp []float32, nTeam, nOpp int) []float32 {
	out := make([]float32, neural.FlatDim)
	off := 0
	off += copy(out[off:off+neural.SelfDim], self)
	off += copy(out[off:off+neural.BallDim], ball)
	off += copy(out[off:off+neural.GlobalDim], global)
	teamBase := neural.SelfDim + neural.BallDim + neural.GlobalDim
	copy(out[teamBase:], team)
	oppBase := teamBase + neural.MaxTeammates*neural.EntDim
	copy(out[oppBase:], opp)
	out[neural.FlatDim-2] = float32(nTeam)
	out[neural.FlatDim-1] = float32(nOpp)
	return out
}

func ruleSkill(kind byte) control.Skill {
	switch kind {
	case 0:
		return control.SkillEasy
	case 1:
		return control.SkillNormal
	case 2:
		return control.SkillHard
	default:
		return control.SkillImpossible
	}
}

func fieldPreset(idx byte) config.Geometry {
	switch idx {
	case 0:
		return config.SmallGeometry()
	case 2:
		return config.LargeGeometry()
	default:
		return config.StandardGeometry()
	}
}

func loadFile(path string) (*policy.Net, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return policy.Load(f)
}

// --- framing & cursor helpers ---

func readMsg(r *bufio.Reader) ([]byte, error) {
	var lenb [4]byte
	if _, err := io.ReadFull(r, lenb[:]); err != nil {
		return nil, err
	}
	n := le.Uint32(lenb[:])
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, err
	}
	return buf, nil
}

func writeMsg(w *bufio.Writer, payload []byte) {
	var lenb [4]byte
	le.PutUint32(lenb[:], uint32(len(payload)))
	if _, err := w.Write(lenb[:]); err != nil {
		log.Fatalf("env: write: %v", err)
	}
	if _, err := w.Write(payload); err != nil {
		log.Fatalf("env: write: %v", err)
	}
}

type cursor struct {
	b   []byte
	off int
}

func newCursor(b []byte) *cursor { return &cursor{b: b} }
func (c *cursor) u8() byte {
	v := c.b[c.off]
	c.off++
	return v
}
func (c *cursor) u16() uint16 {
	v := le.Uint16(c.b[c.off:])
	c.off += 2
	return v
}
func (c *cursor) u32() uint32 {
	v := le.Uint32(c.b[c.off:])
	c.off += 4
	return v
}
func (c *cursor) u64() uint64 {
	v := le.Uint64(c.b[c.off:])
	c.off += 8
	return v
}
func (c *cursor) take(n int) []byte {
	s := c.b[c.off : c.off+n]
	c.off += n
	return s
}

func appendU16(b []byte, v uint16) []byte { return append(b, byte(v), byte(v>>8)) }
func appendU32(b []byte, v uint32) []byte {
	return append(b, byte(v), byte(v>>8), byte(v>>16), byte(v>>24))
}
func appendI32(b []byte, v int32) []byte   { return appendU32(b, uint32(v)) }
func appendF32(b []byte, v float32) []byte { return appendU32(b, math.Float32bits(v)) }
func appendF32s(b []byte, vs []float32) []byte {
	for _, v := range vs {
		b = appendF32(b, v)
	}
	return b
}
