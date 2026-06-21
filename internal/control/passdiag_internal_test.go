package control

// Failed-pass classifier and many-seed sweep runner -- the diagnostic tooling behind the
// tiki-taka passing work. It lives in the INTERNAL test package (package control, not
// control_test) on purpose: it reuses the unexported predict/ball helpers (laneSafe,
// ballTravelTime, predictBall, ...), reads the AI's write-only pass-intent snapshot
// (diagPass* on AI), and mutates a per-AI aiTuning field to sweep one lever at a time --
// none of which an external test could do without enlarging the package's exported surface.
//
// It reads GROUND TRUTH from *sim.Match (ball, touches, carriers, player tuning). That is
// legitimate for a test; the LIVE AI decision path never reads any of this. The classifier
// attributes every FAILED pass to exactly one cause bucket via a total decision tree, and
// the sweep reports completion/volume/clears/shots with the cause histogram over many seeds.

import (
	"math"
	"os"
	"runtime"
	"sort"
	"sync"
	"testing"

	"phootball/internal/config"
	"phootball/internal/geom"
	"phootball/internal/sim"
)

const diagDt = 1.0 / 60.0

// diagTicks is one 120-second game at 60Hz -- the same horizon TestPassCompletionLargeMap uses.
const diagTicks = 60 * 120

// largeGeom is the LargeGeometry config mutation the pass-completion metric is defined on.
func largeGeom(c *config.Config) { c.Geometry = config.LargeGeometry() }

// sweepMatch builds a 6-a-side-style match like the external aiMatch, but applies mutateTune to
// EVERY AI's tuning before the first Intent so a lever can be swept symmetrically (both teams
// identical -- an asymmetric override would make the completion metric meaningless).
func sweepMatch(teamSize int, seed int64, skill Skill, mutateCfg func(*config.Config), mutateTune func(*aiTuning)) (*sim.Match, map[int]*AI) {
	cfg := config.Default()
	cfg.Seed = seed
	if mutateCfg != nil {
		mutateCfg(&cfg)
	}
	field := sim.NewFieldFromGeometry(cfg.Geometry)
	m := sim.BuildMatchFromConfig(field, teamSize, cfg)
	ais := make(map[int]*AI, len(m.Players))
	for _, pl := range m.Players {
		ai := NewAISkill(pl.PlayerID, skill)
		if mutateTune != nil {
			mutateTune(&ai.tune)
		}
		ais[pl.PlayerID] = ai
	}
	return m, ais
}

// passCause buckets WHY a pass failed. The decision tree in classifyFail is total, so the
// buckets always sum to the number of failed passes (asserted in the sweep).
type passCause int

const (
	causeInterceptFlight passCause = iota // an opponent stepped into the lane before the target
	causeLostAtDest                       // an opponent won the ball at/around the target
	causeMiscontrol                       // a team-mate reached it but the touch bounced away and was lost
	causeOverHit                          // the ball sailed past the target
	causeUnderHit                         // the ball fell short of the target (incl. stopped dead)
	causeBadTarget                        // played where no team-mate was / the receiver never converged
	causeOther                            // residual -- watched and kept near zero
	numPassCauses
)

var causeNames = [numPassCauses]string{
	"intercept-in-flight", "lost-at-destination", "receiver-miscontrol",
	"over-hit", "under-hit/short", "bad-target", "other",
}

// diagResult is one game's measured outcome.
type diagResult struct {
	seed                            int64
	passes, passDone                int
	shots, onTarget, scored, clears int
	pushes                          int // ticks the AI fired a middle-click push (the instant radial jab)
	// Hold-time (the hoarding metric): how long a SINGLE player keeps firm possession before the
	// ball moves on. longHolds counts holds over holdLongTicks (5s) -- the reported "holds 10s" bug.
	maxHoldTicks       int
	longHolds          int
	holdSum, holdCount int // for the mean hold
	// Turnovers: a firm possession lost to the opponent. ownHalfTurnovers is the dangerous subset --
	// the ball given away in the LOSING team's own half (the user wants this much lower).
	turnovers, ownHalfTurnovers int
	// Possession-outcome attribution: when a between-team possession change happens, what was the
	// LOSING side's last deliberate action? The north-star is that the MAJORITY of changes come from
	// a SHOT (the attack was finished by shooting), NOT from a give-away (a bad pass or a lost
	// dribble). endShot/endGoal are the GOOD ends; endBadPass/endLostDribble are the MISTAKE ends;
	// endClear is a neutral safety boot. These sum to turnovers (+endGoal, which also flips possession
	// via the kickoff). See the attribution in classifyMatch.
	endShot, endClear, endBadPass, endLostDribble, endGoal int
	// Directional speed efficiency: when a player is actively moving, how aligned is its FACING with
	// its travel direction (the sim's directionalSpeedMul). 1.0 = running flat-out toward its aim;
	// low = crawling off-axis (the "directional AI goes really slow" complaint). Accumulated over
	// every moving player-tick. Always ~1.0 under the Standard model (facing is speed-neutral there).
	speedMulSum   float64
	speedMulCount int
	causes        [numPassCauses]int
	// miscontrol annotations (sub-counts of causeMiscontrol), so a first-touch failure is
	// attributable to arriving off the capture cone (a FACING failure) vs arriving too fast
	// head-on (a true over-pace failure). This is the key disambiguation: softening the launch
	// is the WRONG fix for an off-cone failure.
	miscontrolOffCone, miscontrolHot, miscontrolOther int
}

