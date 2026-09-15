package imapx

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
)

// Create makes the mailbox path on the server. Creating an existing mailbox
// is not an error: servers are expected to refuse with ALREADYEXISTS and the
// end state is what the caller asked for.
func (c *Client) Create(ctx context.Context, path string) error {
	end := c.beginOp(ctx)
	defer end()
	return c.create(path)
}

// create issues CREATE without its own operation bounds; callers provide them.
func (c *Client) create(path string) error {
	if err := c.client.Create(path, nil).Wait(); err != nil {
		if mailboxExists(err) {
			return nil
		}
		return fmt.Errorf("imapx: create %s: %w", path, err)
	}
	return nil
}

// mailboxExists reports whether err is a CREATE refusal for a mailbox that is
// already there. ALREADYEXISTS is the structured signal; the text check covers
// servers that predate the response code.
func mailboxExists(err error) bool {
	var resp *imap.Error
	if errors.As(err, &resp) && resp.Code == imap.ResponseCodeAlreadyExists {
		return true
	}
	return strings.Contains(strings.ToLower(err.Error()), "already exist")
}

// EnsureFolder creates path and every missing ancestor above it, so callers
// can create nested folders like "Archive/2026" without knowing whether the
// server auto-creates parents.
func (c *Client) EnsureFolder(ctx context.Context, path string) error {
	delim, err := c.hierarchyDelimiter(ctx)
	if err != nil {
		return err
	}

	end := c.beginOp(ctx)
	defer end()
	for _, prefix := range hierarchyPrefixes(path, delim) {
		if err := c.create(prefix); err != nil {
			return err
		}
	}
	return nil
}

// StoreFlags adds and removes message flags on the given UID range. Both
// lists may be set in one call; they are applied as separate STORE commands
// because IMAP cannot mix additions and removals in one.
func (c *Client) StoreFlags(ctx context.Context, path string, set UIDSet, add, remove []string) error {
	if set.Empty() {
		return fmt.Errorf("imapx: store flags on %s: empty UID set", path)
	}
	end := c.beginOp(ctx)
	defer end()

	if err := c.selectForOperation(path, false); err != nil {
		return err
	}
	if len(add) > 0 {
		if err := c.storeFlags(set, imap.StoreFlagsAdd, add); err != nil {
			return err
		}
	}
	if len(remove) > 0 {
		if err := c.storeFlags(set, imap.StoreFlagsDel, remove); err != nil {
			return err
		}
	}
	return nil
}

// storeFlags issues one STORE without its own operation bounds. The silent
// form avoids the server echoing the new flag set back; flag truth comes from
// the sync engine's own fetches.
func (c *Client) storeFlags(set UIDSet, op imap.StoreFlagsOp, flags []string) error {
	imapFlags := make([]imap.Flag, 0, len(flags))
	for _, f := range flags {
		imapFlags = append(imapFlags, imap.Flag(f))
	}
	cmd := c.client.Store(set.wireSet(), &imap.StoreFlags{Op: op, Silent: true, Flags: imapFlags}, nil)
	if _, err := cmd.Collect(); err != nil {
		return fmt.Errorf("imapx: store flags (uids %s): %w", set, err)
	}
	return nil
}

// Move moves messages from path to destPath. It uses UID MOVE where the
// server supports it, with a UID COPY + \Deleted + EXPUNGE fallback otherwise.
// The destination mailbox must already exist.
func (c *Client) Move(ctx context.Context, path string, set UIDSet, destPath string) error {
	if set.Empty() {
		return fmt.Errorf("imapx: move from %s: empty UID set", path)
	}
	end := c.beginOp(ctx)
	defer end()

	if err := c.selectForOperation(path, false); err != nil {
		return err
	}
	// The client picks MOVE vs the COPY fallback from the capability cache,
	// so make sure the cache reflects what the server actually advertises.
	if _, err := c.capabilities(); err != nil {
		return err
	}
	if _, err := c.client.Move(set.wireSet(), destPath).Wait(); err != nil {
		return fmt.Errorf("imapx: move %s to %s (uids %s): %w", path, destPath, set, err)
	}
	return nil
}

// ExpungeUIDs permanently removes the given messages from path. With UIDPLUS
// the removal is limited to exactly those messages; without it the fallback
// EXPUNGE also purges any other message already flagged \Deleted in the
// mailbox, which is the best a non-UIDPLUS server allows.
func (c *Client) ExpungeUIDs(ctx context.Context, path string, set UIDSet) error {
	if set.Empty() {
		return nil
	}
	end := c.beginOp(ctx)
	defer end()

	if err := c.selectForOperation(path, false); err != nil {
		return err
	}
	if err := c.storeFlags(set, imap.StoreFlagsAdd, []string{string(imap.FlagDeleted)}); err != nil {
		return err
	}
	caps, err := c.capabilities()
	if err != nil {
		return err
	}
	var expungeErr error
	if caps.Has(imap.CapUIDPlus) {
		_, expungeErr = c.client.UIDExpunge(set.wireSet()).Collect()
	} else {
		_, expungeErr = c.client.Expunge().Collect()
	}
	if expungeErr != nil {
		return fmt.Errorf("imapx: expunge %s (uids %s): %w", path, set, expungeErr)
	}
	return nil
}

// Append uploads a raw message into path with the given flags and internal
// date (a zero time lets the server choose "now") and returns the UID the
// server assigned. That UID is 0 when the server supports neither UIDPLUS nor
// IMAP4rev2 and therefore does not report the new message's UID.
func (c *Client) Append(ctx context.Context, path string, raw []byte, flags []string, when time.Time) (uint32, error) {
	end := c.beginOp(ctx)
	defer end()

	imapFlags := make([]imap.Flag, 0, len(flags))
	for _, f := range flags {
		imapFlags = append(imapFlags, imap.Flag(f))
	}
	appendOpts := &imap.AppendOptions{Flags: imapFlags}
	if !when.IsZero() {
		appendOpts.Time = when
	}

	cmd := c.client.Append(path, int64(len(raw)), appendOpts)
	if _, err := cmd.Write(raw); err != nil {
		_ = cmd.Close()
		return 0, fmt.Errorf("imapx: append to %s: %w", path, err)
	}
	if err := cmd.Close(); err != nil {
		return 0, fmt.Errorf("imapx: append to %s: %w", path, err)
	}
	data, err := cmd.Wait()
	if err != nil {
		return 0, fmt.Errorf("imapx: append to %s: %w", path, err)
	}
	return uint32(data.UID), nil
}
