// Command watch is a live viewer for tiki-taka training: it follows the training log to learn the
// current curriculum stage, sets up that exact drill (collect / carry / shoot / rondo / build-up /
// defend / full game) with the CURRENT-BEST policy, and renders it so you can watch the net practise
// what it is training on right now. It reloads the weights whenever the trainer ships a new best,
// switches drills when the stage changes, and shows a live HUD (stage, possession %, score, whether
// training is still running). Read-only: it never touches training, it just visualizes it.
package main

import (
	_ "phootball/internal/x11quiet"

	"flag"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/ebitenutil"

	"phootball/internal/config"
	"phootball/internal/control"
	"phootball/internal/control/neural"
	"phootball/internal/eval"
	"phootball/internal/geom"
	"phootball/internal/policy"
	"phootball/internal/render"
	"phootball/internal/scenario"
	"phootball/internal/sim"
)

// stageSpec maps a curriculum stage name to the drill the viewer should show (mirrors curriculum.py).
type stageSpec struct {
	label      string
	kind       int
	home, away int
	field      string
	oppMode    string // "keeper" | "presser" | "self" | "none"
	episode    int    // ticks before re-spawning the drill
}

var stageSpecs = map[string]stageSpec{
	"motor":      {"MOTOR — shoot 1v1", scenario.KindShooting, 1, 1, "large", "keeper", 600},
	"collect":    {"COLLECT — settle a rolling ball", scenario.KindCollect, 1, 0, "large", "none", 600},
	"firsttouch": {"FIRST TOUCH — 2v0 exchange", scenario.KindRondo, 2, 0, "large", "none", 700},
	"hold":       {"HOLD — shield 1v1", scenario.KindRondo, 1, 1, "large", "presser", 700},
	"carry":      {"CARRY — dribble past a defender", scenario.KindCarry, 1, 1, "large", "presser", 800},
	"rondo3v1":   {"RONDO 3v1 — keep-away", scenario.KindRondo, 3, 1, "large", "presser", 800},
	"rondo4v2":   {"RONDO 4v2 — keep-away", scenario.KindRondo, 4, 2, "large", "presser", 900},
	"buildup":    {"BUILD-UP 4v3 (self-play)", scenario.KindBuildup, 4, 3, "large", "self", 1000},
	"possession": {"POSSESSION 6v5 (self-play)", scenario.KindBuildup, 6, 5, "large", "self", 1100},
	"defense":    {"DEFENSE 6v6 (self-play)", scenario.KindDefend, 6, 6, "large", "self", 1100},
	"fullgame":   {"FULL GAME 6v6 (self-play)", scenario.KindKickoff, 6, 6, "large", "self", 1600},
	"sharpen":    {"SHARPEN 6v6 (self-play)", scenario.KindKickoff, 6, 6, "large", "self", 1600},
}

var (
	stageRe = regexp.MustCompile(`STAGE \d+ '([a-z0-9]+)'`)
	bestRe  = regexp.MustCompile(`NEW BEST score=([0-9.]+)`)
)

func lastMatch(re *regexp.Regexp, s string) string {
	last := ""
	for _, m := range re.FindAllStringSubmatch(s, -1) {
		last = m[1]
	}
	return last
}

func fieldGeom(name string) config.Geometry {
	switch name {
	case "small":
		return config.SmallGeometry()
	case "large":
		return config.LargeGeometry()
	default:
		return config.StandardGeometry()
	}
}

type viewer struct {
	weightsPath, logPath string
	net                  *policy.Net
	wMtime               int64
	cam                  *render.Camera

	match              eval.Match
	spec               stageSpec
	stageName          string
	bestScore          string
	logFresh           bool
	step               int
	seed               int64
	frame              int
	prevL, prevR       int     // last seen scores (reset the drill on a goal)
	posL, posR, posTot float64 // running radius-possession tally this episode
}

// readLog returns (stageName, bestScore, trainingLive).
func (v *viewer) readLog() (string, string, bool) {
	b, err := os.ReadFile(v.logPath)
	if err != nil {
		return "", "", false
	}
	live := false
	if st, e := os.Stat(v.logPath); e == nil {
		live = time.Since(st.ModTime()) < 45*time.Second // log written recently => training still running
	}
	s := string(b)
	return lastMatch(stageRe, s), lastMatch(bestRe, s), live
}

// loadNet (re)loads the weights file when it changes; keeps the last good net on any error (e.g. a
// partial write or an arch mismatch), so the viewer never crashes mid-training.
func (v *viewer) loadNet() bool {
	st, err := os.Stat(v.weightsPath)
	if err != nil {
		if v.net == nil {
			if n, e := policy.LoadDefault(); e == nil {
				v.net = n
				return true
			}
		}
		return false
	}
	if st.ModTime().UnixNano() == v.wMtime && v.net != nil {
		return false
	}
	f, err := os.Open(v.weightsPath)
	if err != nil {
		return false
	}
	defer f.Close()
	n, err := policy.Load(f)
	if err != nil || neural.ValidateNet(n) != nil {
		return false // partial write or arch mismatch: keep the last good net
	}
	v.net, v.wMtime = n, st.ModTime().UnixNano()
	return true
}

