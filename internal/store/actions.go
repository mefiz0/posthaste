package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

const actionColumns = `id, kind, message_id, uid, folder_id, target_folder_id,
	add_flags, remove_flags, created_at, attempts, last_error`

// EnqueueAction appends an action to the durable offline queue.
func (s *Store) EnqueueAction(ctx context.Context, a Action) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO actions (kind, message_id, uid, folder_id, target_folder_id,
			add_flags, remove_flags, created_at, attempts, last_error)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		string(a.Kind), a.MessageID, a.UID, a.FolderID, a.TargetFolderID,
		uint32(a.AddFlags), uint32(a.RemoveFlags), unix(s.nowFunc()), a.Attempts, a.LastError)
	if err != nil {
		return 0, fmt.Errorf("store: enqueue action %s: %w", a.Kind, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("store: enqueue action id: %w", err)
	}
	return id, nil
}

// ListActions returns queued actions in replay order (oldest first).
func (s *Store) ListActions(ctx context.Context, limit int) ([]Action, error) {
	if limit <= 0 {
		limit = 500
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+actionColumns+` FROM actions ORDER BY id ASC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("store: list actions: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var actions []Action
	for rows.Next() {
		a, err := scanAction(rows)
		if err != nil {
			return nil, err
		}
		actions = append(actions, a)
	}
	return actions, rows.Err()
}

// DeleteAction removes a completed or dropped action.
func (s *Store) DeleteAction(ctx context.Context, id int64) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM actions WHERE id = ?`, id); err != nil {
		return fmt.Errorf("store: delete action %d: %w", id, err)
	}
	return nil
}

// RecordActionAttempt bumps the attempt counter and stores the last error.
func (s *Store) RecordActionAttempt(ctx context.Context, id int64, lastErr string) error {
	if _, err := s.db.ExecContext(ctx,
		`UPDATE actions SET attempts = attempts + 1, last_error = ? WHERE id = ?`, lastErr, id); err != nil {
		return fmt.Errorf("store: record action attempt %d: %w", id, err)
	}
	return nil
}

// CountActions reports how many actions are pending.
func (s *Store) CountActions(ctx context.Context) (int, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM actions`).Scan(&n); err != nil {
		return 0, fmt.Errorf("store: count actions: %w", err)
	}
	return n, nil
}

func scanAction(sc rowScanner) (Action, error) {
	var (
		a          Action
		kind       string
		addFlags   int64
		removeFlag int64
		created    int64
	)
	err := sc.Scan(&a.ID, &kind, &a.MessageID, &a.UID, &a.FolderID, &a.TargetFolderID,
		&addFlags, &removeFlag, &created, &a.Attempts, &a.LastError)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Action{}, ErrNotFound
		}
		return Action{}, fmt.Errorf("store: scan action: %w", err)
	}
	a.Kind = ActionKind(kind)
	a.AddFlags = Flags(uint32(addFlags))
	a.RemoveFlags = Flags(uint32(removeFlag))
	a.CreatedAt = fromUnix(created)
	return a, nil
}
