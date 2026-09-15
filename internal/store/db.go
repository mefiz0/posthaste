package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/pressly/goose/v3"

	"github.com/mefiz0/posthaste/migrations"

	_ "modernc.org/sqlite"
)

// Options configures a per-account store.
type Options struct {
	// BackupDir receives a copy of the database before a version upgrade
	// migrates it. Empty disables the pre-migration backup.
	BackupDir string
	// Logger receives migration and backup events. It must already scrub
	// sensitive values at capture, since callers cannot rely on later redaction.
	Logger *slog.Logger
}

// Store is a handle to one account's SQLite database.
type Store struct {
	db      *sql.DB
	path    string
	logger  *slog.Logger
	nowFunc func() time.Time
}

// Open opens (creating if needed) the per-account database at path, applies
// pragmas, takes a pre-migration backup when the schema is behind, and runs
// embedded goose migrations.
func Open(ctx context.Context, path string, opts Options) (*Store, error) {
	if path == "" {
		return nil, errors.New("store: empty database path")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("store: create data dir: %w", err)
	}

	db, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", filepath.Base(path), err)
	}
	// SQLite serialises writers; a single connection avoids busy retries while
	// WAL still allows the reader pool to proceed. Reads are short-lived.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("store: ping %s: %w", filepath.Base(path), err)
	}

	s := &Store{db: db, path: path, logger: opts.Logger, nowFunc: time.Now}

	pending, err := s.pendingMigrations(ctx)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	if pending > 0 && opts.BackupDir != "" && s.hasContent(ctx) {
		if err := s.backup(ctx, opts.BackupDir); err != nil {
			_ = db.Close()
			return nil, err
		}
	}

	if err := s.migrate(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// Close releases the database handle.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// DB exposes the underlying pool for repositories and tests.
func (s *Store) DB() *sql.DB { return s.db }

// Path returns the database file path.
func (s *Store) Path() string { return s.path }

func dsn(path string) string {
	return "file:" + path +
		"?_pragma=busy_timeout(5000)" +
		"&_pragma=journal_mode(WAL)" +
		"&_pragma=synchronous(NORMAL)" +
		"&_pragma=foreign_keys(1)" +
		"&_pragma=temp_store(MEMORY)"
}

func (s *Store) migrate(ctx context.Context) error {
	goose.SetBaseFS(migrations.FS)
	goose.SetLogger(goose.NopLogger())
	if err := goose.SetDialect("sqlite3"); err != nil {
		return fmt.Errorf("store: goose dialect: %w", err)
	}
	if err := goose.UpContext(ctx, s.db, "."); err != nil {
		return fmt.Errorf("store: migrate %s: %w", filepath.Base(s.path), err)
	}
	return nil
}

// pendingMigrations counts migrations newer than the recorded schema version.
func (s *Store) pendingMigrations(ctx context.Context) (int, error) {
	goose.SetBaseFS(migrations.FS)
	if err := goose.SetDialect("sqlite3"); err != nil {
		return 0, fmt.Errorf("store: goose dialect: %w", err)
	}
	all, err := goose.CollectMigrations(".", 0, goose.MaxVersion)
	if err != nil {
		return 0, fmt.Errorf("store: collect migrations: %w", err)
	}
	current, err := goose.GetDBVersionContext(ctx, s.db)
	if err != nil {
		// No version table yet: everything is pending.
		return len(all), nil
	}
	pending := 0
	for _, m := range all {
		if m.Version > current {
			pending++
		}
	}
	return pending, nil
}

func (s *Store) hasContent(ctx context.Context) bool {
	var name string
	err := s.db.QueryRowContext(ctx,
		`SELECT name FROM sqlite_master WHERE type='table' AND name='messages'`).Scan(&name)
	return err == nil
}

func (s *Store) backup(ctx context.Context, backupDir string) error {
	if err := os.MkdirAll(backupDir, 0o700); err != nil {
		return fmt.Errorf("store: create backup dir: %w", err)
	}
	// Fold the WAL back into the main file so a plain copy is complete.
	if _, err := s.db.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		return fmt.Errorf("store: checkpoint before backup: %w", err)
	}

	timestamp := s.nowFunc().UTC().Format("20060102T150405Z")
	dest := filepath.Join(backupDir,
		fmt.Sprintf("%s.%s.bak", filepath.Base(s.path), timestamp))

	if err := copyFile(s.path, dest); err != nil {
		return fmt.Errorf("store: backup: %w", err)
	}
	if s.logger != nil {
		s.logger.Info("store: pre-migration backup written",
			"database", filepath.Base(s.path), "backup", filepath.Base(dest))
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// SetClock injects a clock; used by tests and by repositories that stamp rows.
func (s *Store) SetClock(now func() time.Time) {
	if now != nil {
		s.nowFunc = now
	}
}

func unix(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UTC().Unix()
}

func fromUnix(sec int64) time.Time {
	if sec == 0 {
		return time.Time{}
	}
	return time.Unix(sec, 0).UTC()
}
