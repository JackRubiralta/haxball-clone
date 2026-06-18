package sim

import (
	"flag"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"phootball/internal/config"
	"phootball/internal/geom"
)

// updateGolden regenerates the golden replay trace instead of asserting against it.
// Run: go test ./internal/sim -run TestGoldenReplay -update
var updateGolden = flag.Bool("update", false, "regenerate golden replay trace files")

// TestGoldenReplay is the FEEL-FREEZE safety net for the whole overhaul. It drives
// fixed-seed matches with fully deterministic, AI-independent input scripts and asserts
// the recorded trace (ball position/velocity sampled every N ticks, the running score,
// and every goal's attribution) is byte-identical to a committed golden file.
//
// The scripts are intentionally NOT the AI: the AI's observable behaviour changes in later
// phases (the human-capability de-power), but the SIMULATION feel must not. So these traces
// depend only on internal/sim + internal/physics + internal/geom -- the frozen layers.
// ANY diff here means a behaviour-preserving refactor wasn't: stop and revert.
//
// Two scenarios run together for coverage:
//   - "swarm": a 4v4 chase that pins, bumps, traps, pushes, and walls the ball -- exercises
//     integration, the soft speed cap, friction, the whole possession subsystem, every
//     Collide site, the dribble interaction, shoot()/push(), and ConfineBall/ConfinePlayer.
//   - "solo": one uncontested player that reliably dribbles and shoots the ball in --
//     exercises CheckGoal's goal-line threshold, resolveGoal attribution, and the kickoff
//     reset, which the congested swarm never reaches.
func TestGoldenReplay(t *testing.T) {
	cfg := config.Default()

	t.Run("swarm", func(t *testing.T) {
		field := NewFieldFromGeometry(cfg.Geometry)
		m := BuildMatchFromConfigSized(field, 4, 4, cfg)
		assertGolden(t, "replay_default.golden", runReplay(m, 3600, 30, swarmInputs))
	})

	t.Run("solo", func(t *testing.T) {
		field := NewFieldFromGeometry(cfg.Geometry)
		m := BuildSolo(field)
		// Deterministic initial conditions: put the lone player just behind a ball that
		// sits a short, clean run from the right goal mouth (on the centre axis) so a
		// drive-and-shoot reliably crosses the line. The post-goal kickoff reset returns
		// both to their home/centre spots, exercising that path too.
		m.Players[0].Position = geom.NewVec(field.Max.X-180, field.CenterSpot.Y)
		m.Players[0].Facing = geom.NewVec(1, 0)
		m.Ball.Position = geom.NewVec(field.Max.X-150, field.CenterSpot.Y)
		got := runReplay(m, 1200, 30, soloInputs)
		// The solo scenario exists to cover scoring; if it ever stops scoring the trace is
		// worthless as a goal/kickoff/attribution characterization.
		if strings.Contains(got, "\ngoals=0\n") {
			t.Fatalf("solo replay scored no goals; the scoring path is no longer exercised")
		}
		assertGolden(t, "replay_solo.golden", got)
	})
}

// runReplay steps a match for `ticks` with the given input script, sampling the ball state
// every `sampleN` ticks, and returns the full deterministic trace as a string.
func runReplay(m *Match, ticks, sampleN int, script func(*Match, uint64) map[int]Intent) string {
	dt := 1.0 / 60.0
	var b strings.Builder
	b.WriteString("# phootball golden replay trace -- DO NOT hand-edit; regenerate with -update\n")
	b.WriteString("seed=" + strconv.FormatInt(m.Seed, 10))
	b.WriteString(" ticks=" + strconv.Itoa(ticks))
	b.WriteString(" players=" + strconv.Itoa(len(m.Players)))
	b.WriteString(" field=" + ftoa(m.Field.Width()) + "x" + ftoa(m.Field.Height()) + "\n")

	for tick := 0; tick < ticks; tick++ {
		m.Step(script(m, uint64(tick)), dt)
		if tick%sampleN == 0 {
			b.WriteString(sampleLine(m, tick))
		}
	}
	b.WriteString("final " + sampleLine(m, ticks))
	b.WriteString("goals=" + strconv.Itoa(len(m.Goals)) + "\n")
	for i, g := range m.Goals {
		b.WriteString("goal[" + strconv.Itoa(i) + "] " + goalLine(g))
	}
	return b.String()
}

// assertGolden compares a trace against testdata/<name>, or regenerates it under -update.
func assertGolden(t *testing.T, name, got string) {
	t.Helper()
	golden := filepath.Join("testdata", name)
	if *updateGolden {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatalf("mkdir testdata: %v", err)
		}
		if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		t.Logf("regenerated %s (%d bytes)", golden, len(got))
		return
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("read golden (run with -update to create it): %v", err)
	}
	if got != string(want) {
		t.Errorf("golden replay diff -- the simulation feel changed.\n%s", firstDiff(string(want), got))
	}
}

