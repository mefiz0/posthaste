package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

const threadColumns = `id, subject_normalized, latest_date, message_count`

// InsertThread creates a thread and returns its ID.
func (s *Store) InsertThread(ctx context.Context, t Thread) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO threads (subject_normalized, latest_date, message_count) VALUES (?, ?, ?)`,
		t.SubjectNormalized, unix(t.LatestDate), t.MessageCount)
	if err != nil {
		return 0, fmt.Errorf("store: insert thread: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("store: insert thread id: %w", err)
	}
	return id, nil
}

// ThreadByID loads a thread.
func (s *Store) ThreadByID(ctx context.Context, id int64) (Thread, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+threadColumns+` FROM threads WHERE id = ?`, id)
	return scanThread(row)
}

// ListThreads returns threads newest-activity first.
func (s *Store) ListThreads(ctx context.Context, limit, offset int) ([]Thread, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+threadColumns+` FROM threads ORDER BY latest_date DESC, id DESC LIMIT ? OFFSET ?`,
		limit, offset)
	if err != nil {
		return nil, fmt.Errorf("store: list threads: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var threads []Thread
	for rows.Next() {
		t, err := scanThread(rows)
		if err != nil {
			return nil, err
		}
		threads = append(threads, t)
	}
	return threads, rows.Err()
}

// RecomputeThreadMeta recalculates the denormalised latest date and count from
// the messages currently assigned to the thread.
func (s *Store) RecomputeThreadMeta(ctx context.Context, threadID int64) error {
	var (
		latest sql.NullInt64
		count  int
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT MAX(date), COUNT(*) FROM messages WHERE thread_id = ?`, threadID).Scan(&latest, &count)
	if err != nil {
		return fmt.Errorf("store: recompute thread meta %d: %w", threadID, err)
	}
	latestSec := int64(0)
	if latest.Valid {
		latestSec = latest.Int64
	}
	if _, err := s.db.ExecContext(ctx,
		`UPDATE threads SET latest_date = ?, message_count = ? WHERE id = ?`,
		latestSec, count, threadID); err != nil {
		return fmt.Errorf("store: update thread meta %d: %w", threadID, err)
	}
	return nil
}

// MergeThreads re-points every message from sourceID onto targetID, deletes the
// now-empty source thread, and recomputes the target's denormalised metadata.
// It is the reconciliation step used when a late-arriving ancestor unifies two
// threads that were previously grouped separately.
func (s *Store) MergeThreads(ctx context.Context, sourceID, targetID int64) error {
	if sourceID == targetID {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin merge threads: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx,
		`UPDATE messages SET thread_id = ? WHERE thread_id = ?`, targetID, sourceID); err != nil {
		return fmt.Errorf("store: re-point thread %d -> %d: %w", sourceID, targetID, err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM threads WHERE id = ?`, sourceID); err != nil {
		return fmt.Errorf("store: delete merged thread %d: %w", sourceID, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit merge threads: %w", err)
	}
	return s.RecomputeThreadMeta(ctx, targetID)
}

// CountThreads reports the number of threads.
func (s *Store) CountThreads(ctx context.Context) (int, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM threads`).Scan(&n); err != nil {
		return 0, fmt.Errorf("store: count threads: %w", err)
	}
	return n, nil
}

func scanThread(sc rowScanner) (Thread, error) {
	var (
		t      Thread
		latest int64
	)
	err := sc.Scan(&t.ID, &t.SubjectNormalized, &latest, &t.MessageCount)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Thread{}, ErrNotFound
		}
		return Thread{}, fmt.Errorf("store: scan thread: %w", err)
	}
	t.LatestDate = fromUnix(latest)
	return t, nil
}
