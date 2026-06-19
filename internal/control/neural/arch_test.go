package neural_test

import (
	"go/build"
	"strings"
	"testing"
)

// TestNeuralImportsArchGuard pins the anti-cheat / determinism import boundary: the neural
// controller may reach the game only through the public sim View/Intent types (and policy,
// geom, control for the shared helpers). It must NOT import physics internals, netcode, the
// renderer, or any cgo/BLAS/ONNX runtime — so it cannot reach hidden state or break headless
// determinism.
func TestNeuralImportsArchGuard(t *testing.T) {
	pkg, err := build.ImportDir(".", 0)
	if err != nil {
		t.Fatalf("ImportDir: %v", err)
	}
	banned := []string{"physics", "netcode", "render", "ebiten", "gonum", "onnx", "tensorflow"}
	for _, imp := range pkg.Imports {
		if imp == "C" {
			t.Errorf("neural must not use cgo")
		}
		for _, b := range banned {
			if strings.Contains(imp, b) {
				t.Errorf("neural must not import %q (contains %q)", imp, b)
			}
		}
	}
}
