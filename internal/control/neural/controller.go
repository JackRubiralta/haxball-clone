// Package neural is the neural-network game controller: a drop-in control.Controller /
// netcode.Bot that observes only sim.View and acts only through sim.Intent, exactly like the
// rule-based AI, so the simulation cannot tell it from a human and it passes the same
// AI<=human boundary. It featurizes the view (egocentric Deep-Sets blocks), runs a pure-Go
// forward pass (internal/policy), and decodes factored discrete heads into an Intent with a
// structurally turn-rate-limited aim, ability masking, and a multi-tick shoot/cancel state.
package neural

import (
	"fmt"
	"math"

	"phootball/internal/control"
	"phootball/internal/geom"
	"phootball/internal/policy"
	"phootball/internal/sim"
)

// teleportStep is the per-tick position jump above which the velocity memory treats a move as a
// teleport (e.g. a kickoff reset) rather than real motion. At max speed a player covers ~2.3
// world units per tick, so 64 is comfortably above real motion and below any reset jump.
const teleportStep = 64.0

// Controller is a single player's neural controller. A read-only *policy.Net is shared across
// all controllers; each Controller owns its Workspace, velocity memory, charge state, and
// feature scratch, so they are independent and allocation-free per tick.
type Controller struct {
	id  int
	net *policy.Net // nil for a featurize-only controller (NewFeaturizer)
	ws  *policy.Workspace
	mem *frameMemory

	headOff []int
	frame   egoFrame
	dtCache float64

	// feature scratch (sized once)
	selfBuf, ballBuf, globalBuf []float32
	teamBuf, oppBuf             []float32
	nTeam, nOpp                 int
	mates, opps                 []sim.ObservedView

	// cross-tick shoot state (thin assist). A per-tick policy rarely sustains a charge cleanly, so
	// once the net decides to shoot (with the ball under control) the engine HOLDS the charge at the
	// human per-tick rate -- exactly as a human holds the mouse button -- for at least a useful
	// minimum, then keeps holding while the net keeps asking and releases (fires) when the net stops
	// or the full-power cap is hit. The net controls START, HOLD-DURATION (= shot power, so it can
	// play soft short passes or hard long ones), RELEASE, and AIM (via the AimBin head, throughout) --
	// the engine only guarantees the hold isn't broken by per-step flicker. No auto-aim: the net aims.
	charging           bool
	chargeStart        uint64
	shootCooldownUntil uint64
}

// ValidateNet checks that a loaded net's dims and head sizes match this package's feature and
// action constants, so a mismatched weight file is rejected loudly at startup rather than
// silently misread.
func ValidateNet(n *policy.Net) error {
	if n.EntDim != EntDim || n.SelfDim != SelfDim || n.BallDim != BallDim || n.GlobalDim != GlobalDim {
		return fmt.Errorf("neural: net dims (ent=%d self=%d ball=%d global=%d) != feature dims (%d/%d/%d/%d)",
			n.EntDim, n.SelfDim, n.BallDim, n.GlobalDim, EntDim, SelfDim, BallDim, GlobalDim)
	}
	want := HeadSizes()
	if len(n.HeadSizes) != len(want) {
		return fmt.Errorf("neural: net has %d heads, want %d", len(n.HeadSizes), len(want))
	}
	for i := range want {
		if n.HeadSizes[i] != want[i] {
			return fmt.Errorf("neural: head %d size %d != %d", i, n.HeadSizes[i], want[i])
		}
	}
	return nil
}

func (c *Controller) allocFeatures() {
	c.selfBuf = make([]float32, SelfDim)
	c.ballBuf = make([]float32, BallDim)
	c.globalBuf = make([]float32, GlobalDim)
	c.teamBuf = make([]float32, MaxTeammates*EntDim)
	c.oppBuf = make([]float32, MaxOpponents*EntDim)
	c.mates = make([]sim.ObservedView, 0, MaxTeammates+1)
	c.opps = make([]sim.ObservedView, 0, MaxOpponents+1)
	c.mem = newFrameMemory(teleportStep)
	c.headOff = headOffsets()
}

// New builds a full neural controller for player id over the shared net. The net must already
// satisfy ValidateNet. The returned *Controller satisfies both control.Controller and
// netcode.Bot (one method, Intent).
func New(id int, net *policy.Net) *Controller {
	c := &Controller{id: id, net: net}
	c.allocFeatures()
	c.ws = net.NewWorkspace()
	return c
}

