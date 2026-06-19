package sim

import "phootball/internal/config"

// Role identifies a player's position. It drives the AI's BEHAVIOUR -- the keeper plays
// its own logic, and the formation, press selection and zone rules treat the keeper
// specially -- but it does NOT affect a player's stats: every role shares one identical
// tuning profile and always will (see TuningForRole).
type Role int

const (
	RoleKeeper Role = iota
	RoleDefender
	RoleMidfielder
	RoleAttacker
)

// TuningForRole returns the player tuning for a role. All roles -- keeper, defender,
// midfielder, attacker -- share ONE profile and always will, so this returns the same
// tuning regardless of the role. (Match.applyConfig then stamps the match's configured
// tuning over it, so this is only the build-time default.) To change any player value,
// edit config.DefaultPlayerTuning in config/tuning.go -- the single source of truth.
func TuningForRole(_ Role) config.PlayerTuning {
	return config.DefaultPlayerTuning()
}
