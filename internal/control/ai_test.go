package control_test

import (
	"math"
	"testing"

	"phootball/internal/config"
	"phootball/internal/control"
	"phootball/internal/geom"
	"phootball/internal/sim"
)

const dt = 1.0 / 60.0

// aiMatch builds a match of the given team size with every player driven by an AI at the
// given skill, applying any config mutation first.
func aiMatch(teamSize int, seed int64, skill control.Skill, mutate func(*config.Config)) (*sim.Match, map[int]*control.AI) {
	cfg := config.Default()
	cfg.Seed = seed
	if mutate != nil {
		mutate(&cfg)
	}
	field := sim.NewFieldFromGeometry(cfg.Geometry)
	m := sim.BuildMatchFromConfig(field, teamSize, cfg)
	ais := make(map[int]*control.AI, len(m.Players))
	for _, p := range m.Players {
		ais[p.PlayerID] = control.NewAISkill(p.PlayerID, skill)
	}
	return m, ais
}

// stepAll advances the match one tick with every AI's intent.
func stepAll(m *sim.Match, ais map[int]*control.AI) {
	in := make(map[int]sim.Intent, len(ais))
	for id, ai := range ais {
		in[id] = ai.Intent(m.View())
	}
	m.Step(in, dt)
}

func run(m *sim.Match, ais map[int]*control.AI, ticks int, onTick func(tick int)) {
	for i := 0; i < ticks; i++ {
		stepAll(m, ais)
		if onTick != nil {
			onTick(i)
		}
	}
}

// teamMatch builds a match where the two sides can run different skill tiers, for
// head-to-head comparisons.
func teamMatch(teamSize int, seed int64, left, right control.Skill, mutate func(*config.Config)) (*sim.Match, map[int]*control.AI) {
	cfg := config.Default()
	cfg.Seed = seed
	if mutate != nil {
		mutate(&cfg)
	}
	field := sim.NewFieldFromGeometry(cfg.Geometry)
	m := sim.BuildMatchFromConfig(field, teamSize, cfg)
	ais := make(map[int]*control.AI, len(m.Players))
	for _, p := range m.Players {
		s := left
		if p.Team.Side == sim.SideRight {
			s = right
		}
		ais[p.PlayerID] = control.NewAISkill(p.PlayerID, s)
	}
	return m, ais
}

// matchStats holds emergent metrics for a simulated match.
type matchStats struct {
	goalsL, goalsR int
	passes         int // completed same-team carrier-to-carrier handovers
	turnovers      int // possession changes between teams
}

// gather runs a match and measures passing/turnovers/goals from the carrier sequence.
func gather(m *sim.Match, ais map[int]*control.AI, ticks int) matchStats {
	var st matchStats
	lastCarrier, lastSide := -1, sim.SideNone
	run(m, ais, ticks, func(tick int) {
		c := m.BallCarrier()
		if c == nil {
			return
		}
		if c.PlayerID != lastCarrier {
			if lastSide != sim.SideNone {
				if c.Team.Side == lastSide {
					st.passes++
				} else {
					st.turnovers++
				}
			}
			lastCarrier, lastSide = c.PlayerID, c.Team.Side
		}
	})
	st.goalsL, st.goalsR = m.Teams[0].Score, m.Teams[1].Score
	return st
}

// TestQualitySweep logs emergent metrics across team sizes and seeds and asserts the AI, in
// aggregate, both scores and completes passes. Per-size totals are logged (and vary -- a
// tight 1v1 against an elite keeper can be goalless, like real football), so the assertions
// are on the aggregate to stay robust to that variance.
func TestQualitySweep(t *testing.T) {
	var totGoals, totPasses int
	for _, n := range []int{2, 3, 4, 5, 6} {
		var goals, passes, turnovers int
		for seed := int64(1); seed <= 4; seed++ {
			m, ais := aiMatch(n, seed, control.SkillHard, nil)
			st := gather(m, ais, 60*120)
			goals += st.goalsL + st.goalsR
			passes += st.passes
			turnovers += st.turnovers
		}
		t.Logf("n=%d over 4x120s: goals=%d passes=%d turnovers=%d", n, goals, passes, turnovers)
		totGoals += goals
		totPasses += passes
	}
	if totPasses == 0 {
		t.Errorf("no completed passes across the whole sweep (passing is broken)")
	}
	if totGoals == 0 {
		t.Errorf("no goals across the whole sweep (attacking is broken)")
	}
}

