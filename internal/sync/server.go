package sync

import (
	"context"
	"fmt"
	"time"

	"github.com/emersion/go-sasl"

	"github.com/mefiz0/posthaste/internal/imapx"
	"github.com/mefiz0/posthaste/internal/store"
)

// ServerFolder is one mailbox as the server reports it.
type ServerFolder struct {
	// Path is the full server-side mailbox path, e.g. "INBOX" or "Archive/2026".
	Path string
	// Delimiter is the server hierarchy separator, empty when unreported.
	Delimiter string
	// Role classifies well-known mailboxes ("inbox", "sent", ...); empty for
	// user folders.
	Role string
}

// FolderStatus is the counter snapshot from selecting a folder.
type FolderStatus struct {
	UIDValidity uint32
	UIDNext     uint32
	Total       int
	Unseen      int
}

// ServerMessage is one message as stored on the server. Header carries the
// raw header block; Body carries the complete raw message when the fetch
// asked for bodies, otherwise nil.
type ServerMessage struct {
	UID   uint32
	Flags []string
	Size  int64
	Date  time.Time
	// Header holds the raw header bytes including the terminating blank line.
	Header []byte
	Body   []byte
}

// MailServer is the server surface the sync worker needs. It is defined here,
// by its consumer, narrowly over what the sync algorithms use, so tests
// substitute a plain fake and the worker never imports the IMAP layer
// directly. Every method must be safe to call only from the worker's own
// goroutine: a server represents one connection.
type MailServer interface {
	// ListFolders reports every mailbox the server exposes.
	ListFolders(ctx context.Context) ([]ServerFolder, error)
	// Select opens a folder and returns its counter snapshot.
	Select(ctx context.Context, path string) (FolderStatus, error)
	// FetchHeaders returns the messages with UID >= fromUID, headers only.
	// Reading never sets \Seen.
	FetchHeaders(ctx context.Context, path string, fromUID uint32) ([]ServerMessage, error)
	// FetchFlags returns UID and flags for the messages with UID >= fromUID,
	// without transferring headers or bodies. It is what flag and existence
	// reconciliation uses.
	FetchFlags(ctx context.Context, path string, fromUID uint32) ([]ServerMessage, error)
	// FetchBodies returns the full raw messages for the given UIDs, headers
	// included in Body. Reading never sets \Seen.
	FetchBodies(ctx context.Context, path string, uids []uint32) ([]ServerMessage, error)
	// SetFlags adds and removes flags on one message.
	SetFlags(ctx context.Context, path string, uid uint32, add, remove []string) error
	// Move moves one message to another folder.
	Move(ctx context.Context, path string, uid uint32, destPath string) error
	// Delete permanently removes one message.
	Delete(ctx context.Context, path string, uid uint32) error
	// Append uploads a raw message into a folder with the given flags.
	Append(ctx context.Context, path string, raw []byte, flags []string) error
	// SupportsIdle reports whether the server advertises the IDLE extension.
	SupportsIdle(ctx context.Context) (bool, error)
	// Idle parks the connection until the server reports a change (nil), the
	// context ends, or the connection dies (error).
	Idle(ctx context.Context, path string) error
	// Close tears the connection down. Best effort.
	Close() error
}

// ServerFactory opens a fresh authenticated connection. Every call must
// return an independent connection; the worker reconnects by calling it
// again.
type ServerFactory func(ctx context.Context) (MailServer, error)

// saslAuth adapts a ready sasl.Client to the authenticator interface the IMAP
// layer expects.
type saslAuth struct{ client sasl.Client }

// Mechanism returns the underlying SASL client.
func (a saslAuth) Mechanism() sasl.Client { return a.client }

// DialFactory returns the production ServerFactory for an account: each call
// fetches a fresh SASL mechanism from authFunc (so rotated credentials and
// refreshed OAuth tokens are used on reconnect) and dials with the IMAP
// layer. Errors are wrapped with the account host only, never the username.
func DialFactory(account store.Account, authFunc func(ctx context.Context) (sasl.Client, error)) ServerFactory {
	return func(ctx context.Context) (MailServer, error) {
		if authFunc == nil {
			return nil, fmt.Errorf("sync: no credential source configured")
		}
		mechanism, err := authFunc(ctx)
		if err != nil {
			return nil, fmt.Errorf("sync: fetch credentials: %w", err)
		}
		client, err := imapx.Dial(ctx, imapx.Config{
			Host:     account.IMAPHost,
			Port:     account.IMAPPort,
			TLS:      imapx.TLSMode(account.IMAPTLS),
			Username: account.IMAPUsername,
			Auth:     saslAuth{client: mechanism},
		})
		if err != nil {
			return nil, fmt.Errorf("sync: dial %s: %w", account.IMAPHost, err)
		}
		return &imapServer{client: client}, nil
	}
}

// imapServer adapts an IMAP client to the MailServer interface. It exists so
// the worker logic stays independent of the IMAP library's types.
type imapServer struct {
	client *imapx.Client
}

// ListFolders reports every mailbox with its special-use role.
func (s *imapServer) ListFolders(ctx context.Context) ([]ServerFolder, error) {
	mailboxes, err := s.client.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("sync: list folders: %w", err)
	}
	out := make([]ServerFolder, 0, len(mailboxes))
	for _, box := range mailboxes {
		out = append(out, ServerFolder{
			Path:      box.Path,
			Delimiter: box.Delimiter,
			Role:      box.Role,
		})
	}
	return out, nil
}

