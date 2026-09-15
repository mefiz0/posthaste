package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

const attachmentColumns = `id, message_id, filename, mime_type, size_bytes, content_hash,
	storage_path, content_id, is_inline, fetch_state, last_accessed_at`

// InsertAttachment stores attachment metadata. The bytes are already in the
// content-addressed store by the time this is called.
func (s *Store) InsertAttachment(ctx context.Context, a Attachment) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO attachments (message_id, filename, mime_type, size_bytes, content_hash,
			storage_path, content_id, is_inline, fetch_state, last_accessed_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.MessageID, a.Filename, a.MIMEType, a.SizeBytes, a.ContentHash,
		a.StoragePath, a.ContentID, boolInt(a.IsInline), string(a.FetchState), unix(a.LastAccessedAt))
	if err != nil {
		return 0, fmt.Errorf("store: insert attachment %q: %w", a.Filename, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("store: insert attachment id: %w", err)
	}
	return id, nil
}

// ListAttachments returns the attachments for a message in insertion order.
func (s *Store) ListAttachments(ctx context.Context, messageID int64) ([]Attachment, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+attachmentColumns+` FROM attachments WHERE message_id = ? ORDER BY id`, messageID)
	if err != nil {
		return nil, fmt.Errorf("store: list attachments: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var attachments []Attachment
	for rows.Next() {
		a, err := scanAttachment(rows)
		if err != nil {
			return nil, err
		}
		attachments = append(attachments, a)
	}
	return attachments, rows.Err()
}

// AttachmentByID loads one attachment.
func (s *Store) AttachmentByID(ctx context.Context, id int64) (Attachment, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+attachmentColumns+` FROM attachments WHERE id = ?`, id)
	return scanAttachment(row)
}

// AttachmentByContentID resolves an inline cid: reference for a message.
func (s *Store) AttachmentByContentID(ctx context.Context, messageID int64, contentID string) (Attachment, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+attachmentColumns+` FROM attachments WHERE message_id = ? AND content_id = ? LIMIT 1`,
		messageID, contentID)
	return scanAttachment(row)
}

// MarkAttachmentFetched records that the bytes are now local and touches the
// access time used by cache eviction.
func (s *Store) MarkAttachmentFetched(ctx context.Context, id int64) error {
	if _, err := s.db.ExecContext(ctx,
		`UPDATE attachments SET fetch_state = ?, last_accessed_at = ? WHERE id = ?`,
		string(FetchFetched), unix(s.nowFunc()), id); err != nil {
		return fmt.Errorf("store: mark attachment fetched %d: %w", id, err)
	}
	return nil
}

// TouchAttachment refreshes the LRU timestamp.
func (s *Store) TouchAttachment(ctx context.Context, id int64) error {
	if _, err := s.db.ExecContext(ctx,
		`UPDATE attachments SET last_accessed_at = ? WHERE id = ?`, unix(s.nowFunc()), id); err != nil {
		return fmt.Errorf("store: touch attachment %d: %w", id, err)
	}
	return nil
}

// BlobReferenceCounts returns how many attachment rows reference each content
// hash. It feeds the garbage collector's zero-reference sweep.
func (s *Store) BlobReferenceCounts(ctx context.Context) (map[string]int, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT content_hash, COUNT(*) FROM attachments WHERE content_hash != '' GROUP BY content_hash`)
	if err != nil {
		return nil, fmt.Errorf("store: blob reference counts: %w", err)
	}
	defer func() { _ = rows.Close() }()

	counts := make(map[string]int)
	for rows.Next() {
		var hash string
		var n int
		if err := rows.Scan(&hash, &n); err != nil {
			return nil, fmt.Errorf("store: scan blob ref: %w", err)
		}
		counts[hash] = n
	}
	return counts, rows.Err()
}

// ListEvictableAttachments returns lazily-fetched attachments not accessed since
// cutoff, oldest first, so the cache stays bounded without evicting the small
// eagerly-fetched attachments that form the offline baseline.
func (s *Store) ListEvictableAttachments(ctx context.Context, cutoff int64, limit int) ([]Attachment, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+attachmentColumns+` FROM attachments
		WHERE fetch_state = ? AND last_accessed_at > 0 AND last_accessed_at < ?
		ORDER BY last_accessed_at ASC LIMIT ?`,
		string(FetchFetched), cutoff, limit)
	if err != nil {
		return nil, fmt.Errorf("store: list evictable attachments: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var attachments []Attachment
	for rows.Next() {
		a, err := scanAttachment(rows)
		if err != nil {
			return nil, err
		}
		attachments = append(attachments, a)
	}
	return attachments, rows.Err()
}

func scanAttachment(sc rowScanner) (Attachment, error) {
	var (
		a          Attachment
		isInline   int
		fetchState string
		accessed   int64
	)
	err := sc.Scan(&a.ID, &a.MessageID, &a.Filename, &a.MIMEType, &a.SizeBytes,
		&a.ContentHash, &a.StoragePath, &a.ContentID, &isInline, &fetchState, &accessed)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Attachment{}, ErrNotFound
		}
		return Attachment{}, fmt.Errorf("store: scan attachment: %w", err)
	}
	a.IsInline = isInline != 0
	a.FetchState = FetchState(fetchState)
	a.LastAccessedAt = fromUnix(accessed)
	return a, nil
}