// TestSkillTiers checks the difficulty knob is meaningful: a Hard team should out-score an
// Easy team in aggregate across seeds (with the stronger side alternating to cancel any
// kickoff-side advantage).
func TestSkillTiers(t *testing.T) {
	hardGoals, easyGoals := 0, 0
	for seed := int64(1); seed <= 6; seed++ {
		left, right := control.SkillHard, control.SkillEasy
		if seed%2 == 0 {
			left, right = control.SkillEasy, control.SkillHard
		}
		m, ais := teamMatch(4, seed, left, right, nil)
		st := gather(m, ais, 60*120)
		if left == control.SkillHard {
			hardGoals += st.goalsL
			easyGoals += st.goalsR
		} else {
			hardGoals += st.goalsR
			easyGoals += st.goalsL
		}
	}
	t.Logf("aggregate over 6x120s: Hard %d - %d Easy", hardGoals, easyGoals)
	if hardGoals <= easyGoals {
		t.Errorf("Hard AI did not out-score Easy AI (%d vs %d): difficulty knob ineffective", hardGoals, easyGoals)
	}
}

// TestCleanFirstTouch checks that a receiver controls an incoming pass (faces it, traps the
// damped touch) instead of letting it bounce away. Only the receiver acts; everyone else
// idles, so the touch is tested in isolation.
func TestCleanFirstTouch(t *testing.T) {
	m, ais := aiMatch(2, 1, control.SkillHard, nil)
	recv := m.Teams[0].Players[1] // an outfielder
	recv.Position = geom.NewVec(300, 150)
	m.Ball.Position = geom.NewVec(470, 150)
	m.Ball.Velocity = geom.NewVec(-230, 0) // a firm pass straight at the receiver
	for i := 0; i < 70; i++ {
		in := map[int]sim.Intent{recv.PlayerID: ais[recv.PlayerID].Intent(m.View())}
		m.Step(in, dt)
	}
	gap := geom.Dist(recv.Position, m.Ball.Position) - recv.Radius() - m.Ball.Radius()
	if gap > 12 {
		t.Errorf("receiver failed to bring the pass under control (gap %.1f) -- ball bounced away", gap)
	}
	if geom.Norm(m.Ball.Velocity) > 130 {
		t.Errorf("ball was not damped on the first touch (speed %.0f)", geom.Norm(m.Ball.Velocity))
	}
}

// TestPrechargeClear checks that a defender meeting a contested loose ball deep in its own
// third clears it upfield with power (pre-charging the kick as it arrives).
func TestPrechargeClear(t *testing.T) {
	m, ais := aiMatch(2, 1, control.SkillHard, nil)
	def := m.Teams[0].Players[1] // left team defends the left goal, clears toward +x
	def.Position = geom.NewVec(125, 300)
	m.Ball.Position = geom.NewVec(205, 300)
	m.Ball.Velocity = geom.NewVec(0, 0)
	foe := m.Teams[1].Players[1]
	foe.Position = geom.NewVec(245, 300) // close enough to pressure the loose ball
	peakVx := 0.0
	for i := 0; i < 80; i++ {
		// Only the defender acts; the opponent idles but its presence creates the pressure.
		in := map[int]sim.Intent{def.PlayerID: ais[def.PlayerID].Intent(m.View())}
		m.Step(in, dt)
		if m.Ball.Velocity.X > peakVx {
			peakVx = m.Ball.Velocity.X
		}
	}
	// A charged clear leaves clearly harder than a tap (~175); the pre-charge proves itself
	// by the peak upfield speed it imparts.
	if peakVx < 250 {
		t.Errorf("defender did not pre-charge a powerful upfield clear (peak vx %.0f, want >250)", peakVx)
	}
}

