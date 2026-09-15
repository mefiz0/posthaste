package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

const messageColumns = `id, folder_id, uid, message_id_header, in_reply_to, references_header,
	from_address, from_name, to_addresses, cc_addresses, subject, date, flags, size_bytes,
	raw_mime_path, body_text, body_html, has_attachments, thread_id, created_at, updated_at`

// InsertMessage stores a new message and returns its local ID. The FTS index is
// updated by trigger, so no separate indexing step is needed.
func (s *Store) InsertMessage(ctx context.Context, m NewMessage) (int64, error) {
	const q = `
		INSERT INTO messages (folder_id, uid, message_id_header, in_reply_to, references_header,
			from_address, from_name, to_addresses, cc_addresses, subject, date, flags, size_bytes,
			raw_mime_path, body_text, body_html, has_attachments, thread_id, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

	now := unix(s.nowFunc())
	res, err := s.db.ExecContext(ctx, q,
		m.FolderID, m.UID, m.MessageIDHeader, m.InReplyTo, m.References,
		m.FromAddress, m.FromName, m.ToAddresses, m.CCAddresses, m.Subject,
		unix(m.Date), uint32(m.Flags), m.SizeBytes, m.RawMIMEPath, m.BodyText, m.BodyHTML,
		boolInt(m.HasAttachments), m.ThreadID, now, now,
	)
	if err != nil {
		return 0, fmt.Errorf("store: insert message in folder %d: %w", m.FolderID, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("store: insert message id: %w", err)
	}
	return id, nil
}

// UpdateMessageContent sets the derived body fields and attachment marker after
// MIME parsing.
func (s *Store) UpdateMessageContent(ctx context.Context, id int64, bodyText, bodyHTML, rawPath string, hasAttachments bool) error {
	if _, err := s.db.ExecContext(ctx, `
		UPDATE messages SET body_text = ?, body_html = ?, raw_mime_path = ?, has_attachments = ?, updated_at = ?
		WHERE id = ?`,
		bodyText, bodyHTML, rawPath, boolInt(hasAttachments), unix(s.nowFunc()), id); err != nil {
		return fmt.Errorf("store: update message content %d: %w", id, err)
	}
	return nil
}

// UpdateMessageHeaders fills header data when a body-less placeholder is later
// completed.
func (s *Store) UpdateMessageHeaders(ctx context.Context, m Message) error {
	if _, err := s.db.ExecContext(ctx, `
		UPDATE messages SET message_id_header = ?, in_reply_to = ?, references_header = ?,
			from_address = ?, from_name = ?, to_addresses = ?, cc_addresses = ?, subject = ?,
			date = ?, flags = ?, size_bytes = ?, updated_at = ?
		WHERE id = ?`,
		m.MessageIDHeader, m.InReplyTo, m.References, m.FromAddress, m.FromName,
		m.ToAddresses, m.CCAddresses, m.Subject, unix(m.Date), uint32(m.Flags), m.SizeBytes,
		unix(s.nowFunc()), m.ID); err != nil {
		return fmt.Errorf("store: update message headers %d: %w", m.ID, err)
	}
	return nil
}

// MessageByID loads a single message.
func (s *Store) MessageByID(ctx context.Context, id int64) (Message, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+messageColumns+` FROM messages WHERE id = ?`, id)
	return scanMessage(row)
}

// MessageByUID loads a message by folder and IMAP UID.
func (s *Store) MessageByUID(ctx context.Context, folderID int64, uid uint32) (Message, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+messageColumns+` FROM messages WHERE folder_id = ? AND uid = ?`, folderID, uid)
	return scanMessage(row)
}

// MessageByMessageID loads the first message with the given RFC 5322 Message-ID
// header, used for threading lookups.
func (s *Store) MessageByMessageID(ctx context.Context, messageID string) (Message, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+messageColumns+` FROM messages WHERE message_id_header = ? ORDER BY id LIMIT 1`, messageID)
	return scanMessage(row)
}

// FindAdoptableMessage locates a local row that already represents the server
// message with the given RFC 5322 Message-ID header and can be adopted instead
// of inserting a duplicate row. A row is adoptable when it lives in the given
// folder under the same Message-ID, or — covering a locally moved row whose
// UID was cleared — when it has no UID, in any folder. A row in another folder
// that still has a real UID never matches, so the same message legitimately
// stored in two folders keeps both rows. An empty Message-ID header never
// matches anything: it cannot identify a message. Every row lives in this
// account's own database, so a lookup can never reach another account's mail.
func (s *Store) FindAdoptableMessage(ctx context.Context, folderID int64, messageIDHeader string) (Message, error) {
	if strings.TrimSpace(messageIDHeader) == "" {
		return Message{}, ErrNotFound
	}
	row := s.db.QueryRowContext(ctx,
		`SELECT `+messageColumns+` FROM messages
		WHERE message_id_header = ? AND (folder_id = ? OR uid = 0)
		ORDER BY CASE WHEN folder_id = ? THEN 0 ELSE 1 END, id
		LIMIT 1`,
		messageIDHeader, folderID, folderID)
	return scanMessage(row)
}

// AdoptMessage points an existing row at the server's copy of the same
// message: the row joins the given folder under the server UID and takes the
// server's flags and size, while keeping its body, attachments, and thread.
// This is how a moved row (folder repointed and UID cleared by MoveMessage)
// becomes a first-class row of its new folder once the server assigns the UID.
func (s *Store) AdoptMessage(ctx context.Context, id, folderID int64, uid uint32, flags Flags, sizeBytes int64) error {
	if _, err := s.db.ExecContext(ctx,
		`UPDATE messages SET folder_id = ?, uid = ?, flags = ?, size_bytes = ?, updated_at = ? WHERE id = ?`,
		folderID, uid, uint32(flags), sizeBytes, unix(s.nowFunc()), id); err != nil {
		return fmt.Errorf("store: adopt message %d: %w", id, err)
	}
	return nil
}

// ListMessages returns a page of messages for a folder, newest first.
func (s *Store) ListMessages(ctx context.Context, q MessageQuery) ([]Message, error) {
	if q.Limit <= 0 {
		q.Limit = 100
	}
	query := `SELECT ` + messageColumns + ` FROM messages WHERE folder_id = ?`
	args := []any{q.FolderID}
	if q.UnreadOnly {
		query += ` AND (flags & ?) = 0`
		args = append(args, uint32(FlagSeen))
	}
	if q.StarredOnly {
		query += ` AND (flags & ?) != 0`
		args = append(args, uint32(FlagFlagged))
	}
	query += ` ORDER BY date DESC, id DESC LIMIT ? OFFSET ?`
	args = append(args, q.Limit, q.Offset)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list messages: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var messages []Message
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		messages = append(messages, m)
	}
	return messages, rows.Err()
}

// ListMessagesByThread returns every message in a thread, oldest first.
func (s *Store) ListMessagesByThread(ctx context.Context, threadID int64) ([]Message, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+messageColumns+` FROM messages WHERE thread_id = ? ORDER BY date ASC, id ASC`, threadID)
	if err != nil {
		return nil, fmt.Errorf("store: list thread messages: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var messages []Message
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		messages = append(messages, m)
	}
	return messages, rows.Err()
}

// SetFlags replaces the flags on a message.
func (s *Store) SetFlags(ctx context.Context, id int64, flags Flags) error {
	if _, err := s.db.ExecContext(ctx,
		`UPDATE messages SET flags = ?, updated_at = ? WHERE id = ?`, uint32(flags), unix(s.nowFunc()), id); err != nil {
		return fmt.Errorf("store: set flags %d: %w", id, err)
	}
	return nil
}

// ApplyFlagDelta adds and removes flag bits in one statement.
func (s *Store) ApplyFlagDelta(ctx context.Context, id int64, add, remove Flags) error {
	if _, err := s.db.ExecContext(ctx,
		`UPDATE messages SET flags = (flags | ?) & ~?, updated_at = ? WHERE id = ?`,
		uint32(add), uint32(remove), unix(s.nowFunc()), id); err != nil {
		return fmt.Errorf("store: apply flag delta %d: %w", id, err)
	}
	return nil
}

// MoveMessage changes a message's folder and clears its UID, which belongs to
// the old folder.
func (s *Store) MoveMessage(ctx context.Context, id, targetFolderID int64) error {
	if _, err := s.db.ExecContext(ctx,
		`UPDATE messages SET folder_id = ?, uid = 0, updated_at = ? WHERE id = ?`,
		targetFolderID, unix(s.nowFunc()), id); err != nil {
		return fmt.Errorf("store: move message %d: %w", id, err)
	}
	return nil
}

// DeleteMessage removes a message (and cascades to its attachments).
func (s *Store) DeleteMessage(ctx context.Context, id int64) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM messages WHERE id = ?`, id); err != nil {
		return fmt.Errorf("store: delete message %d: %w", id, err)
	}
	return nil
}

