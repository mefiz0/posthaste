// Package attachment is the content-addressed blob store backing attachment
// bytes and raw MIME sources. The databases hold only metadata; every blob
// lives here exactly once, addressed by its lowercase hex SHA-256, so
// identical content shared between messages — or between accounts, since the
// store is shared — occupies disk a single time.
package attachment

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ErrNotFound is returned when no blob exists for the requested hash.
var ErrNotFound = errors.New("attachment: blob not found")

// hashLen is the length of a hex-encoded SHA-256 digest, the only file name a
// stored blob may carry.
const hashLen = sha256.Size * 2

// staleTempAge is how old an interrupted write's temporary file must be
// before Sweep removes it. A live Put finishes well within this, so sweeping
// never races a healthy writer.
const staleTempAge = time.Hour

// Store is a sharded, content-addressed blob directory laid out as
// <dir>/<first two hash characters>/<full hash>. Sharding keeps any single
// directory small on mailboxes with millions of blobs. Blobs are immutable
// and reach their final name via atomic renames, so a Store is safe for
// concurrent use.
type Store struct {
	dir string
}

// Open returns a Store rooted at dir, creating the directory when missing.
// User-only permissions are the floor: blobs are untrusted mail content and
// are not meant to be readable by other local users.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("attachment: create %s: %w", dir, err)
	}
	return &Store{dir: dir}, nil
}

// Put stores everything readable from r and returns the lowercase hex SHA-256
// of the content and its byte size. Identical content is stored once: the
// bytes land in a temporary file that is renamed into place, so a crash
// mid-write never leaves a partial blob under its final name.
func (s *Store) Put(r io.Reader) (contentHash string, size int64, err error) {
	tmp, err := os.CreateTemp(s.dir, "incoming-*")
	if err != nil {
		return "", 0, fmt.Errorf("attachment: create temp file: %w", err)
	}
	// The temp file is removed on every path that did not rename it into
	// place; Sweep reclaims the leftovers of a hard crash.
	defer func() {
		_ = tmp.Close()
		if err != nil {
			_ = os.Remove(tmp.Name())
		}
	}()

	digest := sha256.New()
	size, err = io.Copy(io.MultiWriter(tmp, digest), r)
	if err != nil {
		return "", 0, fmt.Errorf("attachment: write temp file: %w", err)
	}
	if err = tmp.Sync(); err != nil {
		return "", 0, fmt.Errorf("attachment: sync temp file: %w", err)
	}
	contentHash = hex.EncodeToString(digest.Sum(nil))

	target := s.Path(contentHash)
	if err = os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return "", 0, fmt.Errorf("attachment: create shard dir: %w", err)
	}
	_, statErr := os.Stat(target)
	switch {
	case statErr == nil:
		// Identical content is already stored; drop the duplicate bytes.
		return contentHash, size, nil
	case !errors.Is(statErr, os.ErrNotExist):
		return "", 0, fmt.Errorf("attachment: stat blob: %w", statErr)
	}
	// The rename is atomic: a reader sees either the old directory entry or
	// the complete new one, never a partially written file. Two concurrent
	// Puts of the same content rename identical bytes onto each other, so
	// either ordering of the race leaves a correct blob.
	if err = os.Rename(tmp.Name(), target); err != nil {
		return "", 0, fmt.Errorf("attachment: rename blob into place: %w", err)
	}
	return contentHash, size, nil
}

// Open returns a reader over the stored blob. It returns an error wrapping
// ErrNotFound when no blob with that hash exists, including when the hash is
// malformed.
func (s *Store) Open(contentHash string) (io.ReadCloser, error) {
	hash, ok := normalizeHash(contentHash)
	if !ok {
		return nil, fmt.Errorf("attachment: %q: %w", contentHash, ErrNotFound)
	}
	f, err := os.Open(s.Path(hash))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("attachment: %s: %w", hash, ErrNotFound)
		}
		return nil, fmt.Errorf("attachment: open blob: %w", err)
	}
	return f, nil
}