func (r diagResult) passPct() float64 {
	if r.passes == 0 {
		return 0
	}
	return 100 * float64(r.passDone) / float64(r.passes)
}

// pendingPass accumulates the evidence for one in-flight pass between its release and its
// resolution, so a failure can be attributed to a cause.
type pendingPass struct {
	kicker      int
	side        sim.Side
	launchPos   geom.Vec
	launchSpeed float64
	laneLen     float64
	regionR     float64 // radius around the target that counts as "at the destination"

	target        geom.Vec
	targetKnown   bool
	reconstructed bool // target inferred from the launch ray (snapshot was stale)
	recvID        int

	lastSeenToucher     int
	mateContacted       bool
	mateContactPos      geom.Vec
	mateImpactSpeed     float64
	mateImpactAngle     float64
	mateRecv            *sim.Player
	oppContacted        bool
	oppContactPos       geom.Vec
	mateNearTargetEver  bool
	recvNearBallEver    bool
	minBallDistToTarget float64

	// Intended-receiver trajectory at the ball's closest approach to it -- the overshoot /
	// velocity-match diagnostic. A good reception has the receiver ON the ball's line (low
	// recvOffLine) moving WITH the ball (recvAlign ~1, low recvRelSpeed). Overshoot shows as the
	// receiver ahead/across with poor alignment; can't-reach shows as a large recvMinDist.
	recvMinDist  float64
	recvAlign    float64 // dot(unit(recvVel), unit(ballVel)) at closest -- moving with the ball?
	recvRelSpeed float64 // |ballVel - recvVel| at closest -- the relative-impact proxy
	recvOffLine  float64 // receiver's perpendicular distance to the ball's line at closest
	recvSpeed    float64 // |recvVel| at closest

	// Target-drift diagnostic: did the intended receiver STAY where the pass was aimed, or wander
	// off it? recvTargetGap0 is the receiver's distance to the aim target at launch; recvTargetGapArr
	// is its distance to the target when the ball was CLOSEST to that target (i.e. when the pass
	// "arrived"). A small gap0 with a large gapArr means the receiver abandoned the spot the pass was
	// aimed at (the over-hit-to-an-empty-spot failure); a large gap0 means the carrier aimed where the
	// receiver never was.
	recvTargetGap0   float64
	recvTargetGapArr float64
	recvClosing      float64 // dot(unit(recvVel), unit(ball-receiver)) at closest: >0 collecting, <=0 drifting away
}

// newPending snapshots the state at a pass's release, using the kicker AI's write-only pass
// intent (frozen at the decision) when it is fresh, else reconstructing the target from the
// launch ray.
func newPending(m *sim.Match, ai *AI, kicker int, players map[int]*sim.Player) pendingPass {
	pp := pendingPass{
		kicker:              kicker,
		side:                players[kicker].Team.Side,
		launchPos:           m.Ball.Position,
		launchSpeed:         geom.Norm(m.Ball.Velocity),
		lastSeenToucher:     kicker,
		minBallDistToTarget: math.Inf(1),
		recvMinDist:         math.Inf(1),
		recvID:              -1,
	}
	pp.regionR = ai.tune.passReceiverSpace + players[kicker].Radius() + m.Ball.Radius()
	if ai.diagPassSet && m.Tick-ai.diagPassTick <= 12 {
		pp.target, pp.targetKnown, pp.recvID = ai.diagPassTarget, true, ai.diagPassRecvID
	} else if v := m.Ball.Velocity; geom.Norm(v) > 1 {
		// Stale snapshot (e.g. a deflection that read as a pass): aim a nominal lane along the
		// launch ray so the geometry still has something to work with.
		pp.target, pp.targetKnown, pp.reconstructed = m.Ball.Position.Add(geom.Unit(v).Scale(200)), true, true
	}
	pp.laneLen = geom.Dist(pp.launchPos, pp.target)
	pp.recvTargetGap0, pp.recvTargetGapArr = math.Inf(1), math.Inf(1)
	if pp.recvID >= 0 && pp.targetKnown {
		if r := players[pp.recvID]; r != nil {
			pp.recvTargetGap0 = geom.Dist(r.Position, pp.target)
		}
	}
	return pp
}

