package app

import (
	"context"

	"github.com/mefiz0/posthaste/internal/settings"
)

// SettingsService is the bound surface for the global settings file. Saving
// refreshes the manager's snapshot, notifies the shell (tray, logging), and
// pushes the settings-changed event to the frontend.
type SettingsService struct {
	manager *Manager
}

// NewSettingsService returns the settings service bound to the manager.
func NewSettingsService(manager *Manager) *SettingsService {
	return &SettingsService{manager: manager}
}

// GetSettings returns the current settings snapshot.
func (s *SettingsService) GetSettings(ctx context.Context) (AppSettingsInfo, error) {
	return settingsToInfo(s.manager.CurrentSettings()), nil
}

// SaveSettings validates and persists the settings snapshot.
func (s *SettingsService) SaveSettings(ctx context.Context, info AppSettingsInfo) error {
	return s.manager.SaveSettings(settingsFromInfo(s.manager.CurrentSettings(), info))
}

// settingsToInfo maps the engine settings onto the bridge shape. The fields
// the bridge does not own (crash reporting opt-in, tray prompt state, the
// default account) stay engine-side.
func settingsToInfo(cfg settings.Settings) AppSettingsInfo {
	keymap := make(map[string]string, len(cfg.Keymap))
	for action, chord := range cfg.Keymap {
		keymap[action] = chord
	}
	return AppSettingsInfo{
		NotificationsEnabled:          cfg.NotificationsEnabled,
		MinimizeToTray:                cfg.MinimizeToTray,
		VerboseLogging:                cfg.VerboseLogging,
		AttachmentEagerThresholdBytes: cfg.AttachmentEagerThresholdBytes,
		Keymap:                        keymap,
	}
}

// settingsFromInfo merges a bridge settings snapshot over the engine's,
// preserving the engine-only fields the bridge never sees.
func settingsFromInfo(current settings.Settings, info AppSettingsInfo) settings.Settings {
	cfg := current
	cfg.NotificationsEnabled = info.NotificationsEnabled
	cfg.MinimizeToTray = info.MinimizeToTray
	cfg.VerboseLogging = info.VerboseLogging
	if info.AttachmentEagerThresholdBytes > 0 {
		cfg.AttachmentEagerThresholdBytes = info.AttachmentEagerThresholdBytes
	}
	keymap := make(map[string]string, len(info.Keymap))
	for action, chord := range info.Keymap {
		keymap[action] = chord
	}
	cfg.Keymap = keymap
	return cfg
}