// Select opens a folder and returns its counter snapshot.
func (s *imapServer) Select(ctx context.Context, path string) (FolderStatus, error) {
	status, err := s.client.Select(ctx, path)
	if err != nil {
		return FolderStatus{}, fmt.Errorf("sync: select folder %s: %w", path, err)
	}
	return FolderStatus{
		UIDValidity: status.UIDValidity,
		UIDNext:     status.UIDNext,
		Total:       status.Messages,
		Unseen:      status.Unseen,
	}, nil
}

// FetchHeaders returns every message with UID >= fromUID, headers only. The
// wildcard range bound is resolved by the IMAP layer; an empty folder yields
// no messages.
func (s *imapServer) FetchHeaders(ctx context.Context, path string, fromUID uint32) ([]ServerMessage, error) {
	if fromUID < 1 {
		fromUID = 1
	}
	messages, err := s.client.FetchByUID(ctx, path, imapx.UIDRangeSet(fromUID, 0), imapx.FetchOptions{})
	if err != nil {
		return nil, fmt.Errorf("sync: fetch headers from %s: %w", path, err)
	}
	return convertMessages(messages), nil
}

// FetchFlags returns UID and flags for every message with UID >= fromUID,
// without headers or bodies.
func (s *imapServer) FetchFlags(ctx context.Context, path string, fromUID uint32) ([]ServerMessage, error) {
	if fromUID < 1 {
		fromUID = 1
	}
	messages, err := s.client.FetchByUID(ctx, path, imapx.UIDRangeSet(fromUID, 0), imapx.FetchOptions{FlagsOnly: true})
	if err != nil {
		return nil, fmt.Errorf("sync: fetch flags from %s: %w", path, err)
	}
	return convertMessages(messages), nil
}

// FetchBodies returns the full raw messages for the given UIDs in one fetch.
func (s *imapServer) FetchBodies(ctx context.Context, path string, uids []uint32) ([]ServerMessage, error) {
	if len(uids) == 0 {
		return nil, nil
	}
	var set imapx.UIDSet
	for _, uid := range uids {
		set.Add(uid)
	}
	messages, err := s.client.FetchByUID(ctx, path, set, imapx.FetchOptions{WithBody: true})
	if err != nil {
		return nil, fmt.Errorf("sync: fetch bodies from %s: %w", path, err)
	}
	return convertMessages(messages), nil
}

// SetFlags adds and removes flags on one message.
func (s *imapServer) SetFlags(ctx context.Context, path string, uid uint32, add, remove []string) error {
	if len(add) == 0 && len(remove) == 0 {
		return nil
	}
	if err := s.client.StoreFlags(ctx, path, imapx.UIDSingle(uid), add, remove); err != nil {
		return fmt.Errorf("sync: set flags on %s: %w", path, err)
	}
	return nil
}

// Move moves one message to another folder.
func (s *imapServer) Move(ctx context.Context, path string, uid uint32, destPath string) error {
	if err := s.client.Move(ctx, path, imapx.UIDSingle(uid), destPath); err != nil {
		return fmt.Errorf("sync: move from %s to %s: %w", path, destPath, err)
	}
	return nil
}

// Delete permanently removes one message.
func (s *imapServer) Delete(ctx context.Context, path string, uid uint32) error {
	if err := s.client.ExpungeUIDs(ctx, path, imapx.UIDSingle(uid)); err != nil {
		return fmt.Errorf("sync: delete from %s: %w", path, err)
	}
	return nil
}

// Append uploads a raw message into a folder; the server stamps the
// internal date.
func (s *imapServer) Append(ctx context.Context, path string, raw []byte, flags []string) error {
	_, err := s.client.Append(ctx, path, raw, flags, time.Time{})
	if err != nil {
		return fmt.Errorf("sync: append to %s: %w", path, err)
	}
	return nil
}

// SupportsIdle reports whether the server advertises the IDLE extension.
func (s *imapServer) SupportsIdle(ctx context.Context) (bool, error) {
	supported, err := s.client.SupportsIdle(ctx)
	if err != nil {
		return false, fmt.Errorf("sync: probe idle support: %w", err)
	}
	return supported, nil
}

// Idle parks the connection until the server reports a change, the context
// ends, or the connection dies.
func (s *imapServer) Idle(ctx context.Context, path string) error {
	if err := s.client.Idle(ctx, path); err != nil {
		return fmt.Errorf("sync: idle on %s: %w", path, err)
	}
	return nil
}

// Close tears the connection down.
func (s *imapServer) Close() error {
	// Logout is best effort by design in the IMAP layer.
	return s.client.Close(context.WithoutCancel(context.Background()))
}

// convertMessages maps fetched messages onto the sync package's type.
func convertMessages(messages []imapx.Message) []ServerMessage {
	out := make([]ServerMessage, 0, len(messages))
	for _, m := range messages {
		out = append(out, ServerMessage{
			UID:    m.UID,
			Flags:  m.Flags,
			Size:   m.Size,
			Date:   m.InternalDate,
			Header: m.Header,
			Body:   m.Body,
		})
	}
	return out
}
