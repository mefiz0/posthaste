package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// RecordContact increments the frequency counter and refreshes the display name
// for an address seen in synced mail. Contacts are derived, never user-edited.
func (s *Store) RecordContact(ctx context.Context, email, displayName string) error {
	email = strings.TrimSpace(email)
	if email == "" {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO contacts (email, display_name, last_used_at, frequency_count)
		VALUES (?, ?, ?, 1)
		ON CONFLICT (email) DO UPDATE SET
			display_name = CASE
				WHEN excluded.display_name != '' THEN excluded.display_name
				ELSE contacts.display_name END,
			last_used_at = excluded.last_used_at,
			frequency_count = contacts.frequency_count + 1`,
		strings.ToLower(email), displayName, unix(s.nowFunc()))
	if err != nil {
		return fmt.Errorf("store: record contact: %w", err)
	}
	return nil
}

// SearchContacts returns autocomplete suggestions matching a prefix, ranked by a
// recency-weighted frequency score so frequent, recent correspondents surface
// first without the user maintaining an address book.
func (s *Store) SearchContacts(ctx context.Context, prefix string, limit int) ([]Contact, error) {
	if limit <= 0 {
		limit = 10
	}
	like := strings.ToLower(strings.TrimSpace(prefix)) + "%"
	// score favours frequent and recently-used correspondents; the divisor keeps
	// very old contacts from dominating.
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, email, display_name, last_used_at, frequency_count
		FROM contacts
		WHERE email LIKE ? OR display_name LIKE ?
		ORDER BY (frequency_count * 1000000.0 / (1000000 + last_used_at)) DESC, last_used_at DESC
		LIMIT ?`, like, like, limit)
	if err != nil {
		return nil, fmt.Errorf("store: search contacts: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var contacts []Contact
	for rows.Next() {
		c, err := scanContact(rows)
		if err != nil {
			return nil, err
		}
		contacts = append(contacts, c)
	}
	return contacts, rows.Err()
}

// ListContacts returns all contacts, most useful first.
func (s *Store) ListContacts(ctx context.Context, limit int) ([]Contact, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, email, display_name, last_used_at, frequency_count
		FROM contacts ORDER BY frequency_count DESC, last_used_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("store: list contacts: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var contacts []Contact
	for rows.Next() {
		c, err := scanContact(rows)
		if err != nil {
			return nil, err
		}
		contacts = append(contacts, c)
	}
	return contacts, rows.Err()
}

func scanContact(sc rowScanner) (Contact, error) {
	var (
		c        Contact
		lastUsed int64
	)
	err := sc.Scan(&c.ID, &c.Email, &c.DisplayName, &lastUsed, &c.FrequencyCount)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Contact{}, ErrNotFound
		}
		return Contact{}, fmt.Errorf("store: scan contact: %w", err)
	}
	c.LastUsedAt = fromUnix(lastUsed)
	return c, nil
}