// TestFlowQuality measures watchability metrics over Impossible matches: passing happens
// and links into sequences, trap (right click) is NOT over-used, and players don't grind to
// a stuck halt overlapping each other. These guard the fluid-play work.
func TestFlowQuality(t *testing.T) {
	var passes, seqMax, trapTicks, moveTicks, grind int
	matches := 0
	for _, n := range []int{3, 5} {
		for seed := int64(1); seed <= 2; seed++ {
			matches++
			m, ais := aiMatch(n, seed, control.SkillImpossible, nil)
			lastC, lastS, seqCur := -1, sim.SideNone, 0
			ticks := 60 * 100
			for i := 0; i < ticks; i++ {
				in := map[int]sim.Intent{}
				for id, ai := range ais {
					it := ai.Intent(m.View())
					in[id] = it
					if it.Trap {
						trapTicks++
					}
					if geom.Norm(it.Move) > 0.01 && it.Throttle > 0.01 {
						moveTicks++
					}
				}
				m.Step(in, dt)
				if c := m.BallCarrier(); c != nil && c.PlayerID != lastC {
					if lastS == c.Team.Side {
						passes++
						seqCur++
						if seqCur > seqMax {
							seqMax = seqCur
						}
					} else if lastS != sim.SideNone {
						seqCur = 0
					}
					lastC, lastS = c.PlayerID, c.Team.Side
				}
				for x := 0; x < len(m.Players); x++ {
					for y := x + 1; y < len(m.Players); y++ {
						pa, pb := m.Players[x], m.Players[y]
						if geom.Dist(pa.Position, pb.Position)-pa.Radius()-pb.Radius() < 3 &&
							geom.Norm(pa.Velocity) < 18 && geom.Norm(pb.Velocity) < 18 {
							grind++
						}
					}
				}
			}
		}
	}
	trapPct := 100 * float64(trapTicks) / float64(moveTicks+1)
	grindPerTick := float64(grind) / float64(matches*60*100)
	t.Logf("flow: passes=%d seqMax=%d trap=%.0f%% grind/tick=%.2f", passes, seqMax, trapPct, grindPerTick)
	if passes < matches*3 {
		t.Errorf("too few completed passes (%d over %d matches): passing not flowing", passes, matches)
	}
	if seqMax < 2 {
		t.Errorf("no passing sequence linked (seqMax=%d)", seqMax)
	}
	if trapPct > 35 {
		t.Errorf("trap (right click) over-used: %.0f%% of moving ticks", trapPct)
	}
	if grindPerTick > 0.5 {
		t.Errorf("players grinding stuck against each other (%.2f overlap-pairs/tick)", grindPerTick)
	}
}

// TestShootsWhenOpen checks the AI takes a clear chance instead of declining it: a carrier
// in range with an open lane at goal and no pass pressure should get a shot away.
func TestShootsWhenOpen(t *testing.T) {
	m, ais := aiMatch(3, 1, control.SkillImpossible, nil)
	me := m.Teams[0].Players[1] // left team, attacks the right goal
	for _, q := range m.Players {
		if q != me {
			q.Position = geom.NewVec(q.Position.X, 40) // clear the path to goal
		}
	}
	me.Position = geom.NewVec(700, 340) // ~240 from the right goal, central, clear lane
	me.Facing = geom.NewVec(1, 0)
	m.Ball.Position = me.Position.Add(geom.NewVec(me.Radius()+m.Ball.Radius(), 0))
	shot := false
	for i := 0; i < 120 && !shot; i++ {
		m.Step(map[int]sim.Intent{me.PlayerID: ais[me.PlayerID].Intent(m.View())}, dt)
		if m.Ball.Velocity.X > 250 { // a real shot toward the goal
			shot = true
		}
	}
	if !shot {
		t.Errorf("AI declined a clear in-range chance at goal instead of shooting")
	}
}

// TestKeeperQuickClear checks a keeper with the ball and no safe pass boots it clear quickly
// (low charge, loose aim) rather than dwelling on it.
func TestKeeperQuickClear(t *testing.T) {
	m, ais := aiMatch(3, 1, control.SkillImpossible, nil)
	var keeper *sim.Player
	for _, pl := range m.Teams[0].Players {
		if pl.Role == sim.RoleGoalkeeper {
			keeper = pl
		}
	}
	// Keeper with the ball deep, teammates marked (opponents sitting on them) so no easy pass.
	keeper.Position = geom.NewVec(110, 340)
	m.Ball.Position = keeper.Position.Add(geom.NewVec(keeper.Radius()+m.Ball.Radius(), 0))
	for i, opp := range m.Teams[1].Players {
		if i < len(m.Teams[0].Players) {
			mate := m.Teams[0].Players[i]
			opp.Position = mate.Position.Add(geom.NewVec(6, 0)) // mark every outfielder tightly
		}
	}
	cleared := false
	for i := 0; i < 45; i++ { // ~0.75s: a quick clear should be away well within this
		in := map[int]sim.Intent{}
		for id, ai := range ais {
			in[id] = ai.Intent(m.View())
		}
		m.Step(in, dt)
		if m.Ball.Velocity.X > 180 && m.Ball.Position.X > 140 { // booted upfield
			cleared = true
			break
		}
	}
	if !cleared {
		t.Errorf("keeper did not clear the ball quickly")
	}
}

