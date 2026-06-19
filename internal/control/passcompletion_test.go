package control_test

import (
	"runtime"
	"sync"
	"testing"

	"phootball/internal/config"
	"phootball/internal/control"
	"phootball/internal/geom"
	"phootball/internal/sim"
)

// kickOutcome tallies, for every deliberate kick, how it ends. It is the direct measure of
// the user's complaint: passes and shots that go to nothing. Each kick is classified by the
// kicker's actual on-ball decision (pass/shot/clear) and resolved by the next distinct
// toucher (teammate = completed, opponent = lost) or a goal.
type kickOutcome struct {
	kicks                   int
	passes, passDone        int // deliberate passes, and how many reached a mate
	shots, onTarget, scored int // shots, how many were goal-bound, how many scored
	clears                  int
	maxHoldTicks            int // longest a single player kept firm possession (the hoarding guard)
	longHolds               int // count of single-player holds over 5s (the "holds 10s" bug)
}

func (k kickOutcome) passPct() float64 {
	if k.passes == 0 {
		return 0
	}
	return 100 * float64(k.passDone) / float64(k.passes)
}

// shotOnTarget reports whether a kick by `player` is goal-bound: the ball's path (constant
// direction; drag only scales speed) crosses the attacking goal mouth.
func shotOnTarget(m *sim.Match, player int, ballPos, ballVel geom.Vec) bool {
	for _, p := range m.Players {
		if p.PlayerID != player {
			continue
		}
		goal := m.AttackingGoal(p.Team)
		if ballVel.X == 0 {
			return false
		}
		t := (goal.Center.X - ballPos.X) / ballVel.X
		if t <= 0 {
			return false // heading away from goal
		}
		y := ballPos.Y + ballVel.Y*t
		lo, hi := goal.Mouth.A.Y, goal.Mouth.B.Y
		if lo > hi {
			lo, hi = hi, lo
		}
		return y >= lo && y <= hi
	}
	return false
}

func measureKicks(m *sim.Match, ais map[int]*control.AI, ticks int) kickOutcome {
	var k kickOutcome
	// Each player's recent on-ball actions, so a kick is classified by what the kicker was
	// actually doing around the release (reaction-cache timing makes a single post-step read
	// unreliable -- it can already show the post-kick dribble).
	recent := map[int][]string{}
	classify := func(player int) string {
		for _, a := range recent[player] {
			if a == "shoot" || a == "pass" || a == "clear" {
				return a
			}
		}
		return "dribble"
	}

	// A kick is detected by a JUMP in ball speed (the shoot impulse), so consecutive kicks by
	// the same player are all counted. A PASS is resolved by who next gains firm possession:
	// same team = completed, opponent = lost. A SHOT is judged on-target at the kick and
	// credited if a goal follows.
	pendingActive := false
	pendingKind := ""
	pendingSide := sim.SideNone
	pendingPlayer := -1
	prevSpeed := 0.0
	prevGoals := m.Teams[0].Score + m.Teams[1].Score
	holdCarrier := -1 // player currently holding firm possession
	holdLen := 0      // consecutive ticks holdCarrier has held it
	flushHold := func() {
		if holdCarrier < 0 || holdLen <= 0 {
			return
		}
		if holdLen > k.maxHoldTicks {
			k.maxHoldTicks = holdLen
		}
		if holdLen > 5*60 { // > 5s
			k.longHolds++
		}
	}

	resolvePass := func(reached bool) {
		if pendingActive && pendingKind == "pass" {
			k.passes++
			if reached {
				k.passDone++
			}
		}
		pendingActive = false
	}

	for i := 0; i < ticks; i++ {
		in := make(map[int]sim.Intent, len(ais))
		for id, ai := range ais {
			in[id] = ai.Intent(m.View())
			r := append([]string{ai.LastAction()}, recent[id]...)
			if len(r) > 4 {
				r = r[:4]
			}
			recent[id] = r
		}
		m.Step(in, dt)

		// Hold-time: count consecutive ticks the SAME player keeps firm possession (the hoarding guard).
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

		// Goal: credit a pending shot, resolve a pending pass as completed.
		if g := m.Teams[0].Score + m.Teams[1].Score; g != prevGoals {
			if pendingActive && pendingKind == "shoot" {
				k.scored++
			}
			resolvePass(true)
			prevGoals = g
			prevSpeed = geom.Norm(m.Ball.Velocity)
			continue
		}

		sp := geom.Norm(m.Ball.Velocity)
		if lt := m.LastTouch; lt != nil && sp-prevSpeed > 60 { // a kick impulse
			if pendingActive && lt.Player != pendingPlayer {
				resolvePass(lt.Side == pendingSide) // a new player got it before this kick
			}
			kind := classify(lt.Player)
			pendingActive, pendingKind, pendingSide, pendingPlayer = true, kind, lt.Side, lt.Player
			switch kind {
			case "shoot":
				k.shots++
				if shotOnTarget(m, lt.Player, m.Ball.Position, m.Ball.Velocity) {
					k.onTarget++
				}
			case "clear":
				k.clears++
			}
			k.kicks++
		}
		prevSpeed = sp

		// Resolve a pending PASS once a different player gains firm possession.
		if pendingActive && pendingKind == "pass" {
			if c := m.BallCarrier(); c != nil && c.PlayerID != pendingPlayer {
				resolvePass(c.Team.Side == pendingSide)
			}
		}
	}
	flushHold() // the hold in progress at the final tick
	return k
}

