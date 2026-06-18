package control_test

import (
	"testing"

	"phootball/internal/config"
	"phootball/internal/control"
)

// TestTmpSweep is a throwaway robustness probe (30 seeds) -- DELETE before finalizing.
func TestTmpSweep(t *testing.T) {
	var agg kickOutcome
	const seeds = 30
	for seed := int64(1); seed <= seeds; seed++ {
		m, ais := aiMatch(6, seed, control.SkillHard, func(c *config.Config) {
			c.Geometry = config.LargeGeometry()
		})
		k := measureKicks(m, ais, 60*120)
		agg.passes += k.passes
		agg.passDone += k.passDone
		agg.shots += k.shots
		agg.onTarget += k.onTarget
		agg.scored += k.scored
	}
	t.Logf("30-seed 6v6 HARD: passes=%d reached=%d (%.1f%%) | shots=%d onTarget=%d scored=%d",
		agg.passes, agg.passDone, 100*float64(agg.passDone)/float64(agg.passes), agg.shots, agg.onTarget, agg.scored)
}
