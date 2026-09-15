package attachment

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "blobs"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return store
}

func putBytes(t *testing.T, store *Store, content string) string {
	t.Helper()
	hash, size, err := store.Put(strings.NewReader(content))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if size != int64(len(content)) {
		t.Fatalf("Put size = %d, want %d", size, len(content))
	}
	return hash
}

func readBlob(t *testing.T, store *Store, hash, want string) {
	t.Helper()
	rc, err := store.Open(hash)
	if err != nil {
		t.Fatalf("Open(%q): %v", hash, err)
	}
	defer func() { _ = rc.Close() }()
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(got) != want {
		t.Fatalf("blob content = %q, want %q", got, want)
	}
}

func TestPutOpenRoundtrip(t *testing.T) {
	store := newTestStore(t)
	content := "hello posthaste"

	hash := putBytes(t, store, content)
	sum := sha256.Sum256([]byte(content))
	want := hex.EncodeToString(sum[:])
	if hash != want {
		t.Fatalf("hash = %s, want %s", hash, want)
	}

	// Layout: a two-character shard directory holding the full hash.
	wantPath := filepath.Join(store.dir, want[:2], want)
	if got := store.Path(hash); got != wantPath {
		t.Fatalf("Path = %s, want %s", got, wantPath)
	}
	if !store.Has(hash) {
		t.Fatal("Has = false for a stored blob")
	}
	readBlob(t, store, hash, content)

	info, err := os.Stat(store.dir)
	if err != nil {
		t.Fatalf("Stat dir: %v", err)
	}
	if !info.IsDir() {
		t.Fatal("store root is not a directory")
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Fatalf("dir perms = %o, want 700", got)
	}
}

func TestPutDeduplicates(t *testing.T) {
	store := newTestStore(t)
	content := "same bytes every time"

	first := putBytes(t, store, content)
	second := putBytes(t, store, content)
	if first != second {
		t.Fatalf("hashes differ: %s vs %s", first, second)
	}

	files, err := os.ReadDir(filepath.Join(store.dir, first[:2]))
	if err != nil {
		t.Fatalf("ReadDir shard: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("shard holds %d files, want 1", len(files))
	}
	readBlob(t, store, first, content)
}

func TestPutEmptyContent(t *testing.T) {
	store := newTestStore(t)

	hash := putBytes(t, store, "")
	sum := sha256.Sum256(nil)
	if hash != hex.EncodeToString(sum[:]) {
		t.Fatalf("hash = %s, want the empty digest", hash)
	}
	readBlob(t, store, hash, "")
}

func TestOpenUnknownOrMalformedHash(t *testing.T) {
	store := newTestStore(t)
	putBytes(t, store, "present")

	cases := []struct {
		name      string
		hash      string
		validHash bool
	}{
		{"unknown but valid", strings.Repeat("a", 64), true},
		{"empty", "", false},
		{"too short", "abc123", false},
		{"too long", strings.Repeat("a", 65), false},
		{"non-hex", strings.Repeat("g", 64), false},
		{"path traversal", "../../etc/passwd", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := store.Open(tc.hash); !errors.Is(err, ErrNotFound) {
				t.Fatalf("Open error = %v, want ErrNotFound", err)
			}
			if store.Has(tc.hash) {
				t.Fatal("Has = true for an absent blob")
			}
			if !tc.validHash && store.Path(tc.hash) != "" {
				t.Fatalf("Path = %q, want empty for a hash that never maps into the store", store.Path(tc.hash))
			}
		})
	}
}

func TestHashCaseInsensitive(t *testing.T) {
	store := newTestStore(t)
	content := "case"

	hash := putBytes(t, store, content)
	upper := strings.ToUpper(hash)
	readBlob(t, store, upper, content)
	if !store.Has(upper) {
		t.Fatal("Has = false for an uppercase hash")
	}
	if store.Path(upper) != store.Path(hash) {
		t.Fatalf("Path(%q) = %s, want %s", upper, store.Path(upper), store.Path(hash))
	}
}

