package neural

import (
	"math"
	"sort"

	"phootball/internal/geom"
	"phootball/internal/sim"
)

// Feature block dimensions. These MUST match the embedded net's dims (asserted by ValidateNet)
// and the Python model built from dataset_meta.json. All features come exclusively from the
// View API, so the controller observes only what a human sees (the anti-cheat boundary).
const (
	SelfDim      = 18 // self block (velocity/heading self-only; +trap aura & team buff/debuff)
	BallDim      = 8  // ball block
	GlobalDim    = 12 // global/context block
	EntDim       = 15 // per-entity (teammate/opponent) row (+trap aura, possession, team buff/debuff)
	MaxTeammates = 10 // max teammates in the flat (datagen) layout (11-a-side -> 10)
	MaxOpponents = 11 // max opponents in the flat (datagen) layout

	// FlatDim is the fixed width of the datagen/parity flat featurization:
	// [self | ball | global | MaxTeammates*Ent (zero-padded) | MaxOpponents*Ent | nTeam | nOpp].
	FlatDim = SelfDim + BallDim + GlobalDim + MaxTeammates*EntDim + MaxOpponents*EntDim + 2

	shootChargeNorm = 0.75 // sim.shootChargeMax: seconds of hold for a full shot
	clockRefSeconds = 300  // normalizer for the match clock fraction
)

// egoFrame is an egocentric, attacking-goal-aligned orthonormal frame: origin at the player,
// x-axis toward the goal it is attacking, y-axis the left perpendicular. Encoding everything in
// this frame makes the policy side-symmetric (one weight set serves both teams and both kickoff
// orientations) and removes "which way is goal".
type egoFrame struct {
	origin     geom.Vec
	xhat, yhat geom.Vec
	distScale  float64 // 1 / field width
	speedScale float64 // 1 / max speed
}

func makeFrame(view sim.View, me sim.SelfView) egoFrame {
	field := view.Field()
	w := field.Width()
	if w <= 0 {
		w = 1
	}
	ms := me.Tuning().MaxSpeed
	if ms <= 0 {
		ms = 1
	}
	selfPos := me.Position()
	xhat := geom.Unit(view.AttackingGoalCenter(me).Sub(selfPos))
	if xhat == (geom.Vec{}) {
		xhat = geom.Unit(me.Facing())
		if xhat == (geom.Vec{}) {
			xhat = geom.NewVec(1, 0)
		}
	}
	yhat := geom.NewVec(-xhat.Y, xhat.X)
	return egoFrame{origin: selfPos, xhat: xhat, yhat: yhat, distScale: 1 / w, speedScale: 1 / ms}
}

// localPos maps a world point to normalized egocentric coordinates.
func (f egoFrame) localPos(p geom.Vec) (float32, float32) {
	d := p.Sub(f.origin)
	return float32(geom.Dot(d, f.xhat) * f.distScale), float32(geom.Dot(d, f.yhat) * f.distScale)
}

// localDir maps a world vector to its egocentric unit direction (cos, sin); zero for a zero vec.
func (f egoFrame) localDir(v geom.Vec) (float32, float32) {
	u := geom.Unit(v)
	return float32(geom.Dot(u, f.xhat)), float32(geom.Dot(u, f.yhat))
}

// localVel maps a world velocity to normalized egocentric components.
func (f egoFrame) localVel(v geom.Vec) (float32, float32) {
	return float32(geom.Dot(v, f.xhat) * f.speedScale), float32(geom.Dot(v, f.yhat) * f.speedScale)
}

func boolf(b bool) float32 {
	if b {
		return 1
	}
	return 0
}

func clampMin0(x float64) float64 {
	if x < 0 {
		return 0
	}
	return x
}

func sortByID(s []sim.ObservedView) {
	sort.Slice(s, func(i, j int) bool { return s[i].ID() < s[j].ID() })
}