// update folds one tick of ball/touch state into the pending pass.
func (pp *pendingPass) update(m *sim.Match, players map[int]*sim.Player, prevBallVel geom.Vec) {
	ball := m.Ball.Position
	if pp.targetKnown {
		if d := geom.Dist(ball, pp.target); d < pp.minBallDistToTarget {
			pp.minBallDistToTarget = d
			// At the moment the ball is closest to where it was aimed, how far is the intended
			// receiver from that spot? (Did it stay to collect, or wander off?)
			if pp.recvID >= 0 {
				if r := players[pp.recvID]; r != nil {
					pp.recvTargetGapArr = geom.Dist(r.Position, pp.target)
				}
			}
		}
		for _, pl := range players {
			if pl.Team.Side == pp.side && pl.PlayerID != pp.kicker && geom.Dist(pl.Position, pp.target) < pp.regionR {
				pp.mateNearTargetEver = true
				break
			}
		}
	}
	if pp.recvID >= 0 {
		if r := players[pp.recvID]; r != nil {
			if geom.Dist(r.Position, ball)-r.Radius()-m.Ball.Radius() < pp.regionR {
				pp.recvNearBallEver = true
			}
			// Record the receiver's motion vs the ball at the ball's CLOSEST approach to it.
			if d := geom.Dist(r.Position, ball); d < pp.recvMinDist {
				pp.recvMinDist = d
				rv, bv := r.Velocity, m.Ball.Velocity
				pp.recvSpeed = geom.Norm(rv)
				pp.recvRelSpeed = geom.Norm(bv.Sub(rv))
				if geom.Norm(rv) > 1 && geom.Norm(bv) > 1 {
					pp.recvAlign = geom.Dot(geom.Unit(rv), geom.Unit(bv))
				}
				// Is the receiver moving TOWARD the ball (collecting it) or away (drifting/repositioning)?
				// dot(unit(recvVel), unit(ball-receiver)): >0 = going to get it, <=0 = leaving it.
				if toBall := geom.Unit(ball.Sub(r.Position)); toBall != (geom.Vec{}) && geom.Norm(rv) > 1 {
					pp.recvClosing = geom.Dot(geom.Unit(rv), toBall)
				}
				if bd := geom.Unit(bv); bd != (geom.Vec{}) {
					rel := r.Position.Sub(ball)
					pp.recvOffLine = geom.Norm(rel.Sub(bd.Scale(geom.Dot(rel, bd))))
				}
			}
		}
	}
	// A new distinct toucher: record the first team-mate touch (with impact pace/angle) and the
	// first opponent touch (with position), which drive the cause split.
	if lt := m.LastTouch; lt != nil && lt.Player != pp.lastSeenToucher {
		pp.lastSeenToucher = lt.Player
		if lt.Player != pp.kicker {
			if lt.Side == pp.side {
				if !pp.mateContacted {
					pp.mateContacted, pp.mateContactPos, pp.mateImpactSpeed = true, ball, geom.Norm(prevBallVel)
					if r := players[lt.Player]; r != nil {
						pp.mateRecv = r
						pp.mateImpactAngle = geom.AngleBetween(r.Facing, ball.Sub(r.Position))
					}
				}
			} else if !pp.oppContacted {
				pp.oppContacted, pp.oppContactPos = true, ball
			}
		}
	}
}

// classifyFail attributes a failed pass to exactly one cause. The tree is total: every path
// returns, with causeOther the explicit residual (kept near zero and watched).
func (pp *pendingPass) classifyFail(ballPos geom.Vec) passCause {
	if !pp.targetKnown {
		switch {
		case pp.mateContacted:
			return causeMiscontrol
		case pp.oppContacted:
			return causeInterceptFlight
		default:
			return causeOther
		}
	}
	// 1. Bad target: played where no team-mate was and the receiver never converged.
	if !pp.mateContacted && !pp.recvNearBallEver && !pp.mateNearTargetEver {
		return causeBadTarget
	}
	// 2. A team-mate reached it but it bounced away and was lost -> first-touch failure.
	if pp.mateContacted {
		return causeMiscontrol
	}
	// 3. An opponent decided the ball: split by WHERE along the lane it first touched.
	if pp.oppContacted {
		along, perp := projectOntoLane(pp.oppContactPos, pp.launchPos, pp.target)
		switch {
		case along > pp.laneLen+pp.regionR:
			return causeOverHit // past the target, to an opponent behind it
		case along < pp.laneLen-pp.regionR:
			if perp <= pp.regionR {
				return causeInterceptFlight // opponent stepped into the lane short of the target
			}
			return causeUnderHit // ball fell short off the lane and an opponent mopped up
		default:
			return causeLostAtDest // opponent won the ball at the target
		}
	}
	// 4. Nobody else touched it: a pure delivery miss judged by closest approach.
	if pp.minBallDistToTarget > pp.regionR {
		if geom.Dist(ballPos, pp.launchPos) < pp.laneLen {
			return causeUnderHit
		}
		return causeOverHit
	}
	return causeOther
}

// trace summarises a failed pass for the numeric scrub.
func (pp *pendingPass) trace(c passCause) passTrace {
	tr := passTrace{
		cause: c, launchSpeed: pp.launchSpeed, laneLen: pp.laneLen, minDist: pp.minBallDistToTarget,
		mateContacted: pp.mateContacted, mateImpactSpeed: pp.mateImpactSpeed, mateImpactAng: pp.mateImpactAngle,
		oppContacted: pp.oppContacted, recvID: pp.recvID, reconstructed: pp.reconstructed,
		recvMinDist: pp.recvMinDist, recvAlign: pp.recvAlign, recvRelSpeed: pp.recvRelSpeed,
		recvOffLine: pp.recvOffLine, recvSpeed: pp.recvSpeed,
		recvTargetGap0: pp.recvTargetGap0, recvTargetGapArr: pp.recvTargetGapArr, recvClosing: pp.recvClosing,
	}
	if pp.mateRecv != nil {
		tr.capSpeed = pp.mateRecv.Tuning.CaptureSpeedAt(pp.mateRecv.Tuning.CaptureConeRadians, pp.mateImpactAngle) + pp.mateRecv.Tuning.TrapCaptureBonus*pp.mateRecv.TrapAura()
	}
	if pp.oppContacted {
		tr.oppAlong, tr.oppPerp = projectOntoLane(pp.oppContactPos, pp.launchPos, pp.target)
	}
	return tr
}