// NewFeaturizer builds a controller that can Featurize/Discretize but not act (no net). Used by
// cmd/datagen, which generates observations while the teacher AI supplies the actions.
func NewFeaturizer(id int) *Controller {
	c := &Controller{id: id}
	c.allocFeatures()
	return c
}

// dt estimates the simulation timestep from the match clock, mirroring control/ai.go, so
// velocity recovery matches the real tick rate. Falls back to 1/60.
func (c *Controller) dt(view sim.View) float64 {
	if view.Tick() > 0 && view.Clock() > 0 {
		return view.Clock() / float64(view.Tick())
	}
	return 1.0 / 60.0
}

// Intent decides this player's action for the tick. A nil/foreign view (or one without this
// player) yields an idle intent rather than panicking, mirroring control/ai.go.
func (c *Controller) Intent(view sim.View) sim.Intent {
	if view == nil || c.net == nil {
		return sim.Intent{}
	}
	me, ok := view.Me(c.id)
	if !ok {
		return sim.Intent{}
	}

	c.build(view, me)
	logits := c.net.Forward(c.ws, c.selfBuf, c.ballBuf, c.globalBuf,
		c.teamBuf[:c.nTeam*EntDim], c.oppBuf[:c.nOpp*EntDim])
	return c.finishIntent(view, me, c.decode(logits, me))
}

// finishIntent applies the shared post-decode pipeline: the give-up charge timeout, the
// capability boundary (at most one ability; off-ball aim that cannot snap-turn, mirrored from
// control/ai.go), and the self-clamp for the local path. The relative-aim head already bounds
// the turn to AimArcMax, so CapAim is a belt-and-suspenders no-op while AimArcMax <=
// DefaultMaxTurnRad.
func (c *Controller) finishIntent(view sim.View, me sim.SelfView, in sim.Intent) sim.Intent {
	in = c.applyShootCommit(view, me, in)
	in = control.EnforceAbilityExclusivity(in)
	if !c.charging && c.gapToBall(view, me) > control.AimCapGap {
		in = control.CapAim(in, me.Position(), me.Facing(), control.DefaultMaxTurnRad)
	}
	return selfClamp(in)
}

// Featurize fills the controller's feature buffers for this tick (updating velocity memory) and
// returns the block slices for policy.Net.Forward. cmd/env calls this to send observations to the
// learner, then ActFromIndices with the learner's chosen action. Returned slices alias internal
// scratch, valid until the next Featurize/Intent call.
func (c *Controller) Featurize(view sim.View, me sim.SelfView) (self, ball, global, team, opp []float32) {
	c.build(view, me)
	return c.selfBuf, c.ballBuf, c.globalBuf, c.teamBuf[:c.nTeam*EntDim], c.oppBuf[:c.nOpp*EntDim]
}

// ActFromIndices turns externally-chosen factored head indices into a sim.Intent, applying the
// same decode + boundary pipeline as Intent. It assumes Featurize/FeaturizeFlat was already
// called this tick (so the egoframe is current). Used by cmd/env for the learner-driven agents.
func (c *Controller) ActFromIndices(view sim.View, me sim.SelfView, idx [5]int) sim.Intent {
	return c.finishIntent(view, me, c.decodeIndices(me, idx))
}

// ActionMaskBytes returns a bit-packed legality mask over the TotalLogits action slots (bit set =
// legal), so the learner never wastes probability on impossible actions. Move/throttle/aim are
// always legal; trap/push are masked off unless the ball is within reach; cancel is masked off
// unless a shot charge is live. It assumes Featurize was called this tick.
func (c *Controller) ActionMaskBytes(view sim.View, me sim.SelfView) []byte {
	n := TotalLogits()
	mask := make([]byte, (n+7)/8)
	set := func(i int) { mask[i/8] |= 1 << (uint(i) % 8) }
	off := c.headOff
	for i := off[0]; i < off[3]; i++ { // move, throttle, aim: all legal
		set(i)
	}
	set(off[3] + AbilNone)
	set(off[3] + AbilShoot)
	if c.gapToBall(view, me) <= me.Tuning().PullRange+4 {
		set(off[3] + AbilTrap)
		set(off[3] + AbilPush)
	}
	set(off[4] + 0)
	if me.ShootCharge() > 0 {
		set(off[4] + 1)
	}
	return mask
}

