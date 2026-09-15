package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/mefiz0/posthaste/internal/settings"
	"github.com/mefiz0/posthaste/internal/store"
)

func TestNewAccountIDIsJavaScriptSafe(t *testing.T) {
	for i := 0; i < 1000; i++ {
		id, err := newAccountID()
		if err != nil {
			t.Fatalf("newAccountID: %v", err)
		}
		if id <= 0 || id > maxSafeAccountID {
			t.Fatalf("newAccountID = %d, want 0 < id <= %d", id, maxSafeAccountID)
		}
	}
}

// TestManagerMigratesUnsafeAccountID simulates an account.json written by an
// old build whose random account ID exceeds JavaScript's safe integer range,
// and checks that the manager moves the database, credential, store row, and
// registry entry to a safe ID.
func TestManagerMigratesUnsafeAccountID(t *testing.T) {
	base := t.TempDir()
	paths := settings.Paths{
		ConfigDir:      filepath.Join(base, "config"),
		DataDir:        filepath.Join(base, "data"),
		CacheDir:       filepath.Join(base, "cache"),
		StateDir:       filepath.Join(base, "state"),
		LogDir:         filepath.Join(base, "state", "logs"),
		AccountsDir:    filepath.Join(base, "data", "accounts"),
		AttachmentsDir: filepath.Join(base, "data", "attachments"),
		MessagesDir:    filepath.Join(base, "data", "messages"),
		BackupsDir:     filepath.Join(base, "data", "backups"),
	}
	for _, dir := range []string{paths.ConfigDir, paths.AccountsDir, paths.AttachmentsDir, paths.MessagesDir, paths.BackupsDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
	}

	oldID := maxSafeAccountID + 4096
	entry := RegistryEntry{
		ID:          oldID,
		Ordinal:     1,
		Email:       "me@here.test",
		DisplayName: "me",
		Color:       "#5f8f5f",
	}
	data, err := json.MarshalIndent([]RegistryEntry{entry}, "", "  ")
	if err != nil {
		t.Fatalf("marshal registry: %v", err)
	}
	if err := os.WriteFile(filepath.Join(paths.ConfigDir, "accounts.json"), data, 0o600); err != nil {
		t.Fatalf("write registry: %v", err)
	}

	ctx := context.Background()
	oldPath := paths.AccountDatabasePath(accountIDString(oldID))
	st, err := store.Open(ctx, oldPath, store.Options{BackupDir: paths.BackupsDir})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := st.SaveAccount(ctx, store.Account{
		ID:            accountIDString(oldID),
		Email:         entry.Email,
		DisplayName:   entry.DisplayName,
		AuthMethod:    "password",
		CredentialRef: credentialRefFor(oldID),
	}); err != nil {
		t.Fatalf("save account: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	creds := newFakeCredentials()
	if err := creds.SaveCredential(ctx, credentialRefFor(oldID), "secret"); err != nil {
		t.Fatalf("save credential: %v", err)
	}

	manager := NewManager(Deps{
		Paths:       paths,
		Settings:    &fakeSettingsStore{cfg: settings.Default()},
		Credentials: creds,
	})

	accounts := manager.Accounts()
	if len(accounts) != 1 {
		t.Fatalf("registry holds %d accounts, want 1", len(accounts))
	}
	newID := accounts[0].ID
	if newID == oldID || newID <= 0 || newID > maxSafeAccountID {
		t.Fatalf("migrated id = %d, want a different safe id (old %d, max %d)", newID, oldID, maxSafeAccountID)
	}

	newPath := paths.AccountDatabasePath(accountIDString(newID))
	if _, err := os.Stat(newPath); err != nil {
		t.Fatalf("migrated database missing: %v", err)
	}
	if _, err := os.Stat(oldPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old database still present: %v", err)
	}

	// The keyring reference is intentionally left as-is.
	if !creds.has(credentialRefFor(oldID)) {
		t.Error("credential reference changed during migration")
	}

	migrated, err := store.Open(ctx, newPath, store.Options{BackupDir: paths.BackupsDir})
	if err != nil {
		t.Fatalf("open migrated store: %v", err)
	}
	defer func() { _ = migrated.Close() }()
	account, err := migrated.GetAccount(ctx)
	if err != nil {
		t.Fatalf("GetAccount: %v", err)
	}
	if account.ID != accountIDString(newID) {
		t.Errorf("store row id = %q, want %q", account.ID, accountIDString(newID))
	}
	if account.CredentialRef != credentialRefFor(oldID) {
		t.Errorf("store credential ref = %q, want the unchanged %q", account.CredentialRef, credentialRefFor(oldID))
	}
}
