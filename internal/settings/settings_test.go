package settings

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadMissingFileReturnsDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.toml")

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.NotificationsEnabled {
		t.Error("notifications should default to enabled")
	}
	if cfg.MinimizeToTray {
		t.Error("tray should default to off")
	}
	if cfg.CrashReportingEnabled {
		t.Error("crash reporting must default to off")
	}
	if cfg.AttachmentEagerThresholdBytes != DefaultAttachmentEagerThresholdBytes {
		t.Errorf("threshold = %d, want %d", cfg.AttachmentEagerThresholdBytes, DefaultAttachmentEagerThresholdBytes)
	}
	if cfg.Keymap == nil {
		t.Error("keymap should be initialised")
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "settings.toml")
	want := Default()
	want.MinimizeToTray = true
	want.VerboseLogging = true
	want.DefaultAccountID = "acct-1"
	want.AttachmentEagerThresholdBytes = 8 << 20
	want.Keymap = map[string]string{"archive": "e", "delete": "#"}

	if err := Save(path, want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.NotificationsEnabled != want.NotificationsEnabled ||
		got.MinimizeToTray != want.MinimizeToTray ||
		got.VerboseLogging != want.VerboseLogging ||
		got.DefaultAccountID != want.DefaultAccountID ||
		got.AttachmentEagerThresholdBytes != want.AttachmentEagerThresholdBytes {
		t.Errorf("round-trip mismatch:\n got %+v\nwant %+v", got, want)
	}
	if got.Keymap["archive"] != "e" || got.Keymap["delete"] != "#" {
		t.Errorf("keymap not preserved: %+v", got.Keymap)
	}
}

func TestLoadPartialFileFillsDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.toml")
	if err := os.WriteFile(path, []byte("minimize_to_tray = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.MinimizeToTray {
		t.Error("explicit value not read")
	}
	if !cfg.NotificationsEnabled {
		t.Error("absent field should fall back to default")
	}
	if cfg.AttachmentEagerThresholdBytes != DefaultAttachmentEagerThresholdBytes {
		t.Error("absent numeric field should fall back to default")
	}
}

func TestLoadRejectsMalformedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.toml")
	if err := os.WriteFile(path, []byte("this is not = = toml"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected parse error")
	}
}