// Thin-assist shoot tuning. The net controls power via how long it holds shoot; these only bound
// the hold so per-step flicker can't fizzle a charge and a stray charge can't run forever.
const (
	minChargeTicks     = 5    // a started shot holds at least this long (a useful soft tap/pass)
	maxChargeTicks     = 46   // hard cap at ~full power (shootChargeMax 0.75s ~= 45 ticks)
	shootCooldownTicks = 18   // ticks to wait after a shot/abort before starting another
	shootStartGap      = 11.0 // surface gap to the ball needed to START a shot (ball under control)
	shootAbortGap      = 28.0 // if the ball drifts past this gap mid-charge, the shot is aborted
)

// applyShootCommit is the thin shoot assist: the net decides WHEN to start a shot, how LONG to hold
// it (= power, so soft passes vs hard shots), when to RELEASE, and WHERE to aim; the engine only
// holds the charge at the human per-tick rate so a flickering per-step policy can't break it. Once
// charging, it guarantees a minimum useful charge, then keeps holding while the net keeps asking
// (in.ShootHeld) and releases (fires on the edge) when the net stops or the full-power cap is hit.
// The net's aim (in.Aim, from the AimBin head) is preserved throughout -- there is no auto-aim.
func (c *Controller) applyShootCommit(view sim.View, me sim.SelfView, in sim.Intent) sim.Intent {
	tick := view.Tick()
	gap := c.gapToBall(view, me)
	wantShoot := in.ShootHeld
	wantCancel := in.CancelCharge

	if c.charging {
		held := tick - c.chargeStart
		if gap > shootAbortGap { // lost the ball: drop the charge without firing
			c.charging = false
			c.shootCooldownUntil = tick + shootCooldownTicks
			in.ShootHeld = false
			in.CancelCharge = false
			return in
		}
		if held < minChargeTicks { // guarantee a useful charge regardless of per-step flicker
			in.ShootHeld, in.Trap, in.Push, in.CancelCharge = true, false, false, false
			return in
		}
		if wantCancel { // net aborts the shot WITHOUT firing (held+cancel => sim drops the charge)
			c.charging = false
			c.shootCooldownUntil = tick + shootCooldownTicks
			in.ShootHeld, in.CancelCharge, in.Trap, in.Push = true, true, false, false
			return in
		}
		if wantShoot && held < maxChargeTicks { // net keeps charging -- longer hold = more power
			in.ShootHeld, in.Trap, in.Push, in.CancelCharge = true, false, false, false
			return in
		}
		// Release: the net let go (its chosen power) or hit the cap -> fire on the release edge.
		c.charging = false
		c.shootCooldownUntil = tick + shootCooldownTicks
		in.ShootHeld = false
		in.CancelCharge = false
		return in
	}

	if wantShoot && !wantCancel && tick >= c.shootCooldownUntil && gap <= shootStartGap && !in.Trap && !in.Push {
		c.charging = true
		c.chargeStart = tick
		in.ShootHeld = true
		return in
	}
	in.ShootHeld = false // not shooting: never accumulate a stray charge
	return in
}

func (c *Controller) gapToBall(view sim.View, me sim.SelfView) float64 {
	b := view.Ball()
	return geom.Dist(me.Position(), b.Position()) - me.Radius() - b.Radius()
}

// selfClamp mirrors netcode.sanitizeIntent for the local (non-server) path, which does not
// sanitize: it zeroes any non-finite Move/Aim/Throttle and clamps Throttle to [0,1]. A bounded
// float32 net + bounded decode should never produce these, but it is cheap insurance.
func selfClamp(in sim.Intent) sim.Intent {
	if !finite(in.Move.X) || !finite(in.Move.Y) {
		in.Move = geom.Vec{}
	}
	if !finite(in.Aim.X) || !finite(in.Aim.Y) {
		in.Aim = geom.Vec{}
	}
	if !finite(in.Throttle) {
		in.Throttle = 0
	}
	if in.Throttle < 0 {
		in.Throttle = 0
	} else if in.Throttle > 1 {
		in.Throttle = 1
	}
	return in
}

func finite(f float64) bool { return !math.IsNaN(f) && !math.IsInf(f, 0) }
