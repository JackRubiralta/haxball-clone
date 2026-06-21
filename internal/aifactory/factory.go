// Package aifactory builds the right Controller for a difficulty tier. It exists to break the
// control <-> control/neural import cycle: control cannot import neural (neural imports control),
// so the per-tier branch that may construct a neural controller lives here, in a small package
// that imports both. Every controller-construction site calls aifactory.New instead of
// control.NewAISkill directly.
package aifactory

import (
	"fmt"
	"os"
	"sync"

	"phootball/internal/control"
	"phootball/internal/control/neural"
	"phootball/internal/policy"
)

var (
	netOnce     sync.Once
	sharedN     *policy.Net
	netErr      error
	weightsPath string // override source for the neural weights ("" = embedded)
)

// SetWeightsPath overrides the embedded neural-tier weights with a file on disk, so a player can
// face a training checkpoint (e.g. training/checkpoints/latest_best.bin) live. It must be called
// BEFORE the first neural controller is built (the net loads once, lazily). The PHBALL_NEURAL_WEIGHTS
// environment variable is an equivalent fallback for paths that don't parse CLI flags.
func SetWeightsPath(path string) { weightsPath = path }

// sharedNet lazily loads and validates the neural-tier weights exactly once: from the override path
// (SetWeightsPath / PHBALL_NEURAL_WEIGHTS) if set, else the embedded net. It is a read-only
// *policy.Net shared across every neural controller (each owns its Workspace, so sharing is
// race-free for the sequential per-tick Intent calls).
func sharedNet() (*policy.Net, error) {
	netOnce.Do(func() {
		path := weightsPath
		if path == "" {
			path = os.Getenv("PHBALL_NEURAL_WEIGHTS")
		}
		if path != "" {
			var f *os.File
			if f, netErr = os.Open(path); netErr == nil {
				defer f.Close()
				sharedN, netErr = policy.Load(f)
			}
		} else {
			sharedN, netErr = policy.LoadDefault()
		}
		if netErr == nil {
			netErr = neural.ValidateNet(sharedN)
		}
	})
	return sharedN, netErr
}

// New returns a Controller for the given player at the given skill tier: a neural controller for
// SkillNeural, otherwise the rule-based AI. The returned control.Controller also satisfies
// netcode.Bot (identical method set), so it is a drop-in at both local and server sites.
//
// If the neural weights fail to load/validate (a build-time invariant, since the weights are
// embedded and generated to match), it panics with a clear message rather than silently
// shipping a different controller.
func New(id int, skill control.Skill) control.Controller {
	if skill == control.SkillNeural {
		net, err := sharedNet()
		if err != nil {
			panic(fmt.Sprintf("aifactory: neural tier unavailable: %v", err))
		}
		return neural.New(id, net)
	}
	return control.NewAISkill(id, skill)
}