// build fills the controller's feature buffers from the view. It updates the velocity-estimation
// memory (in ID order, deterministically), computes the egoframe, and fills self/ball/global
// blocks plus the variable teammate/opponent entity rows. It allocates nothing per tick.
func (c *Controller) build(view sim.View, me sim.SelfView) {
	tick := view.Tick()
	c.dtCache = c.dt(view)
	f := makeFrame(view, me)
	c.frame = f
	ball := view.Ball()
	selfPos := me.Position()

	c.mates = append(c.mates[:0], view.Teammates(me)...)
	c.opps = append(c.opps[:0], view.Opponents(me)...)
	sortByID(c.mates)
	sortByID(c.opps)
	for _, o := range c.mates {
		c.mem.observe(o.ID(), o.Position(), tick)
	}
	for _, o := range c.opps {
		c.mem.observe(o.ID(), o.Position(), tick)
	}

	gap := clampMin0(geom.Dist(selfPos, ball.Position()) - me.Radius() - ball.Radius())
	c.fillSelf(view, me, f, gap)
	c.fillBall(me, f, ball, selfPos, gap)
	c.fillGlobal(view, me, f, ball)

	carrierID := -1
	if car, ok := view.Carrier(); ok {
		carrierID = car.ID()
	}
	nTeam := len(c.mates)
	if nTeam > MaxTeammates {
		nTeam = MaxTeammates
	}
	nOpp := len(c.opps)
	if nOpp > MaxOpponents {
		nOpp = MaxOpponents
	}
	c.nTeam, c.nOpp = nTeam, nOpp
	for i := 0; i < nTeam; i++ {
		c.fillEntity(f, c.mates[i], me, ball, carrierID, c.teamBuf[i*EntDim:(i+1)*EntDim])
	}
	for i := 0; i < nOpp; i++ {
		c.fillEntity(f, c.opps[i], me, ball, carrierID, c.oppBuf[i*EntDim:(i+1)*EntDim])
	}
}

func (c *Controller) fillSelf(view sim.View, me sim.SelfView, f egoFrame, gap float64) {
	s := c.selfBuf
	vx, vy := f.localVel(me.Velocity())
	s[0], s[1] = vx, vy
	s[2] = float32(geom.Norm(me.Velocity()) * f.speedScale)
	fc, fs := f.localDir(me.Facing())
	s[3], s[4] = fc, fs
	hc, hs := f.localDir(me.Heading())
	s[5], s[6] = hc, hs
	s[7] = float32(me.Possession())
	s[8] = float32(me.ShootCharge() / shootChargeNorm)
	s[9] = float32(me.TrapCharge())
	cx, cy := f.localPos(view.Field().CenterSpot())
	s[10], s[11] = cx, cy
	s[12] = float32(geom.Dist(me.Position(), view.AttackingGoalCenter(me)) * f.distScale)
	s[13] = float32(geom.Dist(me.Position(), view.DefendingGoalCenter(me)) * f.distScale)
	s[14] = boolf(me.Role() == sim.RoleKeeper)
	s[15] = float32(gap * f.distScale)
	s[16] = float32(me.TrapAura())  // effective trap strength (the glow size a human sees)
	s[17] = float32(me.TouchCoef()) // team buff(+)/debuff(-) coefficient (the green/red bar)
	// NOTE: directional-movement alignment (facing.heading) + the other observation additions are
	// batched into the one-time net-format retrain (TIKITAKA_AI_PROMPT.md A1/A7 step 7); until then
	// directional efficiency is shaped by a forward-alignment REWARD term (reward.go), keeping the
	// net format stable at 18 so the config-only curriculum recipe can be validated cheaply.
}

func (c *Controller) fillBall(me sim.SelfView, f egoFrame, ball sim.BallView, selfPos geom.Vec, gap float64) {
	b := c.ballBuf
	bx, by := f.localPos(ball.Position())
	b[0], b[1] = bx, by
	bvx, bvy := f.localVel(ball.Velocity())
	b[2], b[3] = bvx, bvy
	b[4] = float32(geom.Norm(ball.Velocity()) * f.speedScale)
	sa := geom.SignedAngle(me.Facing(), ball.Position().Sub(selfPos))
	b[5] = float32(math.Cos(sa))
	b[6] = float32(math.Sin(sa))
	b[7] = boolf(gap <= me.Tuning().PullRange)
}

func (c *Controller) fillGlobal(view sim.View, me sim.SelfView, f egoFrame, ball sim.BallView) {
	g := c.globalBuf
	g[0] = float32(clamp01(view.Clock() / clockRefSeconds))
	g[1] = boolf(amClosestToBall(view, me, ball))
	g[2] = boolf(view.KickoffArmed())
	g[3] = boolf(view.KickoffSide() == me.Side())
	iC, teamC, oppC := carrierFlags(view, me)
	g[4], g[5], g[6] = iC, teamC, oppC
	t0, t1, t2 := ballThird(view, me, ball)
	g[7], g[8], g[9] = t0, t1, t2
	g[10] = float32(float64(len(c.mates)+1) / 11.0) // squad size incl. self
	g[11] = float32(float64(len(c.opps)) / 11.0)
}