// CountMessages reports the stored message count for a folder.
func (s *Store) CountMessages(ctx context.Context, folderID int64) (int, error) {
	var n int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM messages WHERE folder_id = ?`, folderID).Scan(&n); err != nil {
		return 0, fmt.Errorf("store: count messages: %w", err)
	}
	return n, nil
}

// CountUnread reports the unseen message count for a folder.
func (s *Store) CountUnread(ctx context.Context, folderID int64) (int, error) {
	var n int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM messages WHERE folder_id = ? AND (flags & ?) = 0`,
		folderID, uint32(FlagSeen)).Scan(&n); err != nil {
		return 0, fmt.Errorf("store: count unread: %w", err)
	}
	return n, nil
}

// MarkAllRead marks every message in a folder as seen.
func (s *Store) MarkAllRead(ctx context.Context, folderID int64) error {
	if _, err := s.db.ExecContext(ctx,
		`UPDATE messages SET flags = flags | ?, updated_at = ? WHERE folder_id = ? AND (flags & ?) = 0`,
		uint32(FlagSeen), unix(s.nowFunc()), folderID, uint32(FlagSeen)); err != nil {
		return fmt.Errorf("store: mark all read: %w", err)
	}
	return nil
}

// UpdateThreadID points a message at a thread.
func (s *Store) UpdateThreadID(ctx context.Context, messageID, threadID int64) error {
	if _, err := s.db.ExecContext(ctx,
		`UPDATE messages SET thread_id = ? WHERE id = ?`, threadID, messageID); err != nil {
		return fmt.Errorf("store: update thread id %d: %w", messageID, err)
	}
	return nil
}

