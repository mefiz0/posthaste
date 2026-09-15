package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

const folderColumns = `id, name, imap_path, type, delimiter, uid_validity, uid_next,
	total_count, unread_count, attributes`

// UpsertFolder inserts or updates a folder keyed by its IMAP path, preserving its
// internal ID. It returns the folder ID.
func (s *Store) UpsertFolder(ctx context.Context, f Folder) (int64, error) {
	const q = `
		INSERT INTO folders (name, imap_path, type, delimiter, uid_validity, uid_next,
			total_count, unread_count, attributes)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (imap_path) DO UPDATE SET
			name = excluded.name,
			type = excluded.type,
			delimiter = excluded.delimiter,
			uid_validity = excluded.uid_validity,
			uid_next = excluded.uid_next,
			total_count = excluded.total_count,
			unread_count = excluded.unread_count,
			attributes = excluded.attributes
		RETURNING id`

	var id int64
	err := s.db.QueryRowContext(ctx, q,
		f.Name, f.IMAPPath, string(f.Type), f.Delimiter,
		f.UIDValidity, f.UIDNext, f.TotalCount, f.UnreadCount, f.Attributes,
	).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("store: upsert folder %q: %w", f.IMAPPath, err)
	}
	return id, nil
}

// FolderByID loads one folder.
func (s *Store) FolderByID(ctx context.Context, id int64) (Folder, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+folderColumns+` FROM folders WHERE id = ?`, id)
	return scanFolder(row)
}

// FolderByPath loads one folder by its server-side path.
func (s *Store) FolderByPath(ctx context.Context, imapPath string) (Folder, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+folderColumns+` FROM folders WHERE imap_path = ?`, imapPath)
	return scanFolder(row)
}

// FolderByType loads the first folder of a well-known type (for example the
// account's Sent mailbox for outgoing copies).
func (s *Store) FolderByType(ctx context.Context, t FolderType) (Folder, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+folderColumns+` FROM folders WHERE type = ? ORDER BY id LIMIT 1`, string(t))
	return scanFolder(row)
}

// ListFolders returns every folder ordered by name.
func (s *Store) ListFolders(ctx context.Context) ([]Folder, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+folderColumns+` FROM folders ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("store: list folders: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var folders []Folder
	for rows.Next() {
		f, err := scanFolder(rows)
		if err != nil {
			return nil, err
		}
		folders = append(folders, f)
	}
	return folders, rows.Err()
}

// DeleteFolder removes a folder and, by cascade, its messages.
func (s *Store) DeleteFolder(ctx context.Context, id int64) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM folders WHERE id = ?`, id); err != nil {
		return fmt.Errorf("store: delete folder %d: %w", id, err)
	}
	return nil
}

// UpdateFolderCounts stores the server-reported message and unread totals.
func (s *Store) UpdateFolderCounts(ctx context.Context, id int64, total, unread int) error {
	if _, err := s.db.ExecContext(ctx,
		`UPDATE folders SET total_count = ?, unread_count = ? WHERE id = ?`, total, unread, id); err != nil {
		return fmt.Errorf("store: update folder counts %d: %w", id, err)
	}
	return nil
}

// UpdateFolderUIDs stores the sync cursor for a folder.
func (s *Store) UpdateFolderUIDs(ctx context.Context, id int64, uidValidity, uidNext uint32) error {
	if _, err := s.db.ExecContext(ctx,
		`UPDATE folders SET uid_validity = ?, uid_next = ? WHERE id = ?`,
		uidValidity, uidNext, id); err != nil {
		return fmt.Errorf("store: update folder uids %d: %w", id, err)
	}
	return nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanFolder(sc rowScanner) (Folder, error) {
	var (
		f        Folder
		typ      string
		uidValid int64
		uidNext  int64
	)
	err := sc.Scan(&f.ID, &f.Name, &f.IMAPPath, &typ, &f.Delimiter,
		&uidValid, &uidNext, &f.TotalCount, &f.UnreadCount, &f.Attributes)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Folder{}, ErrNotFound
		}
		return Folder{}, fmt.Errorf("store: scan folder: %w", err)
	}
	f.Type = FolderType(typ)
	f.UIDValidity = uint32(uidValid)
	f.UIDNext = uint32(uidNext)
	return f, nil
}
