package control_test

// An authoritative out-of-sample check: the COMMITTED gate metric (measureKicks) run over a
// configurable, DISJOINT seed band. The sweep harness (TestSweepSpec) reports a different metric
// (classifyMatch) that can diverge from the committed measureKicks gate, so this tool validates a
// tuning change with the SAME metric the gate uses, on seeds the gate does not cover. Manual:
//
//	DISJOINT_SEEDS=31-90 go test ./internal/control/ -run TestDisjointGate -count=1 -v
//
// It reads the live defaultAITuning (so patch a default, run this, compare paired at the same build
// moment -- the human edits the physics live, so always measure baseline vs candidate back to back).
import (
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"

	"phootball/internal/config"
	"phootball/internal/control"
)

func TestDisjointGate(t *testing.T) {
	spec := os.Getenv("DISJOINT_SEEDS")
	if spec == "" {
		t.Skip("set DISJOINT_SEEDS=lo-hi (e.g. 31-90) to run this manual out-of-sample gate check")
	}
	parts := strings.SplitN(spec, "-", 2)
	lo, _ := strconv.Atoi(parts[0])
	hi := lo
	if len(parts) == 2 {
		hi, _ = strconv.Atoi(parts[1])
	}
	if hi < lo {
		t.Fatalf("bad DISJOINT_SEEDS %q", spec)
	}
	n := hi - lo + 1
	results := make([]kickOutcome, n)
	workers := runtime.GOMAXPROCS(0)
	if workers > 12 {
		workers = 12
	}
	if workers < 1 {
		workers = 1
	}
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	for i := lo; i <= hi; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			m, ais := aiMatch(6, int64(i), control.SkillHard, func(c *config.Config) { c.Geometry = config.LargeGeometry() })
			results[i-lo] = measureKicks(m, ais, 60*120)
		}(i)
	}
	wg.Wait()
	var agg kickOutcome
	for _, k := range results {
		agg.passes += k.passes
		agg.passDone += k.passDone
		agg.shots += k.shots
		agg.onTarget += k.onTarget
		agg.scored += k.scored
		agg.clears += k.clears
	}
	t.Logf("DISJOINT measureKicks seeds %d-%d (%d games): %.1f%% | passes=%d shots=%d onTarget=%d scored=%d clears=%d",
		lo, hi, n, agg.passPct(), agg.passes, agg.shots, agg.onTarget, agg.scored, agg.clears)
}