func TestSweepRemovesOrphansAndPrunesShards(t *testing.T) {
	store := newTestStore(t)
	kept := putBytes(t, store, "kept")
	unreferenced := putBytes(t, store, "no references at all")
	zeroed := putBytes(t, store, "reference count of zero")

	// A shard directory left empty by earlier sweeps is pruned too.
	emptyShard := filepath.Join(store.dir, "zz")
	if err := os.MkdirAll(emptyShard, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	removed, err := store.Sweep(context.Background(), map[string]int{kept: 1, unreferenced: 0})
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	wantRemoved := map[string]bool{unreferenced: true, zeroed: true}
	if len(removed) != len(wantRemoved) {
		t.Fatalf("Sweep removed %v, want exactly %v", removed, wantRemoved)
	}
	for _, hash := range removed {
		if !wantRemoved[hash] {
			t.Fatalf("Sweep removed referenced blob %s", hash)
		}
	}
	if !store.Has(kept) {
		t.Fatal("Sweep removed the referenced blob")
	}
	if store.Has(unreferenced) || store.Has(zeroed) {
		t.Fatal("Sweep kept an orphaned blob")
	}
	if _, err := os.Stat(emptyShard); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("empty shard still present: %v", err)
	}

	// A later sweep with no references at all reclaims the rest.
	removed, err = store.Sweep(context.Background(), nil)
	if err != nil {
		t.Fatalf("second Sweep: %v", err)
	}
	if len(removed) != 1 || removed[0] != kept {
		t.Fatalf("second Sweep removed %v, want [%s]", removed, kept)
	}
	if store.Has(kept) {
		t.Fatal("second Sweep kept the unreferenced blob")
	}
}

func TestSweepHonorsContext(t *testing.T) {
	store := newTestStore(t)
	putBytes(t, store, "something")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.Sweep(ctx, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("Sweep error = %v, want context.Canceled", err)
	}
}

func TestSweepRemovesStaleTempsOnly(t *testing.T) {
	store := newTestStore(t)

	stale := filepath.Join(store.dir, "incoming-stale")
	if err := os.WriteFile(stale, []byte("partial"), 0o600); err != nil {
		t.Fatalf("WriteFile stale: %v", err)
	}
	staleTime := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(stale, staleTime, staleTime); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}

	fresh := filepath.Join(store.dir, "incoming-fresh")
	if err := os.WriteFile(fresh, []byte("in flight"), 0o600); err != nil {
		t.Fatalf("WriteFile fresh: %v", err)
	}

	kept := putBytes(t, store, "referenced")
	if _, err := store.Sweep(context.Background(), map[string]int{kept: 1}); err != nil {
		t.Fatalf("Sweep: %v", err)
	}

	if _, err := os.Stat(stale); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale temp still present: %v", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatalf("fresh temp removed: %v", err)
	}
	readBlob(t, store, kept, "referenced")
}

func TestConcurrentPutsSameContent(t *testing.T) {
	store := newTestStore(t)
	content := "racing writers, one blob"

	const writers = 24
	var (
		hashes = make([]string, writers)
		sizes  = make([]int64, writers)
		errs   = make([]error, writers)
		wg     sync.WaitGroup
	)
	for i := range hashes {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			hashes[i], sizes[i], errs[i] = store.Put(strings.NewReader(content))
		}(i)
	}
	wg.Wait()

	for i := range hashes {
		if errs[i] != nil {
			t.Fatalf("Put %d: %v", i, errs[i])
		}
		if hashes[i] != hashes[0] {
			t.Fatalf("Put %d hash = %s, want %s", i, hashes[i], hashes[0])
		}
		if sizes[i] != int64(len(content)) {
			t.Fatalf("Put %d size = %d, want %d", i, sizes[i], len(content))
		}
	}

	files, err := os.ReadDir(filepath.Join(store.dir, hashes[0][:2]))
	if err != nil {
		t.Fatalf("ReadDir shard: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("shard holds %d files, want 1", len(files))
	}
	readBlob(t, store, hashes[0], content)
}
