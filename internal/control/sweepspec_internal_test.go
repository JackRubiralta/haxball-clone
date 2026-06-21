package control

import (
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"

	"phootball/internal/config"
)

// This file is the parameterized sweep harness that the ultracode tuning workflow fans out over:
// each parallel agent runs `SWEEP_SPEC="key=val,key=val" SWEEP_MODEL=both SWEEP_SEEDS=val \
// go test ./internal/control/ -run TestSweepSpec -count=1 -v` and parses the one-line RESULT.
// It applies a set of aiTuning overrides symmetrically to BOTH teams (so the completion metric
// stays meaningful) and reports the full diagnosis -- completion, volume, shots, scored,
// shotEnded/mistake (the north-star), turnovers, hold-time, and directional speed-efficiency --
// for one or both move models, over a selectable DISJOINT seed band (so a win can be
// adversarially re-validated on seeds it was not tuned on).

// applyLever sets one sweepable aiTuning field by name. Hand-written (not reflection) because the
// aiTuning fields are unexported and reflect cannot set them. Extend as new levers are added.
func applyLever(t *aiTuning, key string, val float64) bool {
	switch key {
	// Shooting.
	case "shootRange":
		t.shootRange = val
	case "dribbleCommitRange":
		t.dribbleCommitRange = val
	case "tapRange":
		t.tapRange = val
	case "fullRange":
		t.fullRange = val
	case "minShootCharge":
		t.minShootCharge = val
	case "shootOpenBonus":
		t.shootOpenBonus = val
	// Passing / retention.
	case "passContestMargin":
		t.passContestMargin = val
	case "passSafetyMin":
		t.passSafetyMin = val
	case "passReceiverSpace":
		t.passReceiverSpace = val
	case "passReleadGap":
		t.passReleadGap = val
	case "passArriveSpeed":
		t.passArriveSpeed = val
	case "receiveControlFrac":
		t.receiveControlFrac = val
	case "receiveOntoMax":
		t.receiveOntoMax = val
	case "receiveSlowRadius":
		t.receiveSlowRadius = val
	case "actPressure":
		t.actPressure = val
	case "recycleFreely":
		t.recycleFreely = val != 0
	case "supportRangeFrac":
		t.supportRangeFrac = val
	case "supportForwardBias":
		t.supportForwardBias = val
	case "passSettleSpeed":
		t.passSettleSpeed = val
	case "passSettleWeight":
		t.passSettleWeight = val
	case "passAssistBonus":
		t.passAssistBonus = val
	case "assistEdge":
		t.assistEdge = val
	// Trap energy.
	case "trapStealMinEnergy":
		t.trapStealMinEnergy = val
	case "trapHoldMinEnergy":
		t.trapHoldMinEnergy = val
	// Directional facing.
	case "faceActionGap":
		t.faceActionGap = val
	case "faceLeadMargin":
		t.faceLeadMargin = val
	case "faceMoveThrottle":
		t.faceMoveThrottle = val
	case "faceReleaseBand":
		t.faceReleaseBand = val
	// Keeper.
	case "keeperSaveSpeed":
		t.keeperSaveSpeed = val
	case "keeperSweepMargin":
		t.keeperSweepMargin = val
	case "keeperSweepBox":
		t.keeperSweepBox = val
	case "keeperDepthMax":
		t.keeperDepthMax = val
	case "keeperChallengeRange":
		t.keeperChallengeRange = val
	default:
		return false
	}
	return true
}

// parseSpec turns "shootRange=440,passContestMargin=0.45" into a mutateTune that applies them.
func parseSpec(spec string) (func(*aiTuning), []string, error) {
	spec = strings.TrimSpace(spec)
	var applied []string
	pairs := []struct {
		k string
		v float64
	}{}
	if spec != "" {
		for _, part := range strings.Split(spec, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			kv := strings.SplitN(part, "=", 2)
			if len(kv) != 2 {
				return nil, nil, fmt.Errorf("bad spec part %q (want key=value)", part)
			}
			v, err := strconv.ParseFloat(strings.TrimSpace(kv[1]), 64)
			if err != nil {
				return nil, nil, fmt.Errorf("bad value in %q: %v", part, err)
			}
			pairs = append(pairs, struct {
				k string
				v float64
			}{strings.TrimSpace(kv[0]), v})
			applied = append(applied, part)
		}
	}
	mut := func(t *aiTuning) {
		for _, p := range pairs {
			applyLever(t, p.k, p.v)
		}
	}
	return mut, applied, nil
}

