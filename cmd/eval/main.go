// Command eval scores a neural-controller weights file against the rule-based AI (and itself)
// across a seed x size x field grid, printing an aggregate JSON report. It is the autonomous
// pipeline's behavioral gate: each trained/exported checkpoint is graded here. It is pure-Go and
// reuses the parity-guaranteed inference (internal/policy) and the match recorder, so the metrics
// are exactly the shipped controller's.
//
// Matches are independent (shared read-only net, per-controller workspaces), so the grid runs on
// a worker pool across cores. Always aggregate: single matches are chaotic and often goalless by
// design, so win rates and rates are reported over the whole grid, with the NN side alternated by
// seed parity to cancel any kickoff-side advantage.
package main

import (
	"encoding/json"
	"flag"
	"log"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"

	"phootball/internal/config"
	"phootball/internal/control"
	"phootball/internal/control/neural"
	"phootball/internal/eval"
	"phootball/internal/policy"
	"phootball/internal/sim"
)

func main() {
	log.SetFlags(0)
	var (
		weights   = flag.String("weights", "", "weights file (empty = embedded default)")
		sizesSpec = flag.String("sizes", "2,3,4", "comma-separated team sizes")
		fieldSpec = flag.String("fields", "medium,large", "comma-separated field presets")
		oppSpec   = flag.String("opponents", "easy,normal,hard,impossible,nn", "opponent tiers")
		seeds     = flag.Int("seeds", 30, "seeds per (opponent,size,field) cell")
		ticks     = flag.Int("ticks", 60*90, "ticks per match")
		offside   = flag.Bool("offside", false, "enable offside")
		par       = flag.Int("par", runtime.NumCPU(), "parallel matches")
		pretty    = flag.Bool("pretty", false, "indent the JSON report")
	)
	flag.Parse()

	net, err := loadNet(*weights)
	if err != nil {
		log.Fatalf("eval: load weights: %v", err)
	}
	if err := neural.ValidateNet(net); err != nil {
		log.Fatalf("eval: %v", err)
	}

	sizes := parseInts(*sizesSpec)
	fields := strings.Split(*fieldSpec, ",")
	opps := strings.Split(*oppSpec, ",")

	type job struct {
		opp   string
		size  int
		field string
		seed  int64
	}
	var jobs []job
	for _, opp := range opps {
		opp = strings.TrimSpace(opp)
		for _, size := range sizes {
			for _, field := range fields {
				for s := int64(0); s < int64(*seeds); s++ {
					jobs = append(jobs, job{opp, size, strings.TrimSpace(field), s})
				}
			}
		}
	}

	results := make([]result, len(jobs))
	var wg sync.WaitGroup
	ch := make(chan int, len(jobs))
	for i := range jobs {
		ch <- i
	}
	close(ch)
	n := *par
	if n < 1 {
		n = 1
	}
	for w := 0; w < n; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range ch {
				j := jobs[i]
				results[i] = playOne(net, j.opp, j.size, j.field, j.seed, *ticks, *offside)
			}
		}()
	}
	wg.Wait()

	report := Report{
		Weights:   weightsLabel(*weights),
		Seeds:     *seeds,
		Sizes:     sizes,
		Fields:    fields,
		Opponents: map[string]*Agg{},
	}
	for i := range jobs {
		opp := jobs[i].opp
		agg := report.Opponents[opp]
		if agg == nil {
			agg = &Agg{Opponent: opp}
			report.Opponents[opp] = agg
		}
		agg.add(results[i])
	}
	for _, a := range report.Opponents {
		a.finalize()
	}

	enc := json.NewEncoder(os.Stdout)
	if *pretty {
		enc.SetIndent("", "  ")
	}
	if err := enc.Encode(report); err != nil {
		log.Fatal(err)
	}
}

// result is one match's outcome from the NN side's perspective.
type result struct {
	valid                   bool
	outcome                 int // +1 nn win, 0 draw, -1 loss
	gf, ga                  int
	turnovers               int
	poss, passComp, shotOT  float64
	hasPoss, hasComp, hasOT bool
}

type Report struct {
	Weights   string          `json:"weights"`
	Seeds     int             `json:"seeds_per_cell"`
	Sizes     []int           `json:"sizes"`
	Fields    []string        `json:"fields"`
	Opponents map[string]*Agg `json:"opponents"`
}

type Agg struct {
	Opponent string `json:"opponent"`
	Matches  int    `json:"matches"`
	NNWins   int    `json:"nn_wins"`
	OppWins  int    `json:"opp_wins"`
	Draws    int    `json:"draws"`

	WinRate         float64 `json:"win_rate"`
	PossessionPct   float64 `json:"possession_pct"`
	PassCompletion  float64 `json:"pass_completion"`
	ShotsOnTarget   float64 `json:"shots_on_target_ratio"`
	GoalsForAvg     float64 `json:"goals_for_avg"`
	GoalsAgainstAvg float64 `json:"goals_against_avg"`
	TurnoversAvg    float64 `json:"turnovers_avg"`

	sumPoss, sumComp, sumOT float64
	cntPoss, cntComp, cntOT int
	sumGF, sumGA, sumTurn   float64
}