// build (re)creates the match for the current stage with the current-best net.
func (v *viewer) build() {
	name, best, live := v.readLog()
	v.bestScore, v.logFresh = best, live
	if name == "" {
		name = "fullgame"
	}
	spec, ok := stageSpecs[name]
	if !ok {
		spec = stageSpecs["fullgame"]
	}
	v.spec, v.stageName = spec, name
	v.seed++
	ctrl := sim.SideLeft
	mutate := func(cfg *config.Config) { cfg.Geometry = fieldGeom(spec.field) }
	v.match = eval.BuildSizedWith(spec.home, spec.away, v.seed, mutate, func(id int, side sim.Side) control.Controller {
		if side == ctrl {
			return neural.New(id, v.net)
		}
		switch spec.oppMode {
		case "keeper":
			return scenario.NewActor(id, scenario.ScriptKeeper)
		case "presser":
			return scenario.NewActor(id, scenario.ScriptPresser)
		case "none":
			return scenario.NewActor(id, scenario.ScriptIdle)
		default:
			return neural.New(id, v.net) // self-play opponent
		}
	})
	scenario.Arrange(v.match.M, spec.kind, ctrl, v.seed)
	v.step = 0
	v.posL, v.posR, v.posTot = 0, 0, 0
	v.prevL, v.prevR = v.match.M.Teams[0].Score, v.match.M.Teams[1].Score
}

// tallyPossession adds this tick to the running radius-model possession counters.
func (v *viewer) tallyPossession() {
	m := v.match.M
	ball := m.Ball.Position
	bR := m.Ball.Radius()
	lIn, rIn := false, false
	for _, p := range m.Players {
		if geom.Dist(p.Position, ball)-p.Radius()-bR <= p.Tuning.PullRange {
			if p.Team.Side == sim.SideLeft {
				lIn = true
			} else {
				rIn = true
			}
		}
	}
	v.posTot++
	if lIn && !rIn {
		v.posL++
	} else if rIn && !lIn {
		v.posR++
	}
}

func (v *viewer) Update() error {
	v.frame++
	if v.frame%30 == 0 { // ~2x/s: follow shipped weights + stage changes
		reloaded := v.loadNet()
		name, best, live := v.readLog()
		v.bestScore, v.logFresh = best, live
		if (name != "" && name != v.stageName) || reloaded {
			v.build()
		}
	}
	if v.net == nil {
		v.loadNet()
		return nil
	}
	if v.match.M == nil {
		v.build()
	}
	v.match.Step()
	v.tallyPossession()
	v.step++
	// Re-spawn the drill on a goal (clean restart) or after the episode length.
	if v.match.M.Teams[0].Score != v.prevL || v.match.M.Teams[1].Score != v.prevR || v.step >= v.spec.episode {
		v.build()
	}
	return nil
}

func (v *viewer) Draw(screen *ebiten.Image) {
	if v.match.M == nil {
		ebitenutil.DebugPrint(screen, "watch: waiting for a training checkpoint...")
		return
	}
	render.Frame(screen, v.match.M, v.cam, 1.0/60.0)
	poss := 0.0
	if v.posTot > 0 {
		poss = 100 * v.posL / v.posTot
	}
	status := "training LIVE"
	if !v.logFresh {
		status = "training idle/stopped (showing last best)"
	}
	best := v.bestScore
	if best == "" {
		best = "—"
	}
	l := fmt.Sprintf("%s   |   Blue (net) possession %.0f%%   |   score %d–%d",
		v.spec.label, poss, v.match.M.Teams[0].Score, v.match.M.Teams[1].Score)
	ebitenutil.DebugPrintAt(screen, l, 8, 8)
	ebitenutil.DebugPrintAt(screen, fmt.Sprintf("Blue = the training net  ·  best tiki-taka score %s  ·  %s", best, status), 8, 24)
}

func (v *viewer) Layout(w, h int) (int, int) {
	s := ebiten.DeviceScaleFactor()
	if s <= 0 {
		s = 1
	}
	return int(float64(w) * s), int(float64(h) * s)
}

func main() {
	weights := flag.String("weights", "training/checkpoints/latest_best.bin", "weights file to render (the current best)")
	logPath := flag.String("log", "training/tikitaka.log", "training log to follow for the current stage")
	flag.Parse()
	v := &viewer{weightsPath: strings.TrimSpace(*weights), logPath: strings.TrimSpace(*logPath), cam: render.NewCamera()}
	v.loadNet()
	v.build()
	ebiten.SetWindowSize(1280, 860)
	ebiten.SetWindowResizingMode(ebiten.WindowResizingModeEnabled)
	ebiten.SetWindowTitle("phootball — training viewer")
	if err := ebiten.RunGame(v); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
