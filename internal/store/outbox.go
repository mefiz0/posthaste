package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

const outboxColumns = `id, account_id, from_address, from_name, to_addresses, cc_addresses,
	bcc_addresses, subject, body_text, body_html, in_reply_to, references_header,
	attachment_hashes, state, attempts, last_error, next_attempt_at, raw_mime_path,
	created_at, updated_at`

// UpsertOutbox inserts or updates a composed message. Keyed by its UUID ID.
func (s *Store) UpsertOutbox(ctx context.Context, m OutboxMessage) error {
	now := unix(s.nowFunc())
	if m.CreatedAt.IsZero() {
		m.CreatedAt = s.nowFunc()
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO outbox (id, account_id, from_address, from_name, to_addresses, cc_addresses,
			bcc_addresses, subject, body_text, body_html, in_reply_to, references_header,
			attachment_hashes, state, attempts, last_error, next_attempt_at, raw_mime_path,
			created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET
			from_address = excluded.from_address,
			from_name = excluded.from_name,
			to_addresses = excluded.to_addresses,
			cc_addresses = excluded.cc_addresses,
			bcc_addresses = excluded.bcc_addresses,
			subject = excluded.subject,
			body_text = excluded.body_text,
			body_html = excluded.body_html,
			in_reply_to = excluded.in_reply_to,
			references_header = excluded.references_header,
			attachment_hashes = excluded.attachment_hashes,
			state = excluded.state,
			attempts = excluded.attempts,
			last_error = excluded.last_error,
			next_attempt_at = excluded.next_attempt_at,
			raw_mime_path = excluded.raw_mime_path,
			updated_at = excluded.updated_at`,
		m.ID, m.AccountID, m.FromAddress, m.FromName, m.ToAddresses, m.CCAddresses,
		m.BCCAddresses, m.Subject, m.BodyText, m.BodyHTML, m.InReplyTo, m.References,
		m.AttachmentHashes, string(m.State), m.Attempts, m.LastError, unix(m.NextAttemptAt),
		m.RawMIMEPath, unix(m.CreatedAt), now)
	if err != nil {
		return fmt.Errorf("store: upsert outbox %s: %w", m.ID, err)
	}
	return nil
}

// OutboxByID loads one outgoing message.
func (s *Store) OutboxByID(ctx context.Context, id string) (OutboxMessage, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+outboxColumns+` FROM outbox WHERE id = ?`, id)
	return scanOutbox(row)
}

// ListOutbox returns outgoing messages in a state, oldest first.
func (s *Store) ListOutbox(ctx context.Context, state SendState) ([]OutboxMessage, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+outboxColumns+` FROM outbox WHERE state = ? ORDER BY created_at ASC`, string(state))
	if err != nil {
		return nil, fmt.Errorf("store: list outbox: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var messages []OutboxMessage
	for rows.Next() {
		m, err := scanOutbox(rows)
		if err != nil {
			return nil, err
		}
		messages = append(messages, m)
	}
	return messages, rows.Err()
}

// NextDueSend returns the oldest queued message whose retry time has passed.
func (s *Store) NextDueSend(ctx context.Context, nowSec int64) (OutboxMessage, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+outboxColumns+`
		FROM outbox WHERE state = ? AND next_attempt_at <= ?
		ORDER BY created_at ASC LIMIT 1`, string(SendQueued), nowSec)
	return scanOutbox(row)
}

// UpdateOutboxState transitions a message and records the failure detail.
func (s *Store) UpdateOutboxState(ctx context.Context, id string, state SendState, attempts int, lastErr string, nextAttemptAt int64) error {
	if _, err := s.db.ExecContext(ctx, `
		UPDATE outbox SET state = ?, attempts = ?, last_error = ?, next_attempt_at = ?, updated_at = ?
		WHERE id = ?`,
		string(state), attempts, lastErr, nextAttemptAt, unix(s.nowFunc()), id); err != nil {
		return fmt.Errorf("store: update outbox state %s: %w", id, err)
	}
	return nil
}

// DeleteOutbox removes a sent or discarded message.
func (s *Store) DeleteOutbox(ctx context.Context, id string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM outbox WHERE id = ?`, id); err != nil {
		return fmt.Errorf("store: delete outbox %s: %w", id, err)
	}
	return nil
}

func scanOutbox(sc rowScanner) (OutboxMessage, error) {
	var (
		m       OutboxMessage
		state   string
		next    int64
		created int64
		updated int64
	)
	err := sc.Scan(&m.ID, &m.AccountID, &m.FromAddress, &m.FromName, &m.ToAddresses,
		&m.CCAddresses, &m.BCCAddresses, &m.Subject, &m.BodyText, &m.BodyHTML,
		&m.InReplyTo, &m.References, &m.AttachmentHashes, &state, &m.Attempts,
		&m.LastError, &next, &m.RawMIMEPath, &created, &updated)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return OutboxMessage{}, ErrNotFound
		}
		return OutboxMessage{}, fmt.Errorf("store: scan outbox: %w", err)
	}
	m.State = SendState(state)
	m.NextAttemptAt = fromUnix(next)
	m.CreatedAt = fromUnix(created)
	m.UpdatedAt = fromUnix(updated)
	return m, nil
}