// TestBackBallShootRecovers reproduces the reported case: a player wanting to shoot at goal
// with the ball at its BACK. It must scoop the ball round to the front and get a shot away
// (not freeze waiting on it), and it must not jitter doing so.
func TestBackBallShootRecovers(t *testing.T) {
	m, ais := aiMatch(3, 1, control.SkillImpossible, nil)
	me := m.Teams[0].Players[1] // left team attacks +x (right goal)
	for _, p := range m.Players {
		if p != me {
			p.Position = geom.NewVec(p.Position.X, 40) // clear everyone out of the way
		}
	}
	me.Position = geom.NewVec(720, 340)
	me.Facing = geom.NewVec(1, 0)
	m.Ball.Position = geom.NewVec(690, 340) // ball behind the player (away from goal)

	prevAng, prevSign, reversals := math.Atan2(me.Facing.Y, me.Facing.X), 0.0, 0
	shot := false
	for i := 0; i < 240 && !shot; i++ {
		m.Step(map[int]sim.Intent{me.PlayerID: ais[me.PlayerID].Intent(m.View())}, dt)
		ang := math.Atan2(me.Facing.Y, me.Facing.X)
		d := ang - prevAng
		for d > math.Pi {
			d -= 2 * math.Pi
		}
		for d < -math.Pi {
			d += 2 * math.Pi
		}
		if math.Abs(d) > 0.02 {
			if s := math.Copysign(1, d); prevSign != 0 && s != prevSign {
				reversals++
			} else {
				prevSign = s
			}
		}
		prevAng = ang
		if m.Ball.Velocity.X > 250 { // a real shot toward the right goal
			shot = true
		}
	}
	if !shot {
		t.Errorf("player never recovered a back-positioned ball into a shot at goal")
	}
	if reversals > 8 {
		t.Errorf("facing jittered while recovering the ball (%d reversals)", reversals)
	}
}

// TestNoFacingJitter checks that in a real match players' facing does not vibrate: a healthy
// player changes facing direction only a couple of times a second (real turns), not many.
// This guards the reaction-cache far-aim fix and the smooth-turn rules.
func TestNoFacingJitter(t *testing.T) {
	for _, skill := range []control.Skill{control.SkillHard, control.SkillImpossible} {
		m, ais := aiMatch(4, 3, skill, nil)
		prevAng := map[int]float64{}
		prevSign := map[int]float64{}
		reversals := map[int]int{}
		ticks := 60 * 45
		for i := 0; i < ticks; i++ {
			in := map[int]sim.Intent{}
			for id, ai := range ais {
				in[id] = ai.Intent(m.View())
			}
			m.Step(in, dt)
			for _, pl := range m.Players {
				ang := math.Atan2(pl.Facing.Y, pl.Facing.X)
				if pa, ok := prevAng[pl.PlayerID]; ok {
					d := ang - pa
					for d > math.Pi {
						d -= 2 * math.Pi
					}
					for d < -math.Pi {
						d += 2 * math.Pi
					}
					if math.Abs(d) > 0.02 {
						s := math.Copysign(1, d)
						if ps, ok := prevSign[pl.PlayerID]; ok && s != ps {
							reversals[pl.PlayerID]++
						}
						prevSign[pl.PlayerID] = s
					}
				}
				prevAng[pl.PlayerID] = ang
			}
		}
		secs := float64(ticks) / 60
		worst := 0
		for _, r := range reversals {
			if r > worst {
				worst = r
			}
		}
		// More than ~4 facing reversals/sec sustained is vibration, not normal play.
		if float64(worst)/secs > 4 {
			t.Errorf("skill %d: a player's facing reversed %.1f times/sec (jitter)", skill, float64(worst)/secs)
		}
	}
}

