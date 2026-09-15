package settings

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

const appDirName = "posthaste"

// DefaultAttachmentEagerThresholdBytes is the initial eager-fetch cut-off for
// attachments. Small attachments are fetched during sync; larger ones are
// fetched on demand. Two mebibytes covers the common case without bloating the
// local store.
const DefaultAttachmentEagerThresholdBytes int64 = 2 << 20

// Settings is the global, non-account configuration. It must be readable before
// any account exists, so it lives in its own TOML file rather than in a
// per-account database.
type Settings struct {
	NotificationsEnabled          bool              `toml:"notifications_enabled"`
	MinimizeToTray                bool              `toml:"minimize_to_tray"`
	CloseToTrayPrompted           bool              `toml:"close_to_tray_prompted"`
	CrashReportingEnabled         bool              `toml:"crash_reporting_enabled"`
	VerboseLogging                bool              `toml:"verbose_logging"`
	AttachmentEagerThresholdBytes int64             `toml:"attachment_eager_threshold_bytes"`
	DefaultAccountID              string            `toml:"default_account_id"`
	Keymap                        map[string]string `toml:"keymap"`
}

// Default returns the documented defaults: notifications on, tray off, crash
// reporting off (opt-in only), minimal logging.
func Default() Settings {
	return Settings{
		NotificationsEnabled:          true,
		MinimizeToTray:                false,
		CrashReportingEnabled:         false,
		VerboseLogging:                false,
		AttachmentEagerThresholdBytes: DefaultAttachmentEagerThresholdBytes,
		Keymap:                        map[string]string{},
	}
}

// Load reads settings from path. A missing file is not an error — it yields the
// defaults, so a first run never fails. Missing fields in an existing file are
// filled from the defaults, which is how the "migration" for this flat file
// works (new keys get sensible values automatically).
func Load(path string) (Settings, error) {
	cfg := Default()

	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return cfg, nil
		}
		return Settings{}, fmt.Errorf("settings: read %s: %w", path, err)
	}

	if err := toml.Unmarshal(data, &cfg); err != nil {
		return Settings{}, fmt.Errorf("settings: parse %s: %w", path, err)
	}
	if cfg.AttachmentEagerThresholdBytes < 0 {
		cfg.AttachmentEagerThresholdBytes = DefaultAttachmentEagerThresholdBytes
	}
	if cfg.Keymap == nil {
		cfg.Keymap = map[string]string{}
	}
	return cfg, nil
}

// Save writes settings atomically so a crash mid-write cannot leave a truncated
// config behind.
func Save(path string, cfg Settings) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("settings: create config dir: %w", err)
	}

	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(cfg); err != nil {
		return fmt.Errorf("settings: encode: %w", err)
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), ".settings-*.toml")
	if err != nil {
		return fmt.Errorf("settings: create temp: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	if _, err := tmp.Write(buf.Bytes()); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("settings: write temp: %w", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("settings: chmod temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("settings: close temp: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("settings: rename into place: %w", err)
	}
	return nil
}