// swarmInputs is a deterministic, AI-free controller for the 4v4 scenario: when far from
// the ball a player chases it (with a per-player angular wobble so they don't perfectly
// stack); when on the ball it dribbles toward the opponent goal and shoots on a hold/release
// cadence (a sustained hold never fires -- the kick is a release edge -- so the cadence is
// what makes it shoot). It reads only public match state and uses no randomness, so the
// resulting trajectory is fully reproducible.
func swarmInputs(m *Match, tick uint64) map[int]Intent {
	in := make(map[int]Intent, len(m.Players))
	ball := m.Ball.Position
	for _, p := range m.Players {
		var it Intent
		it.Throttle = 1

		goalX := m.Field.Max.X
		if p.Team.Side == SideRight {
			goalX = m.Field.Min.X
		}
		goal := geom.NewVec(goalX, m.Field.CenterSpot.Y)
		it.Aim = goal.Sub(p.Position)

		toBall := ball.Sub(p.Position)
		key := tick + uint64(p.PlayerID)
		if geom.Norm(toBall) < 26 {
			// On the ball: drive it goalward and shoot on a 40-tick hold/release cadence.
			it.Move = geom.Unit(goal.Sub(p.Position))
			it.ShootHeld = key%40 < 26
			if key%163 < 6 {
				it.Trap = true // occasional trap to settle the ball
				it.ShootHeld = false
			}
		} else {
			// Off the ball: chase it with a per-player wobble.
			ang := float64(p.PlayerID)*0.7 + float64(tick)*0.013
			wobble := geom.NewVec(math.Cos(ang), math.Sin(ang)).Scale(0.35)
			it.Move = geom.Unit(toBall).Add(wobble)
		}
		if key%211 == 0 {
			it.Push = true // periodic middle-click jab
		}
		in[p.PlayerID] = it
	}
	return in
}

// soloInputs drives the single uncontested player to drive the ball into the right-hand
// goal with the middle-click push (an instant radial jab along the player->ball line). The
// player steers to the goal-opposite side of the ball so a jab always shoves it goalward;
// a jab that whiffs out of range is harmless. With no opponents the ball reliably crosses
// the line, so the trace exercises CheckGoal, resolveGoal attribution, and the kickoff
// reset (ball to centre, player home) -- which the congested swarm never reaches.
func soloInputs(m *Match, tick uint64) map[int]Intent {
	in := make(map[int]Intent, len(m.Players))
	ball := m.Ball.Position
	goal := geom.NewVec(m.Field.Max.X, m.Field.CenterSpot.Y)
	for _, p := range m.Players {
		var it Intent
		it.Throttle = 1
		it.Aim = goal.Sub(p.Position)
		// Aim for a spot just behind the ball on the side away from the goal, so when the
		// player arrives its push line (player->ball) points at the goal.
		behind := ball.Sub(geom.Unit(goal.Sub(ball)).Scale(22))
		it.Move = geom.Unit(behind.Sub(p.Position))
		it.Push = true // jab every tick; only a connect within reach launches the ball
		in[p.PlayerID] = it
	}
	return in
}

func sampleLine(m *Match, tick int) string {
	return "t=" + strconv.Itoa(tick) +
		" bp=(" + ftoa(m.Ball.Position.X) + "," + ftoa(m.Ball.Position.Y) + ")" +
		" bv=(" + ftoa(m.Ball.Velocity.X) + "," + ftoa(m.Ball.Velocity.Y) + ")" +
		" score=" + strconv.Itoa(m.Teams[0].Score) + "-" + strconv.Itoa(m.Teams[1].Score) + "\n"
}

func goalLine(g ScoreEvent) string {
	return "team=" + strconv.Itoa(int(g.Team)) +
		" scorer=" + strconv.Itoa(g.Scorer) +
		" hasScorer=" + strconv.FormatBool(g.HasScorer) +
		" assist=" + strconv.Itoa(g.Assist) +
		" hasAssist=" + strconv.FormatBool(g.HasAssist) +
		" own=" + strconv.FormatBool(g.OwnGoal) +
		" deflected=" + strconv.FormatBool(g.Deflected) +
		" tick=" + strconv.FormatUint(g.Tick, 10) + "\n"
}

// ftoa formats a float to its shortest exact decimal so the golden round-trips bit-for-bit
// on the same architecture and any change in the low bits surfaces as a diff.
func ftoa(f float64) string { return strconv.FormatFloat(f, 'g', -1, 64) }

// firstDiff returns a short context window around the first differing line, to keep a
// failure readable instead of dumping the whole multi-KB trace.
func firstDiff(want, got string) string {
	wl, gl := strings.Split(want, "\n"), strings.Split(got, "\n")
	n := len(wl)
	if len(gl) < n {
		n = len(gl)
	}
	for i := 0; i < n; i++ {
		if wl[i] != gl[i] {
			lo := i - 2
			if lo < 0 {
				lo = 0
			}
			var sb strings.Builder
			sb.WriteString("first diff at line " + strconv.Itoa(i+1) + ":\n")
			for j := lo; j <= i; j++ {
				sb.WriteString("  want: " + wl[j] + "\n")
			}
			sb.WriteString("  got:  " + gl[i] + "\n")
			return sb.String()
		}
	}
	if len(wl) != len(gl) {
		return "traces differ in length: want " + strconv.Itoa(len(wl)) + " lines, got " + strconv.Itoa(len(gl))
	}
	return "traces differ but no line-level diff found"
}
