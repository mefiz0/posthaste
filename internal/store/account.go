package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

const accountColumns = `id, email, display_name, imap_host, imap_port, imap_tls, imap_username,
	smtp_host, smtp_port, smtp_tls, smtp_username, auth_method, credential_ref, signature,
	notifications_enabled, poll_interval_seconds, attachment_eager_threshold_bytes,
	is_default, paused, created_at, updated_at`

// SaveAccount inserts or updates the singleton account row in this database.
func (s *Store) SaveAccount(ctx context.Context, a Account) error {
	now := unix(s.nowFunc())
	if a.CreatedAt.IsZero() {
		a.CreatedAt = s.nowFunc()
	}
	if a.PollIntervalSeconds <= 0 {
		a.PollIntervalSeconds = 300
	}
	if a.AttachmentEagerThresholdByte < 0 {
		a.AttachmentEagerThresholdByte = 0
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO accounts (id, email, display_name, imap_host, imap_port, imap_tls, imap_username,
			smtp_host, smtp_port, smtp_tls, smtp_username, auth_method, credential_ref, signature,
			notifications_enabled, poll_interval_seconds, attachment_eager_threshold_bytes,
			is_default, paused, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET
			email = excluded.email,
			display_name = excluded.display_name,
			imap_host = excluded.imap_host,
			imap_port = excluded.imap_port,
			imap_tls = excluded.imap_tls,
			imap_username = excluded.imap_username,
			smtp_host = excluded.smtp_host,
			smtp_port = excluded.smtp_port,
			smtp_tls = excluded.smtp_tls,
			smtp_username = excluded.smtp_username,
			auth_method = excluded.auth_method,
			credential_ref = excluded.credential_ref,
			signature = excluded.signature,
			notifications_enabled = excluded.notifications_enabled,
			poll_interval_seconds = excluded.poll_interval_seconds,
			attachment_eager_threshold_bytes = excluded.attachment_eager_threshold_bytes,
			is_default = excluded.is_default,
			paused = excluded.paused,
			updated_at = excluded.updated_at`,
		a.ID, a.Email, a.DisplayName, a.IMAPHost, a.IMAPPort, a.IMAPTLS, a.IMAPUsername,
		a.SMTPHost, a.SMTPPort, a.SMTPTLS, a.SMTPUsername, a.AuthMethod, a.CredentialRef,
		a.Signature, boolInt(a.NotificationsEnabled), a.PollIntervalSeconds,
		a.AttachmentEagerThresholdByte, boolInt(a.IsDefault), boolInt(a.Paused),
		unix(a.CreatedAt), now)
	if err != nil {
		return fmt.Errorf("store: save account %s: %w", a.ID, err)
	}
	return nil
}

// RekeyAccount rewrites the singleton account row's ID and credential
// reference. It supports migrating an account whose bridge ID was changed (for
// example to a JavaScript-safe value) without losing its stored settings.
func (s *Store) RekeyAccount(ctx context.Context, newID, credentialRef string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE accounts SET id = ?, credential_ref = ?, updated_at = ?`,
		newID, credentialRef, unix(s.nowFunc()))
	if err != nil {
		return fmt.Errorf("store: rekey account: %w", err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return ErrNotFound
	}
	return nil
}

// GetAccount loads the account row, or ErrNotFound if none exists yet.
func (s *Store) GetAccount(ctx context.Context) (Account, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+accountColumns+` FROM accounts ORDER BY id LIMIT 1`)
	return scanAccount(row)
}

// SetAccountPaused flips the paused flag.
func (s *Store) SetAccountPaused(ctx context.Context, paused bool) error {
	if _, err := s.db.ExecContext(ctx,
		`UPDATE accounts SET paused = ?, updated_at = ?`, boolInt(paused), unix(s.nowFunc())); err != nil {
		return fmt.Errorf("store: set account paused: %w", err)
	}
	return nil
}

func scanAccount(sc rowScanner) (Account, error) {
	var (
		a             Account
		notifications int
		isDefault     int
		paused        int
		created       int64
		updated       int64
	)
	err := sc.Scan(&a.ID, &a.Email, &a.DisplayName, &a.IMAPHost, &a.IMAPPort, &a.IMAPTLS,
		&a.IMAPUsername, &a.SMTPHost, &a.SMTPPort, &a.SMTPTLS, &a.SMTPUsername,
		&a.AuthMethod, &a.CredentialRef, &a.Signature, &notifications,
		&a.PollIntervalSeconds, &a.AttachmentEagerThresholdByte, &isDefault, &paused,
		&created, &updated)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Account{}, ErrNotFound
		}
		return Account{}, fmt.Errorf("store: scan account: %w", err)
	}
	a.NotificationsEnabled = notifications != 0
	a.IsDefault = isDefault != 0
	a.Paused = paused != 0
	a.CreatedAt = fromUnix(created)
	a.UpdatedAt = fromUnix(updated)
	return a, nil
}