// projectOntoLane returns the along-lane and perpendicular distances of pt from the launch,
// in the launch->target frame.
func projectOntoLane(pt, launch, target geom.Vec) (along, perp float64) {
	dir := geom.Unit(target.Sub(launch))
	if dir == (geom.Vec{}) {
		return 0, geom.Dist(pt, launch)
	}
	rel := pt.Sub(launch)
	along = geom.Dot(rel, dir)
	perp = geom.Norm(rel.Sub(dir.Scale(along)))
	return along, perp
}

// diagShotOnTarget mirrors the external shotOnTarget helper (the ball's straight path crosses
// the attacking goal mouth) so the diagnostic reports the same shots-on-target metric.
func diagShotOnTarget(m *sim.Match, player int, players map[int]*sim.Player) bool {
	p := players[player]
	if p == nil {
		return false
	}
	goal := m.AttackingGoal(p.Team)
	vel := m.Ball.Velocity
	if vel.X == 0 {
		return false
	}
	t := (goal.Center.X - m.Ball.Position.X) / vel.X
	if t <= 0 {
		return false
	}
	y := m.Ball.Position.Y + vel.Y*t
	lo, hi := goal.Mouth.A.Y, goal.Mouth.B.Y
	if lo > hi {
		lo, hi = hi, lo
	}
	return y >= lo && y <= hi
}

// passTrace is a per-failed-pass summary for the numeric scrub (corroborating the classifier
// with the actual physics numbers, in lieu of pixel-watching): the launch pace, the lane
// length, how close the ball got to the target, and the reception/contest evidence.
type passTrace struct {
	cause                                                        passCause
	launchSpeed, laneLen, minDist                                float64
	mateContacted                                                bool
	mateImpactSpeed, mateImpactAng                               float64
	capSpeed                                                     float64
	oppContacted                                                 bool
	oppAlong, oppPerp                                            float64
	recvID                                                       int
	reconstructed                                                bool
	recvMinDist, recvAlign, recvRelSpeed, recvOffLine, recvSpeed float64
	recvTargetGap0, recvTargetGapArr, recvClosing                float64
}

