package menu

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// userConfigVersion is the schema version of the persisted config.json. Bump it only for a
// breaking change that needs migration code (additive fields don't need a bump -- load starts
// from defaults and overlays the file, so an old file simply keeps the new field's default).
const userConfigVersion = 1

// UserConfig is the versioned, persisted user state, written as one config.json under the OS
// user-config dir (Windows %AppData%, macOS ~/Library/Application Support, Linux ~/.config).
// It is forward/back compatible by construction: LoadUserConfig starts from DefaultUserConfig
// and overlays the file, so a file missing a field keeps that field's default and an older
// binary reading a newer file ignores unknown keys. Everything here is plain data (no func
// fields), so it JSON-serializes cleanly -- the same property that lets Tuning cross the wire.
type UserConfig struct {
	Version  int      `json:"version"`
	Prefs    AppPrefs `json:"prefs"`    // camera, zoom, volume, muted
	Settings Settings `json:"settings"` // match setup incl. physics Tuning + per-team control
	Net      netPrefs `json:"net"`      // multiplayer name + recent join addresses
}

// netPrefs is the small persisted multiplayer state: the player's display name and the recently
// used join addresses. The session token is deliberately never persisted (useless after the
// reconnect grace, and a needless on-disk secret).
type netPrefs struct {
	Name        string   `json:"name"`
	LastAddr    string   `json:"last_addr"`
	RecentAddrs []string `json:"recent_addrs"`
}

// DefaultUserConfig is the baseline state used on first run and as the overlay base on load.
func DefaultUserConfig() UserConfig {
	return UserConfig{Version: userConfigVersion, Prefs: DefaultAppPrefs(), Settings: DefaultSettings()}
}

// configDirFn resolves the OS user-config base dir. It is a var so tests can point persistence
// at a temp dir (portable across OSes, unlike relying on $XDG_CONFIG_HOME).
var configDirFn = os.UserConfigDir

// appConfigDir is "<user config dir>/phootball" -- the shared base for config.json, the legacy
// netprefs.json, and the match-records dir.
func appConfigDir() (string, bool) {
	dir, err := configDirFn()
	if err != nil {
		return "", false
	}
	return filepath.Join(dir, "phootball"), true
}

func userConfigPath() (string, bool) {
	dir, ok := appConfigDir()
	if !ok {
		return "", false
	}
	return filepath.Join(dir, "config.json"), true
}

// LoadUserConfig reads the persisted config (best-effort): start from defaults, overlay the
// file, fall back to defaults for an unreadable/corrupt section, and -- on the very first run
// (no config.json yet) -- import the legacy netprefs.json so existing users keep their name and
// recents. Never errors; a missing/unreadable config dir just yields the defaults.
func LoadUserConfig() UserConfig {
	uc := DefaultUserConfig()
	path, ok := userConfigPath()
	if !ok {
		return uc
	}
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &uc) // present keys overlay the defaults; unknown keys ignored
	} else {
		importLegacyNetPrefs(&uc)
	}
	if uc.Settings.Validate() != nil { // a hand-corrupted setup must never reach the sim
		uc.Settings = DefaultSettings()
	}
	uc.Version = userConfigVersion
	return uc
}

// importLegacyNetPrefs pulls name/recents from the old netprefs.json (pre-config.json) so the
// first launch after upgrading doesn't lose them. Best-effort.
func importLegacyNetPrefs(uc *UserConfig) {
	dir, ok := appConfigDir()
	if !ok {
		return
	}
	b, err := os.ReadFile(filepath.Join(dir, "netprefs.json"))
	if err != nil {
		return
	}
	var p netPrefs
	if json.Unmarshal(b, &p) == nil {
		uc.Net = p
	}
}

// saveUserConfig persists the whole envelope from the live app state (best-effort, never per
// frame: called on explicit transitions like Start / Apply / leaving the settings screen, the
// same philosophy as the old saveNetPrefs). An explicitly CLI-passed value that is in play will
// be persisted too (last-used wins); the user can always re-edit it in the menu. A read-only or
// unwritable config dir silently no-ops -- persistence must never crash the game.
func (a *App) saveUserConfig() {
	UserConfig{
		Version:  userConfigVersion,
		Prefs:    a.prefs,
		Settings: a.settings,
		Net:      netPrefs{Name: a.mpName, LastAddr: a.mpAddr, RecentAddrs: a.recentAddrs},
	}.save()
}

// save writes the envelope to config.json (best-effort; an unwritable dir silently no-ops).
func (uc UserConfig) save() {
	path, ok := userConfigPath()
	if !ok {
		return
	}
	if os.MkdirAll(filepath.Dir(path), 0o755) != nil {
		return
	}
	if b, err := json.MarshalIndent(uc, "", "  "); err == nil {
		_ = os.WriteFile(path, b, 0o644)
	}
}
