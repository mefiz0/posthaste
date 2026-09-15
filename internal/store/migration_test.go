package store

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/pressly/goose/v3"

	"github.com/mefiz0/posthaste/migrations"
)

// TestMigrationsFromEveryPriorVersion builds a fixture database at each schema
// version an installed copy could be sitting on, seeds it with representative
// rows, then opens it through the normal path and asserts that upgrading
// preserves the data and leaves every current table and index usable.
func TestMigrationsFromEveryPriorVersion(t *testing.T) {
	now := time.Unix(1700000000, 0).UTC()
	latest := latestMigrationVersion(t)

	for version := int64(1); version < latest; version++ {
		t.Run(fmt.Sprintf("from_v%d", version), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "account.sqlite")
			applyMigrationsUpTo(t, path, version)
			seedLegacyData(t, path, version, now)

			s, err := Open(context.Background(), path, Options{})
			if err != nil {
				t.Fatalf("Open after v%d fixture: %v", version, err)
			}
			t.Cleanup(func() { _ = s.Close() })

			assertSchemaAtLatest(t, s, latest)
			assertLegacyDataPreserved(t, s, version, now)
		})
	}
}

// latestMigrationVersion returns the highest embedded migration version.
func latestMigrationVersion(t *testing.T) int64 {
	t.Helper()
	goose.SetBaseFS(migrations.FS)
	if err := goose.SetDialect("sqlite3"); err != nil {
		t.Fatalf("goose dialect: %v", err)
	}
	all, err := goose.CollectMigrations(".", 0, goose.MaxVersion)
	if err != nil {
		t.Fatalf("collect migrations: %v", err)
	}
	if len(all) == 0 {
		t.Fatal("no migrations embedded")
	}
	return all[len(all)-1].Version
}

// applyMigrationsUpTo creates a database at path and migrates it to exactly
// the given version, reproducing an older installation.
func applyMigrationsUpTo(t *testing.T, path string, version int64) {
	t.Helper()
	db, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer func() { _ = db.Close() }()

	goose.SetBaseFS(migrations.FS)
	goose.SetLogger(goose.NopLogger())
	if err := goose.SetDialect("sqlite3"); err != nil {
		t.Fatalf("goose dialect: %v", err)
	}
	if err := goose.UpToContext(context.Background(), db, ".", version); err != nil {
		t.Fatalf("migrate fixture to v%d: %v", version, err)
	}
}

// seedLegacyData inserts rows that only use columns available at version, so
// the upgrade has something to preserve.
func seedLegacyData(t *testing.T, path string, version int64, now time.Time) {
	t.Helper()
	db, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		t.Fatalf("open fixture for seed: %v", err)
	}
	defer func() { _ = db.Close() }()

	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(query, args...); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	exec(`INSERT INTO folders (id, name, imap_path, type) VALUES (1, 'Inbox', 'INBOX', 'inbox')`)
	exec(`INSERT INTO messages
		(id, folder_id, uid, message_id_header, from_name, from_address, to_addresses, subject, date, body_text)
		VALUES (1, 1, 7, '<legacy@example.com>', 'Alice', 'alice@example.com', 'me@example.com', 'Legacy subject', ?, 'legacy body text')`,
		now.Unix())
	exec(`INSERT INTO accounts (id, email, display_name) VALUES ('acct-1', 'me@example.com', 'Me')`)
	exec(`INSERT INTO actions (kind, message_id, add_flags) VALUES ('flag', 1, 1)`)

	if version >= 2 {
		exec(`INSERT INTO attachments
			(message_id, filename, mime_type, size_bytes, content_hash, fetch_state)
			VALUES (1, 'notes.txt', 'text/plain', 5, 'hash-1', 'fetched')`)
		exec(`INSERT INTO contacts (email, display_name, frequency_count) VALUES ('bob@example.com', 'Bob', 3)`)
	}
	if version >= 3 {
		exec(`INSERT INTO threads (id, subject_normalized, latest_date, message_count)
			VALUES (1, 'legacy subject', ?, 1)`, now.Unix())
		exec(`UPDATE messages SET thread_id = 1 WHERE id = 1`)
	}
}

// assertSchemaAtLatest verifies the database is on the newest migration and
// that every current table exists.
func assertSchemaAtLatest(t *testing.T, s *Store, latest int64) {
	t.Helper()
	ctx := context.Background()

	goose.SetBaseFS(migrations.FS)
	if err := goose.SetDialect("sqlite3"); err != nil {
		t.Fatalf("goose dialect: %v", err)
	}
	current, err := goose.GetDBVersionContext(ctx, s.DB())
	if err != nil {
		t.Fatalf("read schema version: %v", err)
	}
	if current != latest {
		t.Fatalf("schema version = %d, want %d", current, latest)
	}

	for _, table := range []string{"folders", "messages", "accounts", "actions", "attachments", "contacts", "threads", "outbox", "messages_fts"} {
		var name string
		if err := s.DB().QueryRowContext(ctx,
			`SELECT name FROM sqlite_master WHERE name = ?`, table).Scan(&name); err != nil {
			t.Fatalf("table %q missing after migration: %v", table, err)
		}
	}
}

// assertLegacyDataPreserved checks the seeded rows survived the upgrade and
// that the FTS index was backfilled for pre-existing messages.
func assertLegacyDataPreserved(t *testing.T, s *Store, version int64, now time.Time) {
	t.Helper()
	ctx := context.Background()

	var (
		email   string
		subject string
		date    int64
	)
	if err := s.DB().QueryRowContext(ctx, `SELECT email FROM accounts WHERE id = 'acct-1'`).Scan(&email); err != nil {
		t.Fatalf("account row lost: %v", err)
	}
	if email != "me@example.com" {
		t.Fatalf("account email = %q, want me@example.com", email)
	}
	if err := s.DB().QueryRowContext(ctx,
		`SELECT subject, date FROM messages WHERE id = 1`).Scan(&subject, &date); err != nil {
		t.Fatalf("message row lost: %v", err)
	}
	if subject != "Legacy subject" || date != now.Unix() {
		t.Fatalf("message changed: subject=%q date=%d", subject, date)
	}

	var actions int
	if err := s.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM actions`).Scan(&actions); err != nil || actions != 1 {
		t.Fatalf("action rows = %d (err %v), want 1", actions, err)
	}

	if version >= 2 {
		var attachments, contacts int
		if err := s.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM attachments`).Scan(&attachments); err != nil || attachments != 1 {
			t.Fatalf("attachment rows = %d (err %v), want 1", attachments, err)
		}
		if err := s.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM contacts`).Scan(&contacts); err != nil || contacts != 1 {
			t.Fatalf("contact rows = %d (err %v), want 1", contacts, err)
		}
	}

	if version >= 3 {
		var threadID int64
		if err := s.DB().QueryRowContext(ctx, `SELECT thread_id FROM messages WHERE id = 1`).Scan(&threadID); err != nil {
			t.Fatalf("thread_id lost: %v", err)
		}
		if threadID != 1 {
			t.Fatalf("thread_id = %d, want 1", threadID)
		}
	}

	// The FTS index must cover messages that predate it, or upgrading would
	// silently make old mail unsearchable.
	var hits int
	if err := s.DB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM messages_fts WHERE messages_fts MATCH 'legacy'`).Scan(&hits); err != nil {
		t.Fatalf("fts query: %v", err)
	}
	if hits != 1 {
		t.Fatalf("fts hits = %d, want 1 (index not backfilled?)", hits)
	}
}