// classifyMatch runs one game and returns its measured outcome with the failed-pass causes.
// The pass / passDone counting mirrors the external measureKicks exactly (kick detected by a
// ball-speed jump; a pass resolved by the next distinct firm possessor or superseding kick or
// goal), so the diagnostic's completion %% equals the gate's; the cause attribution is layered
// on the failures.
func classifyMatch(m *sim.Match, ais map[int]*AI, ticks int, seed int64, tracer *[]passTrace) diagResult {
	res := diagResult{seed: seed}
	players := make(map[int]*sim.Player, len(m.Players))
	for _, pl := range m.Players {
		players[pl.PlayerID] = pl
	}

	recent := map[int][]string{}
	classify := func(player int) string {
		for _, a := range recent[player] {
			if a == "shoot" || a == "pass" || a == "clear" {
				return a
			}
		}
		return "dribble"
	}

	var (
		pendingActive bool
		pendingKind   string
		pendingSide   sim.Side
		pendingPlayer int
		cur           pendingPass
		prevSpeed     float64
		prevBallVel   geom.Vec
		prevGoals     = m.Teams[0].Score + m.Teams[1].Score
		holdCarrier   = -1 // the player currently holding firm possession
		holdLen       = 0  // consecutive ticks holdCarrier has held it
		lastFirmSide  = sim.SideNone
	)
	cx := m.Field.CenterSpot.X
	inOwnHalf := func(side sim.Side, x float64) bool { // is x in side's defensive half?
		return (side == sim.SideLeft && x < cx) || (side == sim.SideRight && x > cx)
	}

	// flushHold records a completed single-player hold into the result (longest, >5s count, mean).
	flushHold := func() {
		if holdCarrier < 0 || holdLen <= 0 {
			return
		}
		res.holdSum += holdLen
		res.holdCount++
		if holdLen > res.maxHoldTicks {
			res.maxHoldTicks = holdLen
		}
		if holdLen > holdLongTicks {
			res.longHolds++
		}
	}

	resolvePass := func(reached bool) {
		if pendingActive && pendingKind == "pass" {
			res.passes++
			if reached {
				res.passDone++
			} else {
				c := cur.classifyFail(m.Ball.Position)
				res.causes[c]++
				if c == causeMiscontrol {
					res.annotateMiscontrol(&cur)
				}
				if tracer != nil {
					*tracer = append(*tracer, cur.trace(c))
				}
			}
		}
		pendingActive = false
	}

	for i := 0; i < ticks; i++ {
		in := make(map[int]sim.Intent, len(ais))
		for id, ai := range ais {
			in[id] = ai.Intent(m.View())
			if in[id].Push {
				res.pushes++ // count middle-click pushes the AI fires
			}
			// Directional speed efficiency: for an actively-moving player, how much of its top speed
			// the directional curve grants given its facing (1.0 = facing its run, low = crawling
			// off-axis). Skip the carrier (it faces the ball/target to control, which is correct).
			if pl := players[id]; pl != nil && geom.Norm(in[id].Move) > 0.01 && in[id].Throttle > 0.01 {
				if c := m.BallCarrier(); c == nil || c.PlayerID != id {
					res.speedMulSum += m.View().DirectionalSpeedMul(in[id].Move, pl.Facing)
					res.speedMulCount++
				}
			}
			r := append([]string{ai.LastAction()}, recent[id]...)
			if len(r) > 4 {
				r = r[:4]
			}
			recent[id] = r
		}
		m.Step(in, diagDt)

		// Hold-time: count consecutive ticks the SAME player keeps firm possession; on a change,
		// flush the completed hold. (A 10s dribble shows up as one ~600-tick hold.)
		if c := m.BallCarrier(); c != nil && c.PlayerID == holdCarrier {
			holdLen++
		} else {
			flushHold()
			if c != nil {
				holdCarrier, holdLen = c.PlayerID, 1
			} else {
				holdCarrier, holdLen = -1, 0
			}
		}
		// Turnover: a firm carrier of the OTHER side appears -- the previous side lost the ball.
		// Count it, and the dangerous subset where it was given away in that side's own half.
		if c := m.BallCarrier(); c != nil {
			if lastFirmSide != sim.SideNone && c.Team.Side != lastFirmSide {
				res.turnovers++
				if inOwnHalf(lastFirmSide, m.Ball.Position.X) {
					res.ownHalfTurnovers++
				}
				// Attribute the loss to the LOSING side's last deliberate action (the pending kick is
				// from a prior tick, so it reflects what the losing side last did). A shot the opponent
				// gathered = a GOOD end (the attack was finished by shooting); a pass that reached the
				// opponent = a bad pass; a clear = a safety boot; anything else (no recent kick by the
				// losing side) = the ball lost while in possession (a dribble/tackle loss).
				switch {
				case pendingActive && pendingSide == lastFirmSide && pendingKind == "shoot":
					res.endShot++
				case pendingActive && pendingSide == lastFirmSide && pendingKind == "clear":
					res.endClear++
				case pendingActive && pendingSide == lastFirmSide && pendingKind == "pass":
					res.endBadPass++
				default:
					res.endLostDribble++
				}
			}
			lastFirmSide = c.Team.Side
		}

		if g := m.Teams[0].Score + m.Teams[1].Score; g != prevGoals {
			if pendingActive && pendingKind == "shoot" {
				res.scored++
			}
			res.endGoal++ // a goal is the ultimate shot-end: the attack finished by scoring (and flips possession via the kickoff)
			resolvePass(true)
			prevGoals = g
			prevSpeed = geom.Norm(m.Ball.Velocity)
			prevBallVel = m.Ball.Velocity
			continue
		}

		if pendingActive && pendingKind == "pass" {
			cur.update(m, players, prevBallVel)
		}

		sp := geom.Norm(m.Ball.Velocity)
		if lt := m.LastTouch; lt != nil && sp-prevSpeed > 60 { // a kick impulse
			if pendingActive && lt.Player != pendingPlayer {
				resolvePass(lt.Side == pendingSide)
			}
			kind := classify(lt.Player)
			pendingActive, pendingKind, pendingSide, pendingPlayer = true, kind, lt.Side, lt.Player
			switch kind {
			case "shoot":
				res.shots++
				if diagShotOnTarget(m, lt.Player, players) {
					res.onTarget++
				}
			case "clear":
				res.clears++
			case "pass":
				cur = newPending(m, ais[lt.Player], lt.Player, players)
			}
		}
		prevSpeed = sp
		prevBallVel = m.Ball.Velocity

		if pendingActive && pendingKind == "pass" {
			if c := m.BallCarrier(); c != nil && c.PlayerID != pendingPlayer {
				resolvePass(c.Team.Side == pendingSide)
			}
		}
	}
	flushHold() // the hold in progress at the final tick
	return res
}

// holdLongTicks is the threshold (5s at 60Hz) above which a single-player hold counts as the
// reported hoarding bug ("holds the ball 10 seconds at a time").
const holdLongTicks = 5 * 60

// annotateMiscontrol sub-classifies a first-touch failure as off-cone (the ball arrived
// outside the receiver's capture cone -- a FACING failure) vs hot (it arrived faster than the
// receiver's capture pace head-on -- a true over-pace failure) vs other. The capture pace is
// approximated from exported tuning (CaptureSpeedAt(angle) + trap bonus); the exact
// physics value also folds in the unexported cone/touch-quality terms, so this is a directional
// annotation, not a precise threshold -- the off-cone vs head-on split is the part that matters.
func (res *diagResult) annotateMiscontrol(pp *pendingPass) {
	r := pp.mateRecv
	if r == nil {
		res.miscontrolOther++
		return
	}
	capSpeed := r.Tuning.CaptureSpeedAt(r.Tuning.CaptureConeRadians, pp.mateImpactAngle) + r.Tuning.TrapCaptureBonus*r.TrapAura()
	switch {
	case pp.mateImpactAngle > r.Tuning.CaptureConeRadians:
		res.miscontrolOffCone++
	case pp.mateImpactSpeed > capSpeed: // arrived faster than the receiver could capture head-on
		res.miscontrolHot++
	default:
		res.miscontrolOther++
	}
}

