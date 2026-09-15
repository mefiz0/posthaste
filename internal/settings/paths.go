// Package settings owns global, non-account configuration and the XDG-derived
// application directory layout. It imports no other internal package.
package settings

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/adrg/xdg"
)

// Paths is the resolved on-disk layout for the application. All paths are
// absolute. It follows the XDG Base Directory specification, so on Linux the
// data directory honours $XDG_DATA_HOME (defaulting to ~/.local/share/posthaste)
// rather than baking in a literal path.
type Paths struct {
	ConfigDir      string
	DataDir        string
	CacheDir       string
	StateDir       string
	LogDir         string
	AccountsDir    string
	AttachmentsDir string
	MessagesDir    string
	BackupsDir     string
}

// ResolvePaths returns the XDG-conventional layout, without creating anything.
func ResolvePaths() Paths {
	dataDir := filepath.Join(xdg.DataHome, appDirName)
	configDir := filepath.Join(xdg.ConfigHome, appDirName)
	cacheDir := filepath.Join(xdg.CacheHome, appDirName)
	stateDir := filepath.Join(xdg.StateHome, appDirName)

	return Paths{
		ConfigDir:      configDir,
		DataDir:        dataDir,
		CacheDir:       cacheDir,
		StateDir:       stateDir,
		LogDir:         filepath.Join(stateDir, "logs"),
		AccountsDir:    filepath.Join(dataDir, "accounts"),
		AttachmentsDir: filepath.Join(dataDir, "attachments"),
		MessagesDir:    filepath.Join(dataDir, "messages"),
		BackupsDir:     filepath.Join(dataDir, "backups"),
	}
}

// EnsureDirs creates every directory in the layout with user-only permissions.
// The layout contains mail and credentials metadata, so 0700 is the floor.
func EnsureDirs(p Paths) error {
	dirs := []string{
		p.ConfigDir,
		p.DataDir,
		p.CacheDir,
		p.StateDir,
		p.LogDir,
		p.AccountsDir,
		p.AttachmentsDir,
		p.MessagesDir,
		p.BackupsDir,
	}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("settings: create %s: %w", dir, err)
		}
	}
	return nil
}

// AccountDatabasePath is the SQLite file for one account. Account isolation is
// structural: one file per account, keyed by the internal account ID.
func (p Paths) AccountDatabasePath(accountID string) string {
	return filepath.Join(p.AccountsDir, accountID+".sqlite")
}

// SettingsFile is the global TOML settings file.
func (p Paths) SettingsFile() string {
	return filepath.Join(p.ConfigDir, "settings.toml")
}
