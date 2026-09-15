package store

import (
	"context"
	"fmt"
	"strings"
)

// SearchFilter is a parsed, structured search request. The search package
// produces it; the store translates it to an FTS5 query. Pointer booleans mean
// "unset" so the zero value does not accidentally filter.
type SearchFilter struct {
	Text          string
	From          []string
	To            []string
	Subject       []string
	FolderID      int64
	HasAttachment *bool
	IsUnread      *bool
	IsStarred     *bool
	After         int64
	Before        int64
	Limit         int
	Offset        int
}

const prefixedMessageColumns = `m.id, m.folder_id, m.uid, m.message_id_header, m.in_reply_to,
	m.references_header, m.from_address, m.from_name, m.to_addresses, m.cc_addresses,
	m.subject, m.date, m.flags, m.size_bytes, m.raw_mime_path, m.body_text, m.body_html,
	m.has_attachments, m.thread_id, m.created_at, m.updated_at`

// Search runs a structured query against the FTS5 index, ranking by relevance
// (BM25) with recency as the tie-breaker.
func (s *Store) Search(ctx context.Context, f SearchFilter) ([]Message, error) {
	if f.Limit <= 0 {
		f.Limit = 100
	}

	var (
		terms []string
		args  []any
	)
	if text := strings.TrimSpace(f.Text); text != "" {
		terms = append(terms, ftsPhrase(text))
	}
	for _, v := range f.From {
		if v = strings.TrimSpace(v); v != "" {
			terms = append(terms, "{from_address from_name}: "+ftsPhrase(v))
		}
	}
	for _, v := range f.To {
		if v = strings.TrimSpace(v); v != "" {
			terms = append(terms, "{to_addresses cc_addresses}: "+ftsPhrase(v))
		}
	}
	for _, v := range f.Subject {
		if v = strings.TrimSpace(v); v != "" {
			terms = append(terms, "subject: "+ftsPhrase(v))
		}
	}

	query := `SELECT ` + prefixedMessageColumns + `
		FROM messages_fts
		JOIN messages m ON m.id = messages_fts.rowid
		WHERE messages_fts MATCH ?`
	args = append(args, strings.Join(terms, " AND "))

	if f.FolderID > 0 {
		query += ` AND m.folder_id = ?`
		args = append(args, f.FolderID)
	}
	if f.HasAttachment != nil && *f.HasAttachment {
		query += ` AND m.has_attachments = 1`
	}
	if f.IsUnread != nil && *f.IsUnread {
		query += ` AND (m.flags & ?) = 0`
		args = append(args, uint32(FlagSeen))
	}
	if f.IsStarred != nil && *f.IsStarred {
		query += ` AND (m.flags & ?) != 0`
		args = append(args, uint32(FlagFlagged))
	}
	if f.After > 0 {
		query += ` AND m.date >= ?`
		args = append(args, f.After)
	}
	if f.Before > 0 {
		query += ` AND m.date <= ?`
		args = append(args, f.Before)
	}
	query += ` ORDER BY bm25(messages_fts), m.date DESC LIMIT ? OFFSET ?`
	args = append(args, f.Limit, f.Offset)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: search: %w", err)
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

// ftsPhrase wraps a user term as an FTS5 string literal, doubling embedded
// quotes, so query operators typed by the user cannot alter the query shape.
func ftsPhrase(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}