// diagSweep runs the seeds in parallel (each match graph is private and seeded, so the run is
// race-free and deterministic) and returns one diagResult per seed.
func diagSweep(seeds []int64, skill Skill, mutateTune func(*aiTuning)) []diagResult {
	results := make([]diagResult, len(seeds))
	workers := runtime.GOMAXPROCS(0)
	if workers > 12 {
		workers = 12
	}
	if workers < 1 {
		workers = 1
	}
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	for i, seed := range seeds {
		wg.Add(1)
		go func(i int, seed int64) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			m, ais := sweepMatch(6, seed, skill, largeGeom, mutateTune)
			results[i] = classifyMatch(m, ais, diagTicks, seed, nil)
		}(i, seed)
	}
	wg.Wait()
	return results
}

// diagStats are the aggregate numbers reported for a sweep.
type diagStats struct {
	n                                           int
	pctMean, pctStd, pctMin, pctMax             float64
	passesMean, clearsMean, shotsMean           float64
	onTargetMean, scoredMean, pushesMean        float64
	maxHoldSecs, meanHoldSecs, longHoldsPerGame float64 // hoarding metric
	turnoversMean, ownHalfTurnoversMean         float64 // possession lost to the opponent (and the own-half subset)
	// Possession-outcome attribution (the north-star). shotEndedFrac = (endShot+endGoal)/ends should
	// be the MAJORITY; mistakeFrac = (endBadPass+endLostDribble)/ends should fall.
	endShot, endClear, endBadPass, endLostDribble, endGoal int
	shotEndedFrac, mistakeFrac                             float64
	speedEff                                               float64 // mean directional speed multiplier achieved by moving off-ball players (1.0 = run flat-out toward aim; low = crawl off-axis)
	totPasses, totDone, totFails                           int
	causes                                                 [numPassCauses]int
	miscontrolOffCone, miscontrolHot, miscontrolOther      int
}

func aggregate(results []diagResult) diagStats {
	var s diagStats
	s.n = len(results)
	if s.n == 0 {
		return s
	}
	pcts := make([]float64, 0, s.n)
	var sumPasses, sumClears, sumShots, sumOnTarget, sumScored, sumPushes, sumLongHolds, holdSum, holdCount, maxHold, sumTO, sumOwnTO int
	var speedMulSum float64
	var speedMulCount int
	for _, r := range results {
		pcts = append(pcts, r.passPct())
		s.totPasses += r.passes
		s.totDone += r.passDone
		sumPasses += r.passes
		sumClears += r.clears
		sumShots += r.shots
		sumOnTarget += r.onTarget
		sumScored += r.scored
		sumPushes += r.pushes
		sumLongHolds += r.longHolds
		holdSum += r.holdSum
		holdCount += r.holdCount
		sumTO += r.turnovers
		sumOwnTO += r.ownHalfTurnovers
		s.endShot += r.endShot
		s.endClear += r.endClear
		s.endBadPass += r.endBadPass
		s.endLostDribble += r.endLostDribble
		s.endGoal += r.endGoal
		speedMulSum += r.speedMulSum
		speedMulCount += r.speedMulCount
		if r.maxHoldTicks > maxHold {
			maxHold = r.maxHoldTicks
		}
		for c := passCause(0); c < numPassCauses; c++ {
			s.causes[c] += r.causes[c]
		}
		s.miscontrolOffCone += r.miscontrolOffCone
		s.miscontrolHot += r.miscontrolHot
		s.miscontrolOther += r.miscontrolOther
	}
	s.maxHoldSecs = float64(maxHold) * diagDt
	s.longHoldsPerGame = float64(sumLongHolds) / float64(s.n)
	if holdCount > 0 {
		s.meanHoldSecs = float64(holdSum) / float64(holdCount) * diagDt
	}
	s.turnoversMean = float64(sumTO) / float64(s.n)
	s.ownHalfTurnoversMean = float64(sumOwnTO) / float64(s.n)
	if speedMulCount > 0 {
		s.speedEff = speedMulSum / float64(speedMulCount)
	}
	if ends := s.endShot + s.endClear + s.endBadPass + s.endLostDribble + s.endGoal; ends > 0 {
		s.shotEndedFrac = float64(s.endShot+s.endGoal) / float64(ends)
		s.mistakeFrac = float64(s.endBadPass+s.endLostDribble) / float64(ends)
	}
	s.totFails = s.totPasses - s.totDone
	s.pctMean, s.pctStd = meanStd(pcts)
	sort.Float64s(pcts)
	s.pctMin, s.pctMax = pcts[0], pcts[len(pcts)-1]
	fn := float64(s.n)
	s.passesMean = float64(sumPasses) / fn
	s.clearsMean = float64(sumClears) / fn
	s.shotsMean = float64(sumShots) / fn
	s.onTargetMean = float64(sumOnTarget) / fn
	s.scoredMean = float64(sumScored) / fn
	s.pushesMean = float64(sumPushes) / fn
	return s
}

func meanStd(xs []float64) (mean, std float64) {
	if len(xs) == 0 {
		return 0, 0
	}
	for _, x := range xs {
		mean += x
	}
	mean /= float64(len(xs))
	for _, x := range xs {
		std += (x - mean) * (x - mean)
	}
	return mean, math.Sqrt(std / float64(len(xs)))
}

