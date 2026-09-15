package app

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// RegistryEntry is one account's pre-database record: the identity that must
// exist before any account SQLite file is opened. The ID is a random positive
// integer drawn from the crypto source; the ordinal is a small, stable number
// used to build globally unique folder IDs.
type RegistryEntry struct {
	ID          int64  `json:"id"`
	Ordinal     int    `json:"ordinal"`
	Email       string `json:"email"`
	DisplayName string `json:"displayName"`
	Color       string `json:"color,omitempty"`
	Paused      bool   `json:"paused"`
}

// accountColors is the palette assigned to accounts in creation order,
// matching the swatches the UI uses to attribute unified-inbox rows.
var accountColors = []string{"#5f8f5f", "#a4703c", "#5b7ea4", "#8a5ba4"}

// colorForOrdinal picks the palette entry for an account ordinal, wrapping so
// the nth account still gets a usable swatch.
func colorForOrdinal(ordinal int) string {
	if ordinal <= 0 {
		return accountColors[0]
	}
	return accountColors[(ordinal-1)%len(accountColors)]
}

// Registry is the small JSON file listing every configured account. It is the
// only cross-account state in the whole application; everything else is
// strictly per account. Loading a missing file yields an empty registry so a
// first run never fails.
type Registry struct {
	mu      sync.Mutex
	path    string
	entries []RegistryEntry
}

// LoadRegistry reads the registry at path. A missing file is an empty
// registry; a corrupt one is an error the caller surfaces, because silently
// discarding it would look like every account vanished.
func LoadRegistry(path string) (*Registry, error) {
	r := &Registry{path: path}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return r, nil
		}
		return nil, fmt.Errorf("app: read account registry: %w", err)
	}
	if len(data) == 0 {
		return r, nil
	}
	if err := json.Unmarshal(data, &r.entries); err != nil {
		return nil, fmt.Errorf("app: parse account registry: %w", err)
	}
	return r, nil
}

// List returns a copy of the entries ordered by ordinal, which is the order
// accounts were added in.
func (r *Registry) List() []RegistryEntry {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]RegistryEntry, len(r.entries))
	copy(out, r.entries)
	return out
}

// ByID returns a copy of one entry.
func (r *Registry) ByID(id int64) (RegistryEntry, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, e := range r.entries {
		if e.ID == id {
			return e, true
		}
	}
	return RegistryEntry{}, false
}

// Add appends a new entry, assigning the next free ordinal and a palette
// colour when the caller left it empty, and persists the file atomically.
func (r *Registry) Add(entry RegistryEntry) (RegistryEntry, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	next := 1
	for _, e := range r.entries {
		if e.Ordinal >= next {
			next = e.Ordinal + 1
		}
		if e.ID == entry.ID {
			return RegistryEntry{}, fmt.Errorf("app: account %d already registered", entry.ID)
		}
	}
	entry.Ordinal = next
	if entry.Color == "" {
		entry.Color = colorForOrdinal(next)
	}
	r.entries = append(r.entries, entry)
	if err := r.saveLocked(); err != nil {
		r.entries = r.entries[:len(r.entries)-1]
		return RegistryEntry{}, err
	}
	return entry, nil
}

// Update rewrites one entry through mutator and persists the file. The entry
// is left untouched when mutator or the write fails.
func (r *Registry) Update(id int64, mutator func(*RegistryEntry)) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.entries {
		if r.entries[i].ID != id {
			continue
		}
		original := r.entries[i]
		mutator(&r.entries[i])
		if err := r.saveLocked(); err != nil {
			r.entries[i] = original
			return err
		}
		return nil
	}
	return fmt.Errorf("app: account %d is not registered", id)
}

// Remove drops one entry and persists the file.
func (r *Registry) Remove(id int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	kept := r.entries[:0]
	removed := false
	for _, e := range r.entries {
		if e.ID == id {
			removed = true
			continue
		}
		kept = append(kept, e)
	}
	if !removed {
		return fmt.Errorf("app: account %d is not registered", id)
	}
	original := append([]RegistryEntry(nil), r.entries...)
	r.entries = kept
	if err := r.saveLocked(); err != nil {
		r.entries = original
		return err
	}
	return nil
}

// saveLocked writes the registry through a temporary file and rename so a
// crash mid-write cannot lose the account list. Callers hold mu.
func (r *Registry) saveLocked() error {
	if err := os.MkdirAll(filepath.Dir(r.path), 0o700); err != nil {
		return fmt.Errorf("app: create config dir: %w", err)
	}
	data, err := json.MarshalIndent(r.entries, "", "  ")
	if err != nil {
		return fmt.Errorf("app: encode account registry: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(r.path), ".accounts-*.json")
	if err != nil {
		return fmt.Errorf("app: create registry temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("app: write registry temp file: %w", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("app: chmod registry temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("app: close registry temp file: %w", err)
	}
	if err := os.Rename(tmpName, r.path); err != nil {
		return fmt.Errorf("app: rename registry into place: %w", err)
	}
	return nil
}

// newAccountID draws a random positive account ID from the crypto source.
// IDs are unguessable because they appear in file names and keyring
// references; a collision across the small number of accounts is negligible,
// and a zero result is redrawn because zero means "unset" at the bridge.
//
// The draw is masked to 53 bits: account IDs cross the Wails bridge as JSON
// numbers, and JavaScript cannot represent integers above 2^53-1 exactly, so
// a larger ID would come back to the backend as a different number.
func newAccountID() (int64, error) {
	for {
		var raw [8]byte
		if _, err := rand.Read(raw[:]); err != nil {
			return 0, fmt.Errorf("app: generate account id: %w", err)
		}
		id := int64(binary.BigEndian.Uint64(raw[:]) >> 11)
		if id > 0 {
			return id, nil
		}
	}
}