func (c *Controller) fillEntity(f egoFrame, o sim.ObservedView, me sim.SelfView, ball sim.BallView, carrierID int, dst []float32) {
	p := o.Position()
	px, py := f.localPos(p)
	dst[0], dst[1] = px, py
	fc, fs := f.localDir(o.Facing())
	dst[2], dst[3] = fc, fs
	vx, vy := f.localVel(c.mem.estVel(o.ID(), c.dtCache))
	dst[4], dst[5] = vx, vy
	dst[6] = float32(geom.Dist(p, me.Position()) * f.distScale)
	dst[7] = float32(geom.Dist(p, ball.Position()) * f.distScale)
	dst[8] = float32(o.ShootCharge() / shootChargeNorm)
	dst[9] = float32(o.TrapCharge())
	dst[10] = boolf(o.ID() == carrierID)
	dst[11] = boolf(o.Role() == sim.RoleKeeper)
	dst[12] = float32(o.TrapAura())  // effective trap strength (glow size)
	dst[13] = float32(o.Possession()) // per-player possession (white bar)
	dst[14] = float32(o.TouchCoef())  // team buff(+)/debuff(-) (green/red bar)
}

func clamp01(x float64) float64 {
	if x < 0 {
		return 0
	}
	if x > 1 {
		return 1
	}
	return x
}

func amClosestToBall(view sim.View, me sim.SelfView, ball sim.BallView) bool {
	best := geom.Dist(me.Position(), ball.Position())
	closest := true
	for _, q := range view.Squad(me) {
		if q.Same(me) {
			continue
		}
		if geom.Dist(q.Position(), ball.Position()) < best {
			closest = false
			break
		}
	}
	return closest
}

func carrierFlags(view sim.View, me sim.SelfView) (iC, teamC, oppC float32) {
	car, ok := view.Carrier()
	if !ok {
		return 0, 0, 0
	}
	switch {
	case car.Same(me):
		return 1, 0, 0
	case car.SameTeam(me):
		return 0, 1, 0
	default:
		return 0, 0, 1
	}
}

// ballThird returns a one-hot of which third (own / middle / attacking) the ball sits in,
// measured along the player's own attack direction.
func ballThird(view sim.View, me sim.SelfView, ball sim.BallView) (float32, float32, float32) {
	def := view.DefendingGoalCenter(me)
	att := view.AttackingGoalCenter(me)
	axis := att.Sub(def)
	total := geom.Norm(axis)
	if total < 1e-9 {
		return 0, 1, 0
	}
	prog := clamp01(geom.Dot(ball.Position().Sub(def), axis) / (total * total))
	idx := int(prog * 3)
	if idx > 2 {
		idx = 2
	}
	switch idx {
	case 0:
		return 1, 0, 0
	case 1:
		return 0, 1, 0
	default:
		return 0, 0, 1
	}
}

// FeaturizeFlat fills dst (len FlatDim) with the fixed-width flat featurization used by datagen
// and the parity exporter: self, ball, global, zero-padded teammate rows, zero-padded opponent
// rows, then nTeam and nOpp. It runs build() first (updating velocity memory), so callers should
// invoke it once per tick in tick order.
func (c *Controller) FeaturizeFlat(view sim.View, me sim.SelfView, dst []float32) {
	c.build(view, me)
	for i := range dst {
		dst[i] = 0
	}
	off := 0
	copy(dst[off:off+SelfDim], c.selfBuf)
	off += SelfDim
	copy(dst[off:off+BallDim], c.ballBuf)
	off += BallDim
	copy(dst[off:off+GlobalDim], c.globalBuf)
	off += GlobalDim
	copy(dst[off:off+c.nTeam*EntDim], c.teamBuf[:c.nTeam*EntDim])
	off += MaxTeammates * EntDim
	copy(dst[off:off+c.nOpp*EntDim], c.oppBuf[:c.nOpp*EntDim])
	off += MaxOpponents * EntDim
	dst[off] = float32(c.nTeam)
	dst[off+1] = float32(c.nOpp)
}
