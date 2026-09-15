package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRegistryMissingFileIsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "accounts.json")
	registry, err := LoadRegistry(path)
	if err != nil {
		t.Fatalf("LoadRegistry: %v", err)
	}
	if entries := registry.List(); len(entries) != 0 {
		t.Errorf("fresh registry holds %d entries, want 0", len(entries))
	}
}

func TestRegistryAddPersistsAndReloads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "accounts.json")
	registry, err := LoadRegistry(path)
	if err != nil {
		t.Fatalf("LoadRegistry: %v", err)
	}

	first, err := registry.Add(RegistryEntry{ID: 11, Email: "a@x.test", DisplayName: "A"})
	if err != nil {
		t.Fatalf("Add first: %v", err)
	}
	second, err := registry.Add(RegistryEntry{ID: 22, Email: "b@x.test", DisplayName: "B", Color: "#123456"})
	if err != nil {
		t.Fatalf("Add second: %v", err)
	}
	if first.Ordinal != 1 {
		t.Errorf("first ordinal = %d, want 1", first.Ordinal)
	}
	if second.Ordinal != 2 {
		t.Errorf("second ordinal = %d, want 2", second.Ordinal)
	}
	if first.Color == "" {
		t.Error("first entry left without a palette colour")
	}
	if second.Color != "#123456" {
		t.Errorf("explicit colour overridden: %q", second.Color)
	}

	reloaded, err := LoadRegistry(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	entries := reloaded.List()
	if len(entries) != 2 {
		t.Fatalf("reloaded registry holds %d entries, want 2", len(entries))
	}
	if entries[0].ID != 11 || entries[1].ID != 22 {
		t.Errorf("entries out of order: %d, %d", entries[0].ID, entries[1].ID)
	}
}

func TestRegistryUpdateAndRemove(t *testing.T) {
	path := filepath.Join(t.TempDir(), "accounts.json")
	registry, err := LoadRegistry(path)
	if err != nil {
		t.Fatalf("LoadRegistry: %v", err)
	}
	if _, err := registry.Add(RegistryEntry{ID: 5, Email: "a@x.test"}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	if err := registry.Update(5, func(e *RegistryEntry) { e.Paused = true }); err != nil {
		t.Fatalf("Update: %v", err)
	}
	got, ok := registry.ByID(5)
	if !ok || !got.Paused {
		t.Errorf("update not visible in memory: %+v ok=%v", got, ok)
	}
	reloaded, err := LoadRegistry(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	got, ok = reloaded.ByID(5)
	if !ok || !got.Paused {
		t.Errorf("update not persisted: %+v ok=%v", got, ok)
	}

	if err := registry.Remove(5); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if err := registry.Remove(5); err == nil {
		t.Error("removing an absent entry succeeded")
	}
	if _, err := LoadRegistry(path); err != nil {
		t.Fatalf("reload after remove: %v", err)
	}
}

func TestRegistrySaveLeavesNoTempFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "accounts.json")
	registry, err := LoadRegistry(path)
	if err != nil {
		t.Fatalf("LoadRegistry: %v", err)
	}
	if _, err := registry.Add(RegistryEntry{ID: 1, Email: "a@x.test"}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".accounts-") {
			t.Errorf("temporary registry file left behind: %s", entry.Name())
		}
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("registry permissions = %o, want 600", perm)
	}
}

func TestRegistryCorruptFileIsAnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "accounts.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if _, err := LoadRegistry(path); err == nil {
		t.Error("LoadRegistry of a corrupt file succeeded")
	}
}

func TestNewAccountIDIsRandomPositive(t *testing.T) {
	seen := make(map[int64]bool)
	for i := 0; i < 100; i++ {
		id, err := newAccountID()
		if err != nil {
			t.Fatalf("newAccountID: %v", err)
		}
		if id <= 0 {
			t.Fatalf("newAccountID produced %d", id)
		}
		if seen[id] {
			t.Fatalf("newAccountID repeated %d", id)
		}
		seen[id] = true
	}
}
