// Command teachercheck validates the scripted "AI algo" teachers (internal/scenario) BEFORE they are
// used to bootstrap the neural net. For each drill it drives the LEARNER side with the teacher,
// runs many seeded episodes, and reports the drill's key telemetry (possession %, passes/min) as an
// IQM with a bootstrap 95% CI -- compared against an idle baseline. A teacher must clear its drill
// objective by a margin (and beat the idle baseline) or it is not fit to teach. This is the
// "test the teacher before you distill it" gate (research: GRF baselines; rliable IQM/CIs).
//
// Run from the repo root:  go run ./cmd/teachercheck
package main

import (
	"flag"
	"fmt"
	"math"
	"math/rand"
	"os"
	"sort"

	"phootball/internal/config"
	"phootball/internal/control"
	"phootball/internal/eval"
	"phootball/internal/geom"
	"phootball/internal/scenario"
	"phootball/internal/sim"
)

// minPassLen is the world-unit floor below which a same-team handover is a micro-poke, not a pass
// (mirrors the env reward's passMinLen so teachercheck measures what training will count).
const minPassLen = 80.0

type drill struct {
	name        string
	kind        int
	home, away  int
	teacher     scenario.ScriptKind
	oppPresser  bool    // opponent side runs a presser (else idle)
	gradePoss   bool    // grade possession (false for unopposed drills where idle trivially scores 1.0)
	wantPoss    float64 // possession_pct the teacher's IQM must clear
	wantPassPM  float64 // passes/min the teacher's IQM must clear (0 = not graded)
	episodeTcks int
}

var drills = []drill{
	{"collect", scenario.KindCollect, 1, 0, scenario.ScriptCollector, false, true, 0.60, 0, 1200},
	{"carry", scenario.KindCarry, 1, 1, scenario.ScriptCarrier, true, true, 0.50, 0, 1400},
	{"firsttouch", scenario.KindRondo, 2, 0, scenario.ScriptTikitaka, false, false, 0.0, 6.0, 1400},
	{"rondo3v1", scenario.KindRondo, 3, 1, scenario.ScriptTikitaka, true, true, 0.50, 8.0, 1600},
}

// episode runs one drill episode with the given learner-side controller kind and returns
// (possession_pct, passes_per_min).
func episode(d drill, learnerKind scenario.ScriptKind, seed int64) (float64, float64) {
	ctrl := sim.SideLeft
	mutate := func(cfg *config.Config) { cfg.Geometry = config.LargeGeometry() }
	em := eval.BuildSizedWith(d.home, d.away, seed, mutate, func(id int, side sim.Side) control.Controller {
		if side == ctrl {
			return scenario.NewActor(id, learnerKind)
		}
		if d.oppPresser {
			return scenario.NewActor(id, scenario.ScriptPresser)
		}
		return scenario.NewActor(id, scenario.ScriptIdle)
	})
	scenario.Arrange(em.M, d.kind, ctrl, seed)

	m := em.M
	var posLearner, posOpp float64 // contested/loose ticks excluded (radius model, as in the env tracker)
	passes := 0
	lastCtrl := -1               // learner player id last in firm control of the ball
	var lastCtrlBallPos geom.Vec // ball position when lastCtrl last had it
	prevL, prevR := teamScores(m, ctrl)

	for t := 0; t < d.episodeTcks; t++ {
		em.Step()
		// Re-arrange on a stray goal (drills are not about scoring), mirroring the env's noScore path.
		if l, r := teamScores(m, ctrl); l != prevL || r != prevR {
			if !m.Celebrating() {
				scenario.Arrange(m, d.kind, ctrl, seed)
				prevL, prevR = teamScores(m, ctrl)
			}
		}
		// Radius-model possession + pass detection.
		bp := m.Ball.Position
		bR := m.Ball.Radius()
		lIn, rIn := false, false
		nearestLearner, nearestGap, nearestPull := -1, math.MaxFloat64, 0.0
		for _, p := range m.Players {
			g := geom.Dist(p.Position, bp) - p.Radius() - bR
			if g <= p.Tuning.PullRange {
				if p.Team.Side == ctrl {
					lIn = true
				} else {
					rIn = true
				}
			}
			if p.Team.Side == ctrl && g < nearestGap {
				nearestGap, nearestLearner, nearestPull = g, p.PlayerID, p.Tuning.PullRange
			}
		}
		if lIn && !rIn {
			posLearner++
		} else if rIn && !lIn {
			posOpp++
		}
		// A learner is in control when it is the closest player and within its pull reach.
		if nearestLearner >= 0 && nearestGap <= nearestPull {
			if lastCtrl >= 0 && nearestLearner != lastCtrl {
				if geom.Dist(bp, lastCtrlBallPos) >= minPassLen { // a real, length-gated pass
					passes++
				}
			}
			lastCtrl, lastCtrlBallPos = nearestLearner, bp
		}
	}
	poss := 0.0
	if posLearner+posOpp > 0 {
		poss = posLearner / (posLearner + posOpp)
	}
	minutes := float64(d.episodeTcks) / 3600.0
	return poss, float64(passes) / minutes
}

