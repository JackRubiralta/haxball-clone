package control_test

// Committed "good football" scenario suite (directional mode, large pitch). The aggregate
// pass-completion gate (passcompletion_test.go) proves the AI keeps the ball, but it cannot
// prove the AI FINISHES the chances that define good football: scoring a 2v1/3v1 overload,
// converting a fast break, or beating a marker with a give-and-go. These tests place specific
// situations and assert the attacking algo AI resolves them across a seed band (a pass RATE /
// scored-within-N-ticks, never a single brittle seed -- the sim is chaotic).
//
// All scenarios attack as SideLeft (toward the +x / right goal) on config.LargeGeometry() in
// config.MoveDirectional, with every player the user-facing algo controller (SkillAlgo). They
// build with eval.BuildSizedWith for asymmetric rosters, then reposition players/ball directly
// (the build's formation is overwritten; nothing re-centres on Step -- see eval.BuildSizedWith).
//
// Run `SCENARIO_MEASURE=1 go test ./internal/control/ -run TestScenario -v` to print the raw
// rates without failing (the tuning loop uses this); without it the committed thresholds gate.

import (
	"os"
	"sort"
	"testing"

	"phootball/internal/config"
	"phootball/internal/control"
	"phootball/internal/eval"
	"phootball/internal/geom"
	"phootball/internal/sim"
)

// largeDirectional is the scenario config: the large pitch in the directional move model (the
// project default, set explicitly so the suite asserts the mode it claims to test).
func largeDirectional(c *config.Config) {
	c.Geometry = config.LargeGeometry()
	c.Tuning.MoveModel = config.MoveDirectional
}

// scenarioMatch builds an asymmetric large-pitch directional match with every player an algo AI.
func scenarioMatch(homeSize, awaySize int, seed int64) eval.Match {
	return eval.BuildSizedWith(homeSize, awaySize, seed, largeDirectional,
		func(id int, _ sim.Side) control.Controller { return control.NewAISkill(id, control.SkillAlgo) })
}

// sidePlayers returns one side's players sorted by PlayerID (the build's stable formation order:
// for a team of size >= 2, index 0 is the keeper, then outfielders back-to-front).
func sidePlayers(m *sim.Match, side sim.Side) []*sim.Player {
	var out []*sim.Player
	for _, p := range m.Players {
		if p.Team.Side == side {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PlayerID < out[j].PlayerID })
	return out
}

// place sets a player's position and facing and stops it dead (a clean scenario start).
func place(p *sim.Player, pos, face geom.Vec) {
	p.Position = pos
	p.HomePosition = pos
	p.Velocity = geom.Vec{}
	p.Acceleration = geom.Vec{}
	p.Facing = geom.Unit(face)
}

// ballAtFeet pins the ball just in front of p (on its facing side) at rest, so p controls it.
func ballAtFeet(m *sim.Match, p *sim.Player) {
	m.Ball.Position = p.Position.Add(geom.Unit(p.Facing).Scale(p.Radius() + m.Ball.Radius() + 1))
	m.Ball.Velocity = geom.Vec{}
	m.Ball.Acceleration = geom.Vec{}
}

// jit is a deterministic per-seed jitter in [-amp, amp], so a scenario is seed-banded (not one
// brittle layout) without any RNG.
func jit(seed int64, salt, amp float64) float64 {
	h := (seed*2654435761 + int64(salt)*40503) & 0xffff
	return (float64(h)/65535.0*2 - 1) * amp
}

// scenarioOutcome is one scenario instance's result for the ATTACKING side (always SideLeft).
type scenarioOutcome struct {
	scored   bool
	tick     int // tick the goal went in (or maxTicks if none)
	passes   int // completed SideLeft carrier-to-carrier handovers during the run
	maxBallX float64
}

// runScenario builds a sized match, applies setup, and steps up to maxTicks, returning the
// attacking side's outcome (scored / ticks / completed passes / furthest the ball advanced).
func runScenario(homeSize, awaySize int, seed int64, maxTicks int, setup func(m *sim.Match, seed int64)) scenarioOutcome {
	mt := scenarioMatch(homeSize, awaySize, seed)
	m := mt.M
	setup(m, seed)
	out := scenarioOutcome{tick: maxTicks, maxBallX: m.Ball.Position.X}
	start := m.Teams[0].Score
	lastCarrier, lastSide := -1, sim.SideNone
	for i := 0; i < maxTicks; i++ {
		mt.Step()
		if c := m.BallCarrier(); c != nil && c.PlayerID != lastCarrier {
			if lastSide == sim.SideLeft && c.Team.Side == sim.SideLeft {
				out.passes++
			}
			lastCarrier, lastSide = c.PlayerID, c.Team.Side
		}
		if x := m.Ball.Position.X; x > out.maxBallX {
			out.maxBallX = x
		}
		if m.Teams[0].Score > start {
			out.scored, out.tick = true, i
			return out
		}
	}
	return out
}

