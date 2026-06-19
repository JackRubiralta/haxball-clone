package neural

import "phootball/internal/policy"

// FeatureMeta is the Go-owned description of the feature/action contract, serialized to
// dataset_meta.json so the Python trainer builds a model that matches exactly (eliminating
// Go/Python skew). Go is the single source of truth: Python only ever consumes float32 arrays
// it reads from shards, never recomputes features.
type FeatureMeta struct {
	Version         int      `json:"version"`
	FeatureDim      int      `json:"feature_dim"` // FlatDim of the datagen/parity flat vector
	SelfDim         int      `json:"self_dim"`
	BallDim         int      `json:"ball_dim"`
	GlobalDim       int      `json:"global_dim"`
	EntDim          int      `json:"ent_dim"`
	MaxTeammates    int      `json:"max_teammates"`
	MaxOpponents    int      `json:"max_opponents"`
	HeadNames       []string `json:"head_names"`
	HeadSizes       []int    `json:"head_sizes"`
	AimArcMax       float64  `json:"aim_arc_max"`
	ShootChargeNorm float64  `json:"shoot_charge_norm"`
	WeightsVersion  int      `json:"weights_format_version"`
	WeightsArchID   int      `json:"weights_arch_id"`
}

// Meta returns the current feature/action contract.
func Meta() FeatureMeta {
	return FeatureMeta{
		Version:         1,
		FeatureDim:      FlatDim,
		SelfDim:         SelfDim,
		BallDim:         BallDim,
		GlobalDim:       GlobalDim,
		EntDim:          EntDim,
		MaxTeammates:    MaxTeammates,
		MaxOpponents:    MaxOpponents,
		HeadNames:       []string{"move_dir", "throttle", "aim_bin", "ability", "cancel"},
		HeadSizes:       HeadSizes(),
		AimArcMax:       AimArcMax,
		ShootChargeNorm: shootChargeNorm,
		WeightsVersion:  policy.FormatVersion,
		WeightsArchID:   policy.ArchDeepSetsV1,
	}
}
