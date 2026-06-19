package policy

import "embed"

// weightsFS holds the embedded trained weight files. At least one weights/*.bin must exist at
// build time (bootstrapped by cmd/gen-weights for M0; overwritten by training/export.py).
//
//go:embed weights/*.bin
var weightsFS embed.FS

// DefaultWeightsName is the shipped neural-tier net.
const DefaultWeightsName = "neural_v1.bin"

// LoadEmbedded loads a weight file embedded under weights/.
func LoadEmbedded(name string) (*Net, error) {
	b, err := weightsFS.ReadFile("weights/" + name)
	if err != nil {
		return nil, err
	}
	return LoadBytes(b)
}

// LoadDefault loads the shipped neural-tier net.
func LoadDefault() (*Net, error) { return LoadEmbedded(DefaultWeightsName) }