// scenarioBand runs a setup over seeds [1..n] and aggregates the scored/passing rates.
type bandResult struct {
	n, scored, withPass int
	totalPasses         int
	avgScoreTick        float64
	reachedShot         int     // instances whose ball reached enemy shoot range (x>960 on the large pitch)
	avgMaxBallX         float64 // mean furthest ball advancement, a diagnostic for stalled attacks
}

func scenarioBand(homeSize, awaySize, n, maxTicks int, setup func(*sim.Match, int64)) bandResult {
	var br bandResult
	br.n = n
	var tickSum int
	var maxXSum float64
	for s := int64(1); s <= int64(n); s++ {
		o := runScenario(homeSize, awaySize, s, maxTicks, setup)
		if o.scored {
			br.scored++
			tickSum += o.tick
		}
		if o.passes > 0 {
			br.withPass++
		}
		br.totalPasses += o.passes
		maxXSum += o.maxBallX
		if o.maxBallX > 960 {
			br.reachedShot++
		}
	}
	if br.scored > 0 {
		br.avgScoreTick = float64(tickSum) / float64(br.scored)
	}
	br.avgMaxBallX = maxXSum / float64(n)
	return br
}

// gateScenario logs the band result and, unless SCENARIO_MEASURE is set, asserts the floors:
// minScored (goals scored across the band), minWithPass (instances that used a completed pass --
// proving a passing move, not a solo dribble), and minReached (instances whose attack reached the
// enemy shooting range -- proving the AI progressed the ball / beat its marker even when the final
// finish was denied). A floor of 0 disables that check.
func gateScenario(t *testing.T, name string, br bandResult, minScored, minWithPass, minReached int) {
	t.Helper()
	t.Logf("%s: scored %d/%d (avg %.0f ticks) | >=1 pass %d/%d | passes %d | reachedShotRange %d/%d | avgMaxBallX %.0f",
		name, br.scored, br.n, br.avgScoreTick, br.withPass, br.n, br.totalPasses, br.reachedShot, br.n, br.avgMaxBallX)
	if os.Getenv("SCENARIO_MEASURE") != "" {
		return // measurement mode: report rates, don't gate
	}
	if minScored > 0 && br.scored < minScored {
		t.Errorf("%s: scored only %d/%d (want >= %d) -- the AI failed to finish the chance", name, br.scored, br.n, minScored)
	}
	if minWithPass > 0 && br.withPass < minWithPass {
		t.Errorf("%s: only %d/%d instances used a pass (want >= %d) -- not a passing move", name, br.withPass, br.n, minWithPass)
	}
	if minReached > 0 && br.reachedShot < minReached {
		t.Errorf("%s: only %d/%d attacks reached shooting range (want >= %d) -- the AI did not progress the ball", name, br.reachedShot, br.n, minReached)
	}
}

const scenarioSeeds = 24

// setup2v1: two SideLeft attackers (carrier central + a free man wide) overload one SideRight
// defender plus a keeper. Good football: carry to commit the defender, then square to the free
// man and finish. The defender can only stop one attacker, so the AI must move the ball.
func setup2v1(m *sim.Match, seed int64) {
	L := sidePlayers(m, sim.SideLeft)  // 2
	R := sidePlayers(m, sim.SideRight) // 2: [keeper, defender]
	yj := jit(seed, 1, 70)
	for _, p := range L {
		p.Role = sim.RoleAttacker
	}
	place(L[0], geom.NewVec(1060, 470+yj), geom.NewVec(1, 0)) // carrier, central
	place(L[1], geom.NewVec(1090, 640+0.4*yj), geom.NewVec(1, 0)) // free man, wide channel
	ballAtFeet(m, L[0])
	R[0].Role = sim.RoleKeeper
	place(R[0], geom.NewVec(1365, 470), geom.NewVec(-1, 0)) // keeper on its line
	R[1].Role = sim.RoleDefender
	place(R[1], geom.NewVec(1205, 520+0.4*yj), geom.NewVec(-1, 0)) // lone defender, goal-side
}

func TestScenario2v1Overload(t *testing.T) {
	br := scenarioBand(2, 2, scenarioSeeds, 360, setup2v1)
	gateScenario(t, "2v1 overload", br, 4, 0, 18) // conservative floors (the human edits physics LIVE, which swings absolute conversion); guards gross AI regressions, not physics drift
}