// report logs the aggregate stats and the cause histogram, and verifies the buckets sum to the
// failed-pass total (the classifier must be a TOTAL function -- no silently-dropped failures).
func report(t *testing.T, label string, s diagStats) {
	t.Helper()
	t.Logf("%s | seeds=%d", label, s.n)
	t.Logf("  completion: mean %.1f%% sd %.1f min %.0f max %.0f  (per-game)",
		s.pctMean, s.pctStd, s.pctMin, s.pctMax)
	t.Logf("  volume: %.1f passes/game (%d total) | clears %.1f/game | shots %.1f onTarget %.1f scored %.1f /game | push-ticks %.1f/game",
		s.passesMean, s.totPasses, s.clearsMean, s.shotsMean, s.onTargetMean, s.scoredMean, s.pushesMean)
	t.Logf("  hold-time: mean %.1fs | max %.1fs | holds>5s %.2f/game (the hoarding bug)",
		s.meanHoldSecs, s.maxHoldSecs, s.longHoldsPerGame)
	t.Logf("  turnovers: %.1f/game (own-half %.1f/game -- the dangerous ones)",
		s.turnoversMean, s.ownHalfTurnoversMean)
	t.Logf("  possession ends: shot=%d goal=%d clear=%d badPass=%d lostDribble=%d | shotEnded %.1f%% (NORTH-STAR: majority) mistake %.1f%%",
		s.endShot, s.endGoal, s.endClear, s.endBadPass, s.endLostDribble, 100*s.shotEndedFrac, 100*s.mistakeFrac)
	t.Logf("  directional speed-eff: %.3f (1.0 = moving players run flat-out toward their aim; low = crawling off-axis)", s.speedEff)
	var sum int
	for c := passCause(0); c < numPassCauses; c++ {
		sum += s.causes[c]
	}
	if sum != s.totFails {
		t.Errorf("cause buckets (%d) do not sum to failed passes (%d): classifier is not total", sum, s.totFails)
	}
	t.Logf("  failed passes: %d", s.totFails)
	for c := passCause(0); c < numPassCauses; c++ {
		pct := 0.0
		if s.totFails > 0 {
			pct = 100 * float64(s.causes[c]) / float64(s.totFails)
		}
		t.Logf("    %-20s %4d (%4.1f%% of fails)", causeNames[c], s.causes[c], pct)
	}
	t.Logf("    miscontrol breakdown: off-cone=%d hot=%d other=%d",
		s.miscontrolOffCone, s.miscontrolHot, s.miscontrolOther)
}

// diagSeeds returns the validation seed band, DISJOINT from the 1-6 gate seeds so the gate is
// never tuned toward (overfitting). A short run uses fewer seeds for a quick signal.
func diagSeeds(short bool) []int64 {
	// 30 disjoint validation seeds by default (the committed baseline); a final sign-off bumps
	// this to 50+. Short mode trims it for a quick signal.
	n := int64(30)
	if short {
		n = 8
	}
	seeds := make([]int64, 0, n)
	for s := int64(101); s < 101+n; s++ {
		seeds = append(seeds, s)
	}
	return seeds
}

// TestPassDiagnosis is the committed baseline sweep + failed-pass cause histogram over the
// disjoint validation seed band. It is the diagnostic Jack can re-run; it asserts the
// classifier is total (buckets sum to failures) but does not gate on the completion number
// (that is TestPassCompletionLargeMap's job).
func TestPassDiagnosis(t *testing.T) {
	seeds := diagSeeds(testing.Short())
	results := diagSweep(seeds, SkillHard, nil)
	report(t, "BASELINE (default tuning)", aggregate(results))
}

