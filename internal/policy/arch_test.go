package policy

import (
	"go/build"
	"strings"
	"testing"
)

// TestNoGameOrCgoImports pins the architecture boundary: internal/policy must stay game-
// agnostic and cgo/BLAS/ONNX/Ebiten-free, so it is safe to embed in the headless server and
// preserves cross-platform determinism. This is the import-level half of the anti-cheat /
// headless guard.
func TestNoGameOrCgoImports(t *testing.T) {
	pkg, err := build.ImportDir(".", 0)
	if err != nil {
		t.Fatalf("ImportDir: %v", err)
	}
	banned := []string{"gonum", "onnx", "ebiten", "tensorflow", "cgo"}
	for _, imp := range pkg.Imports {
		if strings.HasPrefix(imp, "phootball/") {
			t.Errorf("policy must not import a game package: %q", imp)
		}
		if imp == "C" {
			t.Errorf("policy must not use cgo")
		}
		for _, b := range banned {
			if strings.Contains(imp, b) {
				t.Errorf("policy must not import %q (contains %q)", imp, b)
			}
		}
	}
}