func (a *Agg) add(r result) {
	if !r.valid {
		return
	}
	a.Matches++
	a.sumGF += float64(r.gf)
	a.sumGA += float64(r.ga)
	a.sumTurn += float64(r.turnovers)
	switch {
	case r.outcome > 0:
		a.NNWins++
	case r.outcome < 0:
		a.OppWins++
	default:
		a.Draws++
	}
	if r.hasPoss {
		a.sumPoss += r.poss
		a.cntPoss++
	}
	if r.hasComp {
		a.sumComp += r.passComp
		a.cntComp++
	}
	if r.hasOT {
		a.sumOT += r.shotOT
		a.cntOT++
	}
}

func (a *Agg) finalize() {
	if a.Matches > 0 {
		a.WinRate = float64(a.NNWins) / float64(a.Matches)
		a.GoalsForAvg = a.sumGF / float64(a.Matches)
		a.GoalsAgainstAvg = a.sumGA / float64(a.Matches)
		a.TurnoversAvg = a.sumTurn / float64(a.Matches)
	}
	if a.cntPoss > 0 {
		a.PossessionPct = a.sumPoss / float64(a.cntPoss)
	}
	if a.cntComp > 0 {
		a.PassCompletion = a.sumComp / float64(a.cntComp)
	}
	if a.cntOT > 0 {
		a.ShotsOnTarget = a.sumOT / float64(a.cntOT)
	}
}

func playOne(net *policy.Net, opp string, size int, field string, seed int64, ticks int, offside bool) result {
	geom, ok := config.PresetByName(field)
	if !ok {
		log.Fatalf("eval: unknown field %q", field)
	}
	mutate := func(cfg *config.Config) {
		cfg.Geometry = geom
		cfg.Ruleset.OffsideEnabled = offside
		if offside && cfg.Ruleset.OffsideFrac == 0 {
			cfg.Ruleset.OffsideFrac = 0.5
		}
	}

	nnSide := sim.SideLeft
	if seed%2 == 1 {
		nnSide = sim.SideRight
	}
	nnIsNN := opp == "nn" || opp == "neural"
	var oppSkill control.Skill
	if !nnIsNN {
		s, ok := control.SkillFromString(opp)
		if !ok {
			log.Fatalf("eval: unknown opponent %q", opp)
		}
		oppSkill = s
	}

	factory := func(id int, side sim.Side) control.Controller {
		if side == nnSide || nnIsNN {
			return neural.New(id, net)
		}
		return control.NewAISkill(id, oppSkill)
	}
	m := eval.BuildWith(size, seed, mutate, factory)
	m.M.EnableRecording()

	lastSide := sim.SideNone
	lastCarrier := -1
	turnovers := 0
	m.Run(ticks, func(int) {
		c := m.M.BallCarrier()
		if c == nil {
			return
		}
		if c.PlayerID != lastCarrier {
			if lastSide == nnSide && c.Team.Side != nnSide {
				turnovers++
			}
			lastSide = c.Team.Side
			lastCarrier = c.PlayerID
		}
	})

	st := m.M.Stats()
	nn, op := teamStat(st, nnSide), teamStat(st, nnSide.Opponent())
	if nn == nil || op == nil {
		return result{}
	}
	r := result{valid: true, gf: nn.Goals, ga: op.Goals, turnovers: turnovers}
	switch {
	case nn.Goals > op.Goals:
		r.outcome = 1
	case nn.Goals < op.Goals:
		r.outcome = -1
	}
	if tot := nn.PossessionSeconds + op.PossessionSeconds; tot > 0 {
		r.poss = nn.PossessionSeconds / tot
		r.hasPoss = true
	}
	if nn.Passes > 0 {
		r.passComp = float64(nn.PassesCompleted) / float64(nn.Passes)
		r.hasComp = true
	}
	if nn.Shots > 0 {
		r.shotOT = float64(nn.ShotsOnTarget) / float64(nn.Shots)
		r.hasOT = true
	}
	return r
}

func teamStat(st sim.MatchStats, side sim.Side) *sim.TeamStat {
	for i := range st.Teams {
		if st.Teams[i].Side == side {
			return &st.Teams[i]
		}
	}
	return nil
}

func loadNet(path string) (*policy.Net, error) {
	if path == "" {
		return policy.LoadDefault()
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return policy.Load(f)
}

func weightsLabel(path string) string {
	if path == "" {
		return "embedded:" + policy.DefaultWeightsName
	}
	return path
}

func parseInts(spec string) []int {
	var out []int
	for _, p := range strings.Split(spec, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		v, err := strconv.Atoi(p)
		if err != nil {
			log.Fatalf("eval: bad int %q: %v", p, err)
		}
		out = append(out, v)
	}
	if len(out) == 0 {
		log.Fatal("eval: no sizes")
	}
	sort.Ints(out)
	return out
}
