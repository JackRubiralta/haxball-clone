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
	"phootball/internal/scenario"
	"phootball/internal/sim"
)

const (
	opReset     = 0x01
	opStep      = 0x02
	opObs       = 0x03
	opClose     = 0x04
	opClosed    = 0x05
	opScenario  = 0x06 // RESET variant carrying a scenario/drill spec (see scenario.go)
	opTelemetry = 0x07 // request the per-episode tiki-taka telemetry panel -> opTelemetryOut
	opTeleOut   = 0x08
)

const gamma = 0.99 // discount used by the potential-based ball-progress shaping

var le = binary.LittleEndian

type env struct {
	m          *sim.Match
	controlled []int                      // controlled player IDs, sorted
	ctrl       map[int]*neural.Controller // controlled (learner-driven) controllers
	opp        map[int]control.Controller // opponent controllers (rule AI / frozen net / scripted)
	ctrlSide   sim.Side
	ctrlTeam   *sim.Team
	frameSkip  int
	profile    rewardProfile

	// drill identity, so a noScore drill can re-arrange itself if a stray ball crosses the line
	scenKind  int
	scenSeed  int64
	isScen    bool
	needRearm bool // a goal was scored in a noScore drill; restore the drill once celebration ends

	// guided bootstrapping. teachers are built whenever the scenario has a teacher kind; they serve two
	// roles: (1) per-state ADVICE for a BC/kickstart loss -- the durable imitation signal, returned in
	// obs every step so Python can supervise the policy toward the teacher action at its own states;
	// (2) optional annealed action-OVERRIDE -- if this episode was sampled as a demonstration, the
	// teacher DRIVES the players and the executed action is reported (advice == executed then).
	teachers        map[int]*scenario.Actor // controlled-player teachers (nil if scenario has none)
	episodeOverride bool                    // this whole episode is a teacher demonstration (driving)
	execIdx         map[int][5]int          // last executed head indices per controlled agent
	adviceIdx       map[int][5]int          // teacher's recommended head indices this step ({-1..} = none)

	prevGF, prevGA     int
	prevShots, prevSOT int
	prevProg           float64

	// per-tick tracking (updated inside the frame-skip loop) -> event reward + telemetry
	trk   tracker
	tele  telemetry
	evRew float64 // discrete-event reward accumulated since the last obs()
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
		case opScenario:
			e = handleScenario(msg[1:])
			writeMsg(w, e.obs())
		case opStep:
			if e == nil {
				log.Fatal("env: STEP before RESET")
			}
			e.step(msg[1:])
			writeMsg(w, e.obs())
		case opTelemetry:
			if e == nil {
				log.Fatal("env: TELEMETRY before RESET")
			}
			writeMsg(w, e.telemetryMsg())
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

	e := &env{ctrl: map[int]*neural.Controller{}, opp: map[int]control.Controller{}, ctrlSide: ctrlSide, frameSkip: frameSkip}
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
	e.finishSetup(em.M, profileFull())
	return e
}

// finishSetup completes env construction shared by handleReset and handleScenario: it enables
// recording, sorts the controlled IDs, resolves the controlled team, installs the reward profile,
// and primes the reward/telemetry baselines.
func (e *env) finishSetup(m *sim.Match, profile rewardProfile) {
	e.m = m
	e.m.EnableRecording()
	e.profile = profile
	sort.Ints(e.controlled)
	for _, t := range e.m.Teams {
		if t.Side == e.ctrlSide {
			e.ctrlTeam = t
		}
	}
	e.featurizeControlled()
	e.prevGF, e.prevGA = e.scores()
	e.prevShots, e.prevSOT = e.shots()
	e.prevProg = e.ballProgress()
	e.trk = newTracker(e.m, e.ctrlSide)
	e.tele = telemetry{}
	if e.execIdx == nil {
		e.execIdx = make(map[int][5]int, len(e.controlled))
		e.adviceIdx = make(map[int][5]int, len(e.controlled))
	}
	for _, id := range e.controlled {
		e.execIdx[id] = [5]int{}
		e.adviceIdx[id] = noAdvice
	}
}