// TestNoKickoffDeadlock checks the central regression: from kickoff the ball must be put
// into play quickly, and players must not all pile onto the spot.
func TestNoKickoffDeadlock(t *testing.T) {
	for _, n := range []int{1, 2, 3, 4, 5, 6} {
		m, ais := aiMatch(n, 7, control.SkillHard, nil)
		start := m.Ball.Position
		moved := false
		var maxNearTeam int
		run(m, ais, 360, func(tick int) {
			if geom.Dist(m.Ball.Position, start) > 30 {
				moved = true
			}
			if tick == 30 {
				maxNearTeam = maxPlayersNearBall(m, m.Ball.Radius()*6)
			}
		})
		if !moved {
			t.Errorf("n=%d: ball never left the kickoff spot within 6s", n)
		}
		if maxNearTeam > 1 {
			t.Errorf("n=%d: %d players from one team swarmed the ball at tick 30 (want <=1)", n, maxNearTeam)
		}
	}
}

// TestLargeMapManyPlayers checks the AI works on a big pitch with large squads: no kickoff
// deadlock, the ball is put into play, and players stay spread.
func TestLargeMapManyPlayers(t *testing.T) {
	for _, n := range []int{6, 8} {
		m, ais := aiMatch(n, 4, control.SkillHard, func(c *config.Config) {
			c.Geometry = config.LargeGeometry()
		})
		start := m.Ball.Position
		moved := false
		run(m, ais, 360, func(tick int) {
			if geom.Dist(m.Ball.Position, start) > 40 {
				moved = true
			}
		})
		if !moved {
			t.Errorf("large map n=%d: ball never left the kickoff spot", n)
		}
		if got := maxPlayersNearBall(m, m.Ball.Radius()*8); got > 3 {
			t.Errorf("large map n=%d: %d players swarming the ball (want few)", n, got)
		}
	}
}

// maxPlayersNearBall returns the largest number of players from a single team within
// radius of the ball.
func maxPlayersNearBall(m *sim.Match, radius float64) int {
	worst := 0
	for _, tm := range m.Teams {
		n := 0
		for _, p := range tm.Players {
			if geom.Dist(p.Position, m.Ball.Position) < radius+p.Radius() {
				n++
			}
		}
		if n > worst {
			worst = n
		}
	}
	return worst
}

// TestSpread checks teammates settle into distinct positions rather than stacking.
func TestSpread(t *testing.T) {
	for _, n := range []int{3, 4, 5, 6} {
		m, ais := aiMatch(n, 11, control.SkillHard, nil)
		run(m, ais, 90, nil)
		for _, tm := range m.Teams {
			minD := math.Inf(1)
			for i := 0; i < len(tm.Players); i++ {
				for j := i + 1; j < len(tm.Players); j++ {
					if d := geom.Dist(tm.Players[i].Position, tm.Players[j].Position); d < minD {
						minD = d
					}
				}
			}
			if minD < tm.Players[0].Radius()*2 {
				t.Errorf("n=%d team %v: closest teammates only %.1f apart (overlapping)", n, tm.Side, minD)
			}
		}
	}
}

// TestGoalsScored runs AI-vs-AI matches across several team sizes and expects goals to be
// scored in aggregate (attacking works) without an absurd avalanche (defending works). It
// aggregates because any single match can be goalless by chance.
func TestGoalsScored(t *testing.T) {
	total, matches := 0, 0
	for _, n := range []int{3, 4, 5, 6} {
		for seed := int64(1); seed <= 2; seed++ {
			m, ais := aiMatch(n, seed, control.SkillHard, nil)
			run(m, ais, 60*120, nil)
			total += m.Teams[0].Score + m.Teams[1].Score
			matches++
		}
	}
	if total == 0 {
		t.Errorf("no goals across %d AI-vs-AI matches (attacking is broken)", matches)
	}
	if total > 40*matches {
		t.Errorf("absurd goal count %d over %d matches (defending is broken)", total, matches)
	}
	t.Logf("%d goals across %d matches (%.1f/match)", total, matches, float64(total)/float64(matches))
}

