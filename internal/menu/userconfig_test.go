package menu

import (
	"os"
	"path/filepath"
	"testing"
)

// useTempConfigDir points persistence at a temp dir for the duration of a test (portable across
// OSes, unlike relying on $XDG_CONFIG_HOME).
func useTempConfigDir(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	old := configDirFn
	configDirFn = func() (string, error) { return dir, nil }
	t.Cleanup(func() { configDirFn = old })
}

func phootballDir(t *testing.T) string {
	t.Helper()
	d, ok := appConfigDir()
	if !ok {
		t.Fatal("appConfigDir() not resolvable")
	}
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	return d
}

// TestUserConfigRoundTrip: saved tuning + prefs + net come back exactly on reload.
func TestUserConfigRoundTrip(t *testing.T) {
	useTempConfigDir(t)
	uc := DefaultUserConfig()
	uc.Prefs.Volume, uc.Prefs.CameraMode = 0.33, "player"
	uc.Settings.Tuning.Player.MaxSpeed = 222
	uc.Settings.Tuning.Player.Shoot.Front = 999
	uc.Settings.Tuning.Possession.BuildSeconds = 2.5
	uc.Net.Name, uc.Net.RecentAddrs = "Tester", []string{"1.2.3.4:47600"}
	uc.save()

	got := LoadUserConfig()
	if got.Prefs.Volume != 0.33 || got.Prefs.CameraMode != "player" {
		t.Errorf("prefs not persisted: %+v", got.Prefs)
	}
	if got.Settings.Tuning.Player.MaxSpeed != 222 || got.Settings.Tuning.Player.Shoot.Front != 999 {
		t.Errorf("player tuning not persisted: %+v", got.Settings.Tuning.Player)
	}
	if got.Settings.Tuning.Possession.BuildSeconds != 2.5 {
		t.Errorf("possession tuning not persisted: %v", got.Settings.Tuning.Possession.BuildSeconds)
	}
	if got.Net.Name != "Tester" || len(got.Net.RecentAddrs) != 1 {
		t.Errorf("net not persisted: %+v", got.Net)
	}
}

// TestUserConfigDefaultsOverlay: a file with only one field set loads with everything else at
// its default -- the forward-compatibility property (an old file missing newer fields).
func TestUserConfigDefaultsOverlay(t *testing.T) {
	useTempConfigDir(t)
	os.WriteFile(filepath.Join(phootballDir(t), "config.json"), []byte(`{"prefs":{"Volume":0.3}}`), 0o644)
	got := LoadUserConfig()
	if got.Prefs.Volume != 0.3 {
		t.Errorf("overlay did not apply: Volume = %v, want 0.3", got.Prefs.Volume)
	}
	if got.Settings.Tuning.Player.MaxSpeed != 140 || got.Prefs.CameraMode != DefaultAppPrefs().CameraMode {
		t.Errorf("absent fields should keep defaults: MaxSpeed=%v Camera=%q", got.Settings.Tuning.Player.MaxSpeed, got.Prefs.CameraMode)
	}
}

// TestUserConfigCorruptYieldsDefaults: garbage on disk never crashes and falls back to defaults.
func TestUserConfigCorruptYieldsDefaults(t *testing.T) {
	useTempConfigDir(t)
	os.WriteFile(filepath.Join(phootballDir(t), "config.json"), []byte("}{ not json"), 0o644)
	got := LoadUserConfig()
	if got.Settings.Tuning.Player.MaxSpeed != 140 {
		t.Errorf("corrupt file should yield defaults, got MaxSpeed=%v", got.Settings.Tuning.Player.MaxSpeed)
	}
}

// TestUserConfigInvalidValuesResetSettings: valid JSON with a divide-by-zero value (mass 0) is
// rejected by Validate and the settings fall back to default (never reaches the sim).
func TestUserConfigInvalidValuesResetSettings(t *testing.T) {
	useTempConfigDir(t)
	os.WriteFile(filepath.Join(phootballDir(t), "config.json"),
		[]byte(`{"settings":{"Tuning":{"Player":{"Mass":0}}}}`), 0o644)
	got := LoadUserConfig()
	if got.Settings.Tuning.Player.Mass != DefaultSettings().Tuning.Player.Mass {
		t.Errorf("invalid (mass 0) settings should reset to default, got Mass=%v", got.Settings.Tuning.Player.Mass)
	}
}

// TestUserConfigLegacyMigration: on first run (no config.json) the legacy netprefs.json is
// imported so existing users keep their name/recents.
func TestUserConfigLegacyMigration(t *testing.T) {
	useTempConfigDir(t)
	os.WriteFile(filepath.Join(phootballDir(t), "netprefs.json"),
		[]byte(`{"name":"Old","last_addr":"9.9.9.9:1","recent_addrs":["9.9.9.9:1"]}`), 0o644)
	got := LoadUserConfig()
	if got.Net.Name != "Old" || got.Net.LastAddr != "9.9.9.9:1" || len(got.Net.RecentAddrs) != 1 {
		t.Errorf("legacy netprefs not imported: %+v", got.Net)
	}
}
