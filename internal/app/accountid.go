package app

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/mefiz0/posthaste/internal/store"
)

// maxSafeAccountID is the largest integer JavaScript can represent exactly
// (Number.MAX_SAFE_INTEGER). Account IDs cross the Wails bridge as JSON
// numbers, so anything larger comes back as a different value.
const maxSafeAccountID int64 = 1<<53 - 1

// migrateAccountIDs rewrites every account whose stored ID is too large for the
// frontend to represent. Early builds drew a full 63-bit random ID; those
// persist in accounts.json and round-trip through JavaScript rounded, so every
// per-account binding call failed with "account ... is not running" while the
// account still appeared in the sidebar.
func (m *Manager) migrateAccountIDs() {
	for _, entry := range m.registry.List() {
		if entry.ID <= maxSafeAccountID {
			continue
		}
		if err := m.rekeyAccount(entry); err != nil {
			m.logger.Error("app: migrate account id", "account", entry.ID, "err", err)
		}
	}
}

// rekeyAccount moves one account's database file and registry entry from its
// unsafe ID to a fresh safe one. The registry is rewritten last, so a partial
// failure cannot leave it pointing at state that never moved.
//
// The keyring reference stored in the account row is deliberately left alone:
// it is an opaque string, so it does not need to be JavaScript-safe, and
// keeping it avoids a keyring write during startup and keeps authentication
// working even when the secret store is temporarily unavailable.
func (m *Manager) rekeyAccount(entry RegistryEntry) error {
	newID, err := newAccountID()
	if err != nil {
		return err
	}
	ctx := context.Background()
	credentialRef := credentialRefFor(entry.ID)

	oldBase := m.deps.Paths.AccountDatabasePath(accountIDString(entry.ID))
	newBase := m.deps.Paths.AccountDatabasePath(accountIDString(newID))
	if err := renameDatabaseFiles(oldBase, newBase); err != nil {
		return err
	}

	// Rekey the stored account row so its ID follows the registry. A database
	// that does not exist yet has nothing to update.
	if _, err := os.Stat(newBase); err == nil {
		st, err := store.Open(ctx, newBase, store.Options{
			BackupDir: m.deps.Paths.BackupsDir,
			Logger:    m.logger,
		})
		if err != nil {
			return err
		}
		rekeyErr := st.RekeyAccount(ctx, accountIDString(newID), credentialRef)
		_ = st.Close()
		if rekeyErr != nil {
			return rekeyErr
		}
	}

	if err := m.registry.Update(entry.ID, func(e *RegistryEntry) { e.ID = newID }); err != nil {
		return err
	}
	m.logger.Info("app: migrated account id to a JavaScript-safe value", "account", entry.ID)
	return nil
}

// renameDatabaseFiles moves a SQLite file and its WAL/SHM sidecars, ignoring
// the ones that do not exist.
func renameDatabaseFiles(oldBase, newBase string) error {
	for _, suffix := range []string{"", "-wal", "-shm"} {
		oldPath := oldBase + suffix
		if _, err := os.Stat(oldPath); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return fmt.Errorf("app: stat account database: %w", err)
		}
		if err := os.Rename(oldPath, newBase+suffix); err != nil {
			return fmt.Errorf("app: rename account database: %w", err)
		}
	}
	return nil
}