func teamScores(m *sim.Match, ctrl sim.Side) (l, r int) {
	for _, t := range m.Teams {
		if t.Side == ctrl {
			l = t.Score
		} else {
			r = t.Score
		}
	}
	return
}

// iqm returns the interquartile mean: the mean of the middle 50% of the sorted values (robust to
// outlier seeds -- the rliable recommendation for chaotic RL metrics).
func iqm(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	lo, hi := len(s)/4, len(s)-len(s)/4
	if hi <= lo {
		lo, hi = 0, len(s)
	}
	sum := 0.0
	for _, v := range s[lo:hi] {
		sum += v
	}
	return sum / float64(hi-lo)
}

// bootstrapCI returns the 95% bootstrap confidence interval of the IQM over the samples.
func bootstrapCI(xs []float64, rng *rand.Rand) (lo, hi float64) {
	const B = 2000
	stats := make([]float64, B)
	n := len(xs)
	for b := 0; b < B; b++ {
		res := make([]float64, n)
		for i := range res {
			res[i] = xs[rng.Intn(n)]
		}
		stats[b] = iqm(res)
	}
	sort.Float64s(stats)
	return stats[int(0.025*B)], stats[int(0.975*B)]
}

func main() {
	seeds := flag.Int("seeds", 40, "episodes (seeds) per drill (>=30 for a stable IQM)")
	flag.Parse()
	rng := rand.New(rand.NewSource(1))

	fmt.Printf("teachercheck: validating scripted teachers over %d seeds/drill (IQM + 95%% bootstrap CI)\n\n", *seeds)
	allPass := true
	for _, d := range drills {
		var tPoss, tPass, bPoss, bPass []float64
		for s := 0; s < *seeds; s++ {
			seed := int64(1000 + s)
			p, pm := episode(d, d.teacher, seed)
			tPoss, tPass = append(tPoss, p), append(tPass, pm)
			bp, bpm := episode(d, scenario.ScriptIdle, seed) // idle baseline
			bPoss, bPass = append(bPoss, bp), append(bPass, bpm)
		}
		tpi, bpi := iqm(tPoss), iqm(bPoss)
		tpci0, tpci1 := bootstrapCI(tPoss, rng)
		ok := true
		if d.gradePoss {
			okPoss := tpi >= d.wantPoss && tpi > bpi
			ok = okPoss
			fmt.Printf("  %-11s possession IQM=%.2f [%.2f,%.2f]  (need >=%.2f, baseline %.2f)  %s\n",
				d.name, tpi, tpci0, tpci1, d.wantPoss, bpi, passFail(okPoss))
		} else {
			fmt.Printf("  %-11s possession IQM=%.2f [%.2f,%.2f]  (not graded -- unopposed)\n",
				d.name, tpi, tpci0, tpci1)
		}
		if d.wantPassPM > 0 {
			tmi, bmi := iqm(tPass), iqm(bPass)
			tmci0, tmci1 := bootstrapCI(tPass, rng)
			okPass := tmi >= d.wantPassPM && tmi > bmi
			ok = ok && okPass
			fmt.Printf("  %-11s pass/min   IQM=%.1f [%.1f,%.1f]  (need >=%.1f, baseline %.1f)  %s\n",
				d.name, tmi, tmci0, tmci1, d.wantPassPM, bmi, passFail(okPass))
		}
		if !ok {
			allPass = false
		}
		fmt.Println()
	}
	if !allPass {
		fmt.Println("teachercheck: FAIL -- a teacher did not clear its drill objective; fix it before training on it.")
		os.Exit(1)
	}
	fmt.Println("teachercheck: PASS -- all teachers clear their drill objectives and beat the idle baseline.")
}

func passFail(ok bool) string {
	if ok {
		return "PASS"
	}
	return "FAIL"
}