// noAdvice is the sentinel (all -1) reported when there is no teacher recommendation this step, so
// the BC loss skips that sample.
var noAdvice = [5]int{-1, -1, -1, -1, -1}

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
	// Default the executed indices to what Python sent (identity); the override path below replaces
	// them with the discretized teacher action for an overridden agent. Advice defaults to none.
	for _, id := range e.controlled {
		e.execIdx[id] = idxByID[id]
		e.adviceIdx[id] = noAdvice
	}
	e.evRew = 0
	for s := 0; s < e.frameSkip; s++ {
		view := e.m.View()
		intents := make(map[int]sim.Intent, len(e.controlled)+len(e.opp))
		for _, id := range e.controlled {
			me, ok := view.Me(id)
			if !ok {
				continue
			}
			// Teacher advice (BC label) is sampled on the first sub-tick from the state the policy
			// acted on, whether or not this episode is an override demonstration.
			if s == 0 && e.teachers[id] != nil {
				if adv, ok := e.teachers[id].Advise(view); ok {
					e.adviceIdx[id] = neural.Discretize(view, me, adv)
				}
			}
			if e.episodeOverride && e.teachers[id] != nil {
				// Teacher demonstration: drive the player with the validated scripted teacher
				// (per-tick, exactly as cmd/teachercheck validated it), and report the discretized
				// teacher action (sampled on the first sub-tick) for PPO to imitate.
				ti := e.teachers[id].Intent(view)
				intents[id] = ti
				if s == 0 {
					e.execIdx[id] = neural.Discretize(view, me, ti)
				}
			} else {
				intents[id] = e.ctrl[id].ActFromIndices(view, me, idxByID[id])
			}
		}
		for id, oc := range e.opp {
			intents[id] = oc.Intent(view)
		}
		e.m.Step(intents, eval.DT)
		// Per-tick: detect carrier/ability/positioning events, accumulate the dense event reward
		// and the telemetry counters (so reward() only adds the sparse + potential-based terms).
		e.evRew += e.trk.observe(e.m, &e.profile, &e.tele, intents, e.controlled)
		e.maybeFlagRearm()
	}
	e.maybeRearm()
	e.featurizeControlled()
}

// maybeFlagRearm notes that a goal was scored during a no-score drill, so the drill should be
// restored. We don't re-arrange mid-celebration (the engine's post-goal kickoff would overwrite it);
// maybeRearm does the restore once the celebration countdown has elapsed.
func (e *env) maybeFlagRearm() {
	if !e.isScen || !e.profile.noScore {
		return
	}
	gf, ga := e.scores()
	if gf != e.prevGF || ga != e.prevGA {
		e.needRearm = true
	}
}

// maybeRearm restores a no-score drill after a stray goal once the engine has finished its kickoff
// handling. This closes the reward-hacking shortcut: a keep-away policy gains nothing from scoring
// (goal weight is 0) and cannot even disrupt the drill by booting the ball into the net.
func (e *env) maybeRearm() {
	if !e.needRearm || e.m.Celebrating() {
		return
	}
	scenario.Arrange(e.m, e.scenKind, e.ctrlSide, e.scenSeed)
	gf, ga := e.scores()
	e.prevGF, e.prevGA = gf, ga
	e.prevProg = e.ballProgress() // re-baseline so the restore is not seen as ball progress
	e.needRearm = false
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

// reward computes the shared team reward for this step from the active profile: the dominant
// sparse goal term, potential-based ball-progress shaping, the recorder's shot events, and the
// per-tick dense events accumulated by the tracker (possession, passing, turnovers, dawdle, crowd,
// stay-back, GK-box, push/trap). The whole dense part is clamped below the ±1 goal so it can't be
// farmed (the Google-Research-Football lesson).
func (e *env) reward() float64 {
	pr := &e.profile
	gf, ga := e.scores()
	sparse := pr.goal * float64((gf-e.prevGF)-(ga-e.prevGA))

	prog := e.ballProgress()
	shaped := pr.ballProg * (gamma*prog - e.prevProg) // potential-based: policy-invariant, can't be farmed

	sh, sot := e.shots()
	shotR := pr.shot*float64(sh-e.prevShots) + pr.shotOnTgt*float64(sot-e.prevSOT)
	e.prevShots, e.prevSOT = sh, sot

	dense := shaped + shotR + e.evRew
	if dense > pr.denseClamp {
		dense = pr.denseClamp
	} else if dense < -pr.denseClamp {
		dense = -pr.denseClamp
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
		// Executed action indices (for guided bootstrapping): the action actually applied this step,
		// which equals what Python sent unless this episode is a teacher demonstration.
		ex := e.execIdx[id]
		for h := 0; h < 5; h++ {
			b = appendI32(b, int32(ex[h]))
		}
		// Teacher advice indices (the BC/kickstart label); all -1 when there is no teacher advice.
		ad := e.adviceIdx[id]
		for h := 0; h < 5; h++ {
			b = appendI32(b, int32(ad[h]))
		}
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
func (c *cursor) f32() float32 { return math.Float32frombits(c.u32()) }
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