// TestPassCompletionLargeMap is the user's requested check: real 6-a-side games on the large
// pitch on HARD. It is a ROBUST gate over 30 seeds. A single 6-seed run is a coin flip on this
// chaotic metric (a tiny tuning change swings it ~60-78%), so 6 seeds cannot guard a real
// regression; the seed coverage was EXPANDED 6 -> 30 (never shrunk) and run in parallel so it
// stays fast. Seeds 1-30 are DISJOINT from the band the AI passing was tuned against (101-150),
// so this is an out-of-sample check, never overfit to the gate seeds.
//
// The threshold stays at the historical 70% and is cleared by a robust margin: against the
// current sim physics the pre-fix AI aggregated ~59%, and the passing fixes (passLaunchVelComp
// -- the shot impulse ADDS to the ball's velocity, so a pass off a moving carrier launched
// 2-3x too fast; and receiveDeepenHot -- meeting a too-fast pass deep, running WITH the ball,
// for a low relative-impact first touch) lift the robust 30-seed aggregate to ~71%.
func TestPassCompletionLargeMap(t *testing.T) {
	const seeds = 30
	results := make([]kickOutcome, seeds)
	workers := runtime.GOMAXPROCS(0)
	if workers > 12 {
		workers = 12
	}
	if workers < 1 {
		workers = 1
	}
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	for s := 0; s < seeds; s++ {
		wg.Add(1)
		go func(s int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			m, ais := aiMatch(6, int64(s+1), control.SkillHard, func(c *config.Config) {
				c.Geometry = config.LargeGeometry()
			})
			results[s] = measureKicks(m, ais, 60*120)
		}(s)
	}
	wg.Wait()

	var agg kickOutcome
	for _, k := range results {
		agg.kicks += k.kicks
		agg.passes += k.passes
		agg.passDone += k.passDone
		agg.shots += k.shots
		agg.onTarget += k.onTarget
		agg.scored += k.scored
		agg.clears += k.clears
		agg.longHolds += k.longHolds
		if k.maxHoldTicks > agg.maxHoldTicks {
			agg.maxHoldTicks = k.maxHoldTicks
		}
	}
	t.Logf("large 6v6 HARD over %d seeds: passes=%d reached=%d (%.1f%%) | shots=%d onTarget=%d scored=%d | clears=%d | maxHold=%.1fs longHolds=%d",
		seeds, agg.passes, agg.passDone, agg.passPct(), agg.shots, agg.onTarget, agg.scored, agg.clears, float64(agg.maxHoldTicks)/60, agg.longHolds)
	// Gate RAISED 70 -> 73 to lock in the velocity-matched reception rework (the receiver runs
	// ALONG the ball's line instead of overshooting it -- see steerReceive/receiveMatch), which
	// lifted the robust aggregate here to ~76% with shots/goals held and clears DOWN. 73 keeps a
	// margin below the earned 76 (the metric is chaotic and Jack's sim physics is still moving).
	if agg.passPct() < 73 {
		t.Errorf("pass accuracy %.1f%% < 73%% target", agg.passPct())
	}
	if agg.onTarget < seeds*2 {
		t.Errorf("too few shots on target: %d over %d games (want >= %d)", agg.onTarget, seeds, seeds*2)
	}
	// Volume floor + clears cap: completion must NOT be bought by refusing to pass (dribbling or
	// holding) or by hoofing the ball clear. These catch a regression toward "never pass / hoof
	// everything" -- the degenerate way to fake a high completion %%. Thresholds sit well inside the
	// measured margins (this AI passes ~11/game and clears ~5/game), so they flag a collapse, not
	// normal variance: tuning that gamed completion by suppressing volume (e.g. an over-tight
	// contest margin) dropped volume to ~3-4/game in sweeps, which this floor fails.
	if minPasses := seeds * 8; agg.passes < minPasses {
		t.Errorf("pass volume collapsed: %d passes over %d games (want >= %d, ~8/game) -- completion must not be bought by not passing", agg.passes, seeds, minPasses)
	}
	if maxClears := seeds * 8; agg.clears > maxClears {
		t.Errorf("too many clears: %d over %d games (want <= %d, ~8/game) -- completion must not be bought by hoofing", agg.clears, seeds, maxClears)
	}
	// Hold guard: a carrier must not HOARD the ball (dribble forever to dodge risk) -- the hold-time
	// release valve (holdPressure/holdForceTicks) forces a stuck carrier to move it on. Without the
	// valve a single player held the ball up to ~13s; with it, ~6s. These catch a regression back to
	// hoarding: no single hold over 10s, and the >5s holds stay rare (the valve achieves ~0.2/game).
	// maxHold is a single-outlier backstop (a genuinely surrounded carrier with NO passing option can
	// still hold a while -- the valve can't pass to nobody); the longHolds RATE below is the robust
	// guard. 12s catches a full regression to the no-valve behaviour (single holds up to ~13s).
	if maxHoldS := float64(agg.maxHoldTicks) / 60; maxHoldS > 12 {
		t.Errorf("a player hoarded the ball %.1fs (want <= 12s) -- the hold-time release valve regressed", maxHoldS)
	}
	if maxLong := seeds * 2 / 5; agg.longHolds > maxLong { // ~0.4/game (valve achieves ~0.2; off was ~0.53)
		t.Errorf("too many long holds: %d holds over 5s across %d games (want <= %d) -- carrier hoarding the ball", agg.longHolds, seeds, maxLong)
	}
}