// TestDeterminism checks two identical runs produce identical state (the AI introduces no
// nondeterminism: no random source, no map-order-dependent decisions).
func TestDeterminism(t *testing.T) {
	run1, ais1 := aiMatch(5, 42, control.SkillHard, nil)
	run2, ais2 := aiMatch(5, 42, control.SkillHard, nil)
	run(run1, ais1, 1200, nil)
	run(run2, ais2, 1200, nil)
	if run1.Teams[0].Score != run2.Teams[0].Score || run1.Teams[1].Score != run2.Teams[1].Score {
		t.Fatalf("scores diverged: %d-%d vs %d-%d", run1.Teams[0].Score, run1.Teams[1].Score, run2.Teams[0].Score, run2.Teams[1].Score)
	}
	if geom.Dist(run1.Ball.Position, run2.Ball.Position) > 1e-6 {
		t.Fatalf("ball positions diverged: %v vs %v", run1.Ball.Position, run2.Ball.Position)
	}
}

// TestNoClampJitter enables offside and the goal-area limit and checks the AI keeps itself
// legal -- attackers should rarely be caught beyond the offside line, meaning they are not
// fighting the soft clamp.
func TestNoClampJitter(t *testing.T) {
	m, ais := aiMatch(5, 9, control.SkillHard, func(c *config.Config) {
		c.Ruleset.OffsideEnabled = true
		c.Ruleset.OffsideFrac = 2.0 / 3.0
		c.Ruleset.GoalAreaMaxPlayers = 1
		c.Ruleset.Enforcement = config.EnforceWarnEvict
		c.Ruleset.EvictGrace = 0.5
	})
	// A player that respects the line crosses it a handful of times (genuine transitions
	// when possession turns over). A player FIGHTING the soft clamp oscillates across it
	// constantly. We count crossings per player and flag rapid oscillation.
	wasPast := map[int]bool{}
	crossings := map[int]int{}
	const ticks = 1200
	run(m, ais, ticks, func(tick int) {
		carrier := m.BallCarrier()
		for _, tm := range m.Teams {
			possessing := carrier != nil && carrier.Team == tm
			line := m.Field.OffsideLineX(tm.Side, 2.0/3.0)
			for _, p := range tm.Players {
				if p.Role == sim.RoleGoalkeeper {
					continue
				}
				past := !possessing && ((tm.Side == sim.SideLeft && p.Position.X > line+p.Radius()) ||
					(tm.Side == sim.SideRight && p.Position.X < line-p.Radius()))
				if past != wasPast[p.PlayerID] {
					crossings[p.PlayerID]++
					wasPast[p.PlayerID] = past
				}
			}
		}
	})
	worst := 0
	for _, c := range crossings {
		if c > worst {
			worst = c
		}
	}
	// At 60Hz over 20s, more than ~40 crossings means a player is buzzing the line.
	if worst > 40 {
		t.Errorf("a player crossed the offside line %d times in 20s (fighting the clamp)", worst)
	}
}

// TestKeeperSave fires several central-ish shots at the keeper (only the keeper reacts) and
// checks it saves the majority -- a competent keeper stops straight shots, even if a corner
// occasionally beats it.
func TestKeeperSave(t *testing.T) {
	saves, shots := 0, 0
	for _, off := range []float64{-18, -6, 0, 6, 18} {
		for _, spd := range []float64{230, 300} {
			shots++
			m, ais := aiMatch(2, 5, control.SkillHard, nil)
			goal := m.Field.RightGoal.Center // right team defends the right goal
			m.Ball.Position = geom.NewVec(goal.X-220, goal.Y+off)
			m.Ball.Velocity = geom.NewVec(spd, 0)
			keeperID := -1
			for _, p := range m.Teams[1].Players {
				if p.Role == sim.RoleGoalkeeper {
					keeperID = p.PlayerID
				}
			}
			conceded := false
			for i := 0; i < 90; i++ {
				in := map[int]sim.Intent{keeperID: ais[keeperID].Intent(m.View())}
				before := m.Teams[0].Score
				m.Step(in, dt)
				if m.Teams[0].Score > before {
					conceded = true
					break
				}
			}
			if !conceded {
				saves++
			}
		}
	}
	if saves*100 < shots*60 {
		t.Errorf("keeper only saved %d/%d central shots (want a majority)", saves, shots)
	}
}