// TestPassScrub corroborates the classifier numerically: it traces every failed pass over a
// few seeds and prints, per dominant cause, the launch pace, lane length, closest approach to
// the target, and the reception/contest evidence -- so a bucket like "receiver-miscontrol
// (hot)" can be confirmed to really be a too-fast arrival rather than a classifier artifact.
// SKIPPED by default; run with DIAG_SCRUB=1.
func TestPassScrub(t *testing.T) {
	if os.Getenv("DIAG_SCRUB") == "" {
		t.Skip("numeric pass scrub is a manual diagnostic; run with DIAG_SCRUB=1 to enable")
	}
	var traces []passTrace
	for _, seed := range []int64{101, 102, 103, 104, 105, 106, 107, 108} {
		m, ais := sweepMatch(6, seed, SkillHard, largeGeom, nil)
		classifyMatch(m, ais, diagTicks, seed, &traces)
	}
	perCause := map[passCause][]passTrace{}
	for _, tr := range traces {
		perCause[tr.cause] = append(perCause[tr.cause], tr)
	}
	for c := passCause(0); c < numPassCauses; c++ {
		list := perCause[c]
		if len(list) == 0 {
			continue
		}
		t.Logf("== %s (%d) ==", causeNames[c], len(list))
		for i, tr := range list {
			if i >= 8 {
				t.Logf("   ... and %d more", len(list)-8)
				break
			}
			t.Logf("   launch=%.0f laneLen=%.0f minDist=%.0f | mate=%v impact=%.0f ang=%.2f cap=%.0f | opp=%v along=%.0f perp=%.0f | recv=%d recon=%v",
				tr.launchSpeed, tr.laneLen, tr.minDist, tr.mateContacted, tr.mateImpactSpeed, tr.mateImpactAng, tr.capSpeed,
				tr.oppContacted, tr.oppAlong, tr.oppPerp, tr.recvID, tr.reconstructed)
			t.Logf("        receiver@closest: minDist=%.0f offLine=%.0f recvSpeed=%.0f align=%.2f relSpeed=%.0f | tgtGap launch=%.0f arrival=%.0f",
				tr.recvMinDist, tr.recvOffLine, tr.recvSpeed, tr.recvAlign, tr.recvRelSpeed, tr.recvTargetGap0, tr.recvTargetGapArr)
		}
	}
	// Aggregate the target-drift signal across ALL failed passes with a known receiver: was the
	// receiver near where the pass was aimed at LAUNCH, and did it stay there until ARRIVAL? This
	// distinguishes "carrier aimed at empty space" (gap0 large) from "receiver abandoned the spot the
	// pass was aimed at" (gap0 small, gapArr large) -- the two ways a pass misses an OPEN man.
	var n, onTargetAtLaunch, driftedOff, collecting, drifting int
	var sumGap0, sumGapArr float64
	for _, tr := range traces {
		if tr.recvID < 0 || math.IsInf(tr.recvTargetGap0, 1) {
			continue
		}
		n++
		sumGap0 += tr.recvTargetGap0
		if !math.IsInf(tr.recvTargetGapArr, 1) {
			sumGapArr += tr.recvTargetGapArr
		}
		if tr.recvTargetGap0 < 40 {
			onTargetAtLaunch++
			if tr.recvTargetGapArr > 60 {
				driftedOff++
			}
		}
		if tr.recvClosing > 0.2 {
			collecting++ // moving toward the ball (going to collect it)
		} else if tr.recvClosing < -0.2 {
			drifting++ // moving away from the ball (not collecting -- the drift bug)
		}
	}
	if n > 0 {
		t.Logf("TARGET-DRIFT over %d failed passes: mean gap0=%.0f gapArr=%.0f | on-target@launch=%d/%d drifted-off=%d | at closest: COLLECTING(towards)=%d DRIFTING(away)=%d neutral=%d",
			n, sumGap0/float64(n), sumGapArr/float64(n), onTargetAtLaunch, n, driftedOff, collecting, drifting, n-collecting-drifting)
	}
}

// TestLeverSweep is a reusable one-lever grid sweep for the tuning loop. It is SKIPPED by
// default (it is a working tool, not a gate); edit the lever/grid below and run it with
//
//	DIAG_LEVER=1 go test ./internal/control/ -run TestLeverSweep -v
//
// Each grid point is validated over the same disjoint seeds as the baseline, with the full
// cause histogram, so a change is judged on completion AND volume AND clears AND the bucket it
// targeted -- never on the headline %% alone.
func TestLeverSweep(t *testing.T) {
	if os.Getenv("DIAG_LEVER") == "" {
		t.Skip("lever sweep is a manual tuning tool; run with DIAG_LEVER=1 to enable")
	}
	seeds := diagSeeds(false)
	report(t, "baseline", aggregate(diagSweep(seeds, SkillHard, nil)))
	// Edit this table per tuning iteration. Example shape (no-op by default):
	for _, c := range leverCandidates {
		report(t, c.label, aggregate(diagSweep(seeds, SkillHard, c.mutate)))
	}
}

// leverCandidate is one tuning hypothesis for TestLeverSweep.
type leverCandidate struct {
	label  string
	mutate func(*aiTuning)
}

// leverCandidates is the current sweep table (empty by default). Populate during a tuning
// iteration with one lever varied across a grid.
// The committed reference is the ablation of the two passing wins (velocity compensation and
// the deepen-hot reception) -- re-runnable evidence that each contributes and they stack. Edit
// this table to sweep any other lever during a future tuning pass.
// The committed reference: the velocity-matched reception ablation (the overshoot fix). match=OFF
// is the prior deepenHot-only behaviour (receivers sprint to the meeting point and overshoot it);
// match=ON runs them ALONG the ball's line. onto trades alignment vs over-retention -- 0.55 is the
// shipped value (completion up ~6% with goals held; 0.70 retains so hard it stops creating shots).
// The hold-time release-valve ablation: OFF (the prior hoarding behaviour) vs the valve forcing an
// offload sooner (lower holdForceTicks). Judge on holds>5s AND completion/volume/scored together.
// The hold-time release-valve ablation: OFF reproduces the hoarding bug (single holds up to ~13s);
// ON forces a stuck carrier to move the ball on. It also lifts completion and CUTS turnovers
// (hoarding into a tackle is a turnover) -- the headline numbers below show both.
// Stable-receiving-spot ablation: hold=0 re-picks the support spot every tick (the old drifting
// behaviour that lets passes over-hit a moving receiver and hoard more); the final holds the spot
// until the ball moves ~50u, presenting a stationary target (+~2% completion over 50 seeds, tighter
// variance, scored-neutral, and a bonus drop in hoarding because the carrier always has a target).
var leverCandidates = []leverCandidate{
	{"hold=0 (drift)", func(t *aiTuning) { t.supportHoldBallMove = 0 }},
	{"hold=50 (final)", func(t *aiTuning) { t.supportHoldBallMove = 50 }},
}