func scanMessage(sc rowScanner) (Message, error) {
	var (
		m         Message
		date      int64
		flags     int64
		hasAttach int
		created   int64
		updated   int64
	)
	err := sc.Scan(&m.ID, &m.FolderID, &m.UID, &m.MessageIDHeader, &m.InReplyTo, &m.References,
		&m.FromAddress, &m.FromName, &m.ToAddresses, &m.CCAddresses, &m.Subject, &date, &flags,
		&m.SizeBytes, &m.RawMIMEPath, &m.BodyText, &m.BodyHTML, &hasAttach, &m.ThreadID,
		&created, &updated)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Message{}, ErrNotFound
		}
		return Message{}, fmt.Errorf("store: scan message: %w", err)
	}
	m.Date = fromUnix(date)
	m.Flags = Flags(uint32(flags))
	m.HasAttachments = hasAttach != 0
	m.CreatedAt = fromUnix(created)
	m.UpdatedAt = fromUnix(updated)
	return m, nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// AddressList renders a comma-separated address column as a slice.
func AddressList(column string) []string {
	if strings.TrimSpace(column) == "" {
		return nil
	}
	parts := strings.Split(column, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if trimmed := strings.TrimSpace(p); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// AddressColumn joins addresses for storage.
func AddressColumn(addresses []string) string {
	return strings.Join(addresses, ", ")
}