// Has reports whether the blob exists locally.
func (s *Store) Has(contentHash string) bool {
	hash, ok := normalizeHash(contentHash)
	if !ok {
		return false
	}
	_, err := os.Stat(s.Path(hash))
	return err == nil
}

// Path is the filesystem path where the blob with this hash lives, or would
// live once stored. It returns the empty string for a malformed hash rather
// than a usable path, so a corrupt hash can never escape the store root.
func (s *Store) Path(contentHash string) string {
	hash, ok := normalizeHash(contentHash)
	if !ok {
		return ""
	}
	return filepath.Join(s.dir, hash[:2], hash)
}

// Sweep removes blobs that nothing references and prunes shard directories
// left empty. refCounts maps content hash to the number of rows referencing
// it; because the blob store is shared by every account, the caller must
// aggregate counts across all account databases before sweeping, or blobs
// still in use elsewhere will be deleted. A file inside a shard whose name is
// not a valid hash is unreachable through Has and Open and is removed too,
// while an absent or non-positive count marks a blob as an orphan. Temporary
// files from interrupted writes are removed once older than the sweep can
// safely assume no live Put owns them. Sweep returns the hashes it removed,
// in walk order, and stops on the first error or when ctx is done.
func (s *Store) Sweep(ctx context.Context, refCounts map[string]int) ([]string, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, fmt.Errorf("attachment: read %s: %w", s.dir, err)
	}
	var removed []string
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("attachment: sweep: %w", err)
		}
		if !entry.IsDir() {
			// Temporary files from interrupted writes are not blobs; only
			// reclaim ones old enough that no live write can own them.
			info, err := entry.Info()
			if err != nil {
				return nil, fmt.Errorf("attachment: stat %s: %w", entry.Name(), err)
			}
			if time.Since(info.ModTime()) > staleTempAge {
				path := filepath.Join(s.dir, entry.Name())
				if err := os.Remove(path); err != nil {
					return nil, fmt.Errorf("attachment: remove stale temp: %w", err)
				}
			}
			continue
		}
		remaining, err := s.sweepShard(ctx, entry.Name(), refCounts, &removed)
		if err != nil {
			return nil, err
		}
		if remaining == 0 {
			// A shard directory holds nothing but blobs; an empty one is a
			// leftover from earlier sweeps or removed content.
			if err := os.Remove(filepath.Join(s.dir, entry.Name())); err != nil {
				return nil, fmt.Errorf("attachment: prune shard %s: %w", entry.Name(), err)
			}
		}
	}
	return removed, nil
}

// sweepShard removes unreferenced blobs from one shard directory and returns
// how many entries remain in it.
func (s *Store) sweepShard(ctx context.Context, shard string, refCounts map[string]int, removed *[]string) (int, error) {
	dir := filepath.Join(s.dir, shard)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, fmt.Errorf("attachment: read shard %s: %w", dir, err)
	}
	remaining := 0
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return remaining, fmt.Errorf("attachment: sweep: %w", err)
		}
		if entry.IsDir() {
			// Blobs are files two levels deep; keep anything deeper rather
			// than delete data the store does not own.
			remaining++
			continue
		}
		hash, ok := normalizeHash(entry.Name())
		if ok && refCounts[hash] > 0 {
			remaining++
			continue
		}
		path := filepath.Join(dir, entry.Name())
		if err := os.Remove(path); err != nil {
			return remaining, fmt.Errorf("attachment: remove %s: %w", path, err)
		}
		if ok {
			*removed = append(*removed, hash)
		}
	}
	return remaining, nil
}

// normalizeHash accepts a hex SHA-256 digest in either case and returns the
// lowercase form. Anything else is rejected: hashes arrive from untrusted
// message metadata and become path components, so they are validated
// strictly.
func normalizeHash(contentHash string) (string, bool) {
	if len(contentHash) != hashLen {
		return "", false
	}
	for _, c := range contentHash {
		isHex := (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
		if !isHex {
			return "", false
		}
	}
	return strings.ToLower(contentHash), true
}