// sweepSeeds returns a named, DISJOINT seed band. "val"=101-130 (the committed validation band),
// "gate"=1-30 (the gate seeds -- only for confirming, never for tuning), "adv"=201-230 and
// "adv2"=301-330 (independent bands for adversarial re-validation), "big"=101-150 (50-seed sign-off).
func sweepSeeds(name string) []int64 {
	start, n := int64(101), int64(30)
	switch name {
	case "gate":
		start, n = 1, 30
	case "adv":
		start = 201
	case "adv2":
		start = 301
	case "big":
		n = 50
	}
	seeds := make([]int64, 0, n)
	for s := start; s < start+n; s++ {
		seeds = append(seeds, s)
	}
	return seeds
}

func sweepRun(seeds []int64, mm config.MoveModel, mut func(*aiTuning)) diagStats {
	results := make([]diagResult, len(seeds))
	workers := runtime.GOMAXPROCS(0)
	if workers > 12 {
		workers = 12
	}
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	for i, seed := range seeds {
		wg.Add(1)
		go func(i int, seed int64) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			m, ais := sweepMatch(6, seed, SkillHard, func(c *config.Config) {
				c.Geometry = config.LargeGeometry()
				c.Tuning.MoveModel = mm
			}, mut)
			results[i] = classifyMatch(m, ais, diagTicks, seed, nil)
		}(i, seed)
	}
	wg.Wait()
	return aggregate(results)
}

// TestSweepSpec is env-driven and never fails (it is a measurement harness, not a gate). It prints
// one RESULT line per move model that a workflow agent parses.
func TestSweepSpec(t *testing.T) {
	mut, applied, err := parseSpec(os.Getenv("SWEEP_SPEC"))
	if err != nil {
		t.Fatalf("SWEEP_SPEC: %v", err)
	}
	band := os.Getenv("SWEEP_SEEDS")
	if band == "" {
		band = "val"
	}
	seeds := sweepSeeds(band)
	models := []struct {
		name string
		m    config.MoveModel
	}{}
	switch os.Getenv("SWEEP_MODEL") {
	case "standard":
		models = append(models, struct {
			name string
			m    config.MoveModel
		}{"standard", config.MoveStandard})
	case "directional":
		models = append(models, struct {
			name string
			m    config.MoveModel
		}{"directional", config.MoveDirectional})
	case "both":
		models = append(models,
			struct {
				name string
				m    config.MoveModel
			}{"standard", config.MoveStandard},
			struct {
				name string
				m    config.MoveModel
			}{"directional", config.MoveDirectional})
	default: // directional only -- the project's chosen direction (Jack: "test on directional only")
		models = append(models, struct {
			name string
			m    config.MoveModel
		}{"directional", config.MoveDirectional})
	}
	fmt.Printf("SPEC=[%s] seeds=%s(%d)\n", strings.Join(applied, ","), band, len(seeds))
	for _, mm := range models {
		s := sweepRun(seeds, mm.m, mut)
		fmt.Printf("RESULT model=%-11s comp=%.1f vol=%.1f shots=%.1f scored=%.1f shotEnded=%.1f mistake=%.1f TO=%.1f ownTO=%.1f clears=%.1f hold5s=%.2f speedEff=%.3f\n",
			mm.name, s.pctMean, s.passesMean, s.shotsMean, s.scoredMean, 100*s.shotEndedFrac, 100*s.mistakeFrac,
			s.turnoversMean, s.ownHalfTurnoversMean, s.clearsMean, s.longHoldsPerGame, s.speedEff)
	}
}
