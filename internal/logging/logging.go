// Package logging provides the application's structured local logger: JSON
// lines written to a size-capped, single-generation rotated file, with
// sensitive values scrubbed at the point of capture.
//
// Scrubbing here is a safety net, not the mechanism. Callers must already log
// only safe identifiers — an account is identified by its internal ID, never
// its address — and must not put message bodies, subjects, addresses,
// attachment filenames, or credentials into log fields in the first place.
// The handler additionally drops known-sensitive attribute keys and redacts
// address- and token-shaped strings, so a careless call cannot leak plaintext
// into the log file.
//
// Local logging is always on and never touches the network; it exists only
// for the user's own troubleshooting on this device.
package logging

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
)

// DefaultMaxBytes is the rotation threshold applied when Options.MaxBytes is
// unset: large enough for months of minimal logging, small enough that the
// log never matters on disk.
const DefaultMaxBytes int64 = 5 << 20

const (
	logFileName = "posthaste.log"
	// One generation is kept: the current file rotates onto this name,
	// replacing whatever was there before.
	rotatedFileName = logFileName + ".1"
)

// Options configures New.
type Options struct {
	// Dir is the directory that receives posthaste.log. It is created with
	// user-only permissions when missing; it must not be empty.
	Dir string
	// Verbose raises the level from Info to Debug for active troubleshooting.
	Verbose bool
	// MaxBytes is the size at which posthaste.log rotates to posthaste.log.1.
	// Zero or negative selects DefaultMaxBytes.
	MaxBytes int64
}

// CloseFunc releases the log file. It must be called once at shutdown; it is
// safe to call more than once.
type CloseFunc func() error

// New builds the application logger. It creates Dir when needed, rotates an
// oversized posthaste.log out of the way before opening, and returns a logger
// that writes scrubbed JSON lines to posthaste.log plus the function that
// closes the file.
func New(opts Options) (*slog.Logger, CloseFunc, error) {
	if opts.Dir == "" {
		return nil, nil, errors.New("logging: empty log directory")
	}
	maxBytes := opts.MaxBytes
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}
	level := slog.LevelInfo
	if opts.Verbose {
		level = slog.LevelDebug
	}

	if err := os.MkdirAll(opts.Dir, 0o700); err != nil {
		return nil, nil, fmt.Errorf("logging: create log dir: %w", err)
	}
	w, err := newRotatingWriter(opts.Dir, maxBytes)
	if err != nil {
		return nil, nil, err
	}
	handler := newScrubbingHandler(
		slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level}))
	return slog.New(handler), w.Close, nil
}

// rotatingWriter appends log records to posthaste.log and rotates the file to
// posthaste.log.1 (replacing the previous generation) when it would exceed the
// size cap. Rotation happens at open time and before a write that would cross
// the cap, so a record is never split across files: the JSON handler emits
// exactly one Write per record.
type rotatingWriter struct {
	mu   sync.Mutex
	dir  string
	max  int64
	file *os.File
	size int64
}

func newRotatingWriter(dir string, max int64) (*rotatingWriter, error) {
	w := &rotatingWriter{dir: dir, max: max}
	path := filepath.Join(dir, logFileName)
	info, err := os.Stat(path)
	switch {
	case err == nil && info.Size() > max:
		if err := rotateFile(path); err != nil {
			return nil, err
		}
	case err != nil && !errors.Is(err, fs.ErrNotExist):
		return nil, fmt.Errorf("logging: stat %s: %w", logFileName, err)
	}
	if err := w.open(); err != nil {
		return nil, err
	}
	return w, nil
}

func (w *rotatingWriter) open() error {
	f, err := os.OpenFile(filepath.Join(w.dir, logFileName),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("logging: open %s: %w", logFileName, err)
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return fmt.Errorf("logging: stat %s: %w", logFileName, err)
	}
	w.file = f
	w.size = info.Size()
	return nil
}

// Write implements io.Writer, rotating the file first when the incoming
// record would push it past the cap.
func (w *rotatingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.size > 0 && w.size+int64(len(p)) > w.max {
		if err := w.rotate(); err != nil {
			return 0, err
		}
	}
	n, err := w.file.Write(p)
	w.size += int64(n)
	return n, err
}

func (w *rotatingWriter) rotate() error {
	path := filepath.Join(w.dir, logFileName)
	if err := w.file.Close(); err != nil {
		w.file = nil
		return fmt.Errorf("logging: close %s for rotation: %w", logFileName, err)
	}
	w.file = nil
	if err := rotateFile(path); err != nil {
		return err
	}
	return w.open()
}

// rotateFile renames the current log file onto the single rotated generation,
// replacing any file already there.
func rotateFile(path string) error {
	if err := os.Rename(path, filepath.Join(filepath.Dir(path), rotatedFileName)); err != nil {
		return fmt.Errorf("logging: rotate %s: %w", filepath.Base(path), err)
	}
	return nil
}

// Close syncs and closes the log file. Calling it again is a no-op.
func (w *rotatingWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return nil
	}
	err := w.file.Sync()
	if cerr := w.file.Close(); err == nil {
		err = cerr
	}
	w.file = nil
	if err != nil {
		return fmt.Errorf("logging: close %s: %w", logFileName, err)
	}
	return nil
}