// setup3v1: three SideLeft attackers overload one SideRight defender plus a keeper. Should
// finish even more reliably than the 2v1.
func setup3v1(m *sim.Match, seed int64) {
	L := sidePlayers(m, sim.SideLeft)  // 3
	R := sidePlayers(m, sim.SideRight) // 2: [keeper, defender]
	yj := jit(seed, 2, 60)
	for _, p := range L {
		p.Role = sim.RoleAttacker
	}
	place(L[0], geom.NewVec(1040, 410+yj), geom.NewVec(1, 0))
	place(L[1], geom.NewVec(1070, 470+0.3*yj), geom.NewVec(1, 0)) // carrier, central
	place(L[2], geom.NewVec(1040, 600+0.5*yj), geom.NewVec(1, 0))
	ballAtFeet(m, L[1])
	R[0].Role = sim.RoleKeeper
	place(R[0], geom.NewVec(1365, 470), geom.NewVec(-1, 0))
	R[1].Role = sim.RoleDefender
	place(R[1], geom.NewVec(1230, 490+0.3*yj), geom.NewVec(-1, 0))
}

func TestScenario3v1Overload(t *testing.T) {
	br := scenarioBand(3, 2, scenarioSeeds, 360, setup3v1)
	gateScenario(t, "3v1 overload", br, 4, 0, 18)
}

// setupFastBreak: SideLeft has just regained the ball deep, with the three SideRight defenders
// caught UPFIELD (behind the ball, in Left's half) after their own attack -- a clear runway to
// goal. Good football: launch the counter (carry/combine) and finish before the chasing defenders
// recover the ~800px they are out of position. Keepers stay home on both sides.
func setupFastBreak(m *sim.Match, seed int64) {
	L := sidePlayers(m, sim.SideLeft)  // 4: [keeper, def, mid, att]
	R := sidePlayers(m, sim.SideRight) // 3: [keeper, def, att]
	yj := jit(seed, 3, 50)
	// Left keeper stays home; the deepest outfielder carries the break with two runners ahead -- a
	// 3-on-2 numerical advantage in transition.
	place(L[0], geom.NewVec(150, 470), geom.NewVec(1, 0))
	L[1].Role = sim.RoleMidfielder
	place(L[1], geom.NewVec(600, 470+yj), geom.NewVec(1, 0)) // ball-carrier, just won it
	L[2].Role = sim.RoleAttacker
	place(L[2], geom.NewVec(820, 380+0.4*yj), geom.NewVec(1, 0)) // runner, ahead-left
	L[3].Role = sim.RoleAttacker
	place(L[3], geom.NewVec(820, 560+0.4*yj), geom.NewVec(1, 0)) // runner, ahead-right
	ballAtFeet(m, L[1])
	// Right keeper home; two defenders caught DEEP in Left's half (behind the ball), recovering ~900px.
	place(R[0], geom.NewVec(1365, 470), geom.NewVec(-1, 0))
	R[1].Role = sim.RoleDefender
	place(R[1], geom.NewVec(430, 380+0.3*yj), geom.NewVec(-1, 0))
	R[2].Role = sim.RoleDefender
	place(R[2], geom.NewVec(430, 560+0.3*yj), geom.NewVec(-1, 0))
}

func TestScenarioFastBreak(t *testing.T) {
	br := scenarioBand(4, 3, scenarioSeeds, 600, setupFastBreak)
	gateScenario(t, "fast break", br, 1, 0, 12) // scored is physics-sensitive on a counter; the stable guard is that the break REACHES a shot
}

// setupGiveAndGo: a SideLeft carrier with a single tight SideRight marker between it and goal,
// and a support attacker offset ahead. Good football: a one-two -- pass and move past the marker
// to receive and finish. No keeper (away size 1), so beating the marker should produce a goal,
// and we additionally require the move involved a pass (not a solo dribble round).
func setupGiveAndGo(m *sim.Match, seed int64) {
	L := sidePlayers(m, sim.SideLeft)  // 2
	R := sidePlayers(m, sim.SideRight) // 1: a lone marker (size 1 has no keeper)
	yj := jit(seed, 4, 60)
	for _, p := range L {
		p.Role = sim.RoleAttacker
	}
	place(L[0], geom.NewVec(780, 470+yj), geom.NewVec(1, 0))      // carrier
	place(L[1], geom.NewVec(940, 560+0.5*yj), geom.NewVec(1, 0))  // support, ahead & wide for the one-two
	ballAtFeet(m, L[0])
	R[0].Role = sim.RoleDefender
	place(R[0], geom.NewVec(840, 470+yj), geom.NewVec(-1, 0)) // marker, tight goal-side of the carrier
}

func TestScenarioGiveAndGo(t *testing.T) {
	br := scenarioBand(2, 1, scenarioSeeds, 480, setupGiveAndGo)
	gateScenario(t, "give-and-go", br, 0, 2, 16) // the one-two beats the lone marker into shooting range and uses a pass
}
