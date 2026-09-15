package imapx

import (
	"bytes"
	"context"
	"fmt"
	"time"

	"github.com/emersion/go-imap/v2"
)

// Status is a snapshot of a mailbox's counters and UID space.
type Status struct {
	UIDValidity uint32
	UIDNext     uint32
	Messages    int
	Unseen      int
}

// Select opens the mailbox and returns a status snapshot for it. The mailbox
// stays selected on the connection, but every other operation re-selects what
// it needs, so callers do not need to track selection state.
func (c *Client) Select(ctx context.Context, path string) (Status, error) {
	end := c.beginOp(ctx)
	defer end()

	sel, err := c.client.Select(path, nil).Wait()
	if err != nil {
		return Status{}, fmt.Errorf("imapx: select %s: %w", path, err)
	}
	c.selectedPath = path
	c.selectedReadOnly = false
	status := Status{
		UIDValidity: sel.UIDValidity,
		UIDNext:     uint32(sel.UIDNext),
		Messages:    int(sel.NumMessages),
	}
	// SELECT does not carry an unseen count; ask for it explicitly. Servers
	// answer STATUS on the selected mailbox without complaint.
	data, err := c.client.Status(path, &imap.StatusOptions{NumMessages: true, NumUnseen: true}).Wait()
	if err != nil {
		return Status{}, fmt.Errorf("imapx: status %s: %w", path, err)
	}
	if data.NumUnseen != nil {
		status.Unseen = int(*data.NumUnseen)
	}
	return status, nil
}

// selectForOperation selects the mailbox for a subsequent command. Fetches use
// a read-only selection so the server never lets an implicit flag change
// through; mutations need the read-write form. A SELECT costs a round trip and
// nothing else deselects the mailbox, so the same selection is reused until a
// different path or access mode is needed — which matters a lot on a
// high-latency link.
func (c *Client) selectForOperation(path string, readOnly bool) error {
	if c.selectedPath == path && c.selectedReadOnly == readOnly {
		return nil
	}
	_, err := c.client.Select(path, &imap.SelectOptions{ReadOnly: readOnly}).Wait()
	if err != nil {
		return fmt.Errorf("imapx: select %s: %w", path, err)
	}
	c.selectedPath = path
	c.selectedReadOnly = readOnly
	return nil
}

// FetchOptions controls how much of a message FetchByUID transfers.
type FetchOptions struct {
	// WithBody fetches the complete raw message instead of only its header.
	WithBody bool
	// FlagsOnly fetches UID and flags and nothing else. It is used to
	// reconcile flags and message existence without transferring the headers
	// or bodies of a whole mailbox.
	FlagsOnly bool
}

// Message is one message as stored on the server.
type Message struct {
	UID          uint32
	Flags        []string
	Size         int64
	InternalDate time.Time
	// Header holds the raw RFC 5322 header bytes, including the blank line
	// that terminates them.
	Header []byte
	// Body holds the complete raw message (headers included) when
	// FetchOptions.WithBody was set; otherwise nil.
	Body []byte
}

// FetchByUID fetches messages from path by UID range. Fetching never sets
// \Seen: header-only fetches use BODY.PEEK[HEADER] and full fetches use
// BODY.PEEK[]. A set with a "*" bound (the UID 0) costs one extra UID SEARCH
// round trip, because FETCH responses cannot be matched against wildcard
// sets locally.
func (c *Client) FetchByUID(ctx context.Context, path string, set UIDSet, opts FetchOptions) ([]Message, error) {
	if set.Empty() {
		return nil, fmt.Errorf("imapx: fetch from %s: empty UID set", path)
	}
	end := c.beginOp(ctx)
	defer end()

	if err := c.selectForOperation(path, true); err != nil {
		return nil, err
	}

	if set.Dynamic() {
		largest, err := c.largestUID()
		if err != nil {
			return nil, err
		}
		if largest == 0 {
			// The mailbox has no messages, so no wildcard range can match.
			return []Message{}, nil
		}
		set = set.staticForFetch(largest)
	}

	fetchOpts := &imap.FetchOptions{
		Flags:        true,
		InternalDate: true,
		RFC822Size:   true,
	}
	if opts.FlagsOnly {
		fetchOpts = &imap.FetchOptions{Flags: true}
	} else {
		headerSection := &imap.FetchItemBodySection{Specifier: imap.PartSpecifierHeader, Peek: true}
		if opts.WithBody {
			// A single BODY.PEEK[] fetch carries headers and body together;
			// the header block is split out afterwards to fill both fields.
			fetchOpts.BodySection = []*imap.FetchItemBodySection{{Peek: true}}
		} else {
			fetchOpts.BodySection = []*imap.FetchItemBodySection{headerSection}
		}
	}

	cmd := c.client.Fetch(set.wireSet(), fetchOpts)
	bufs, err := cmd.Collect()
	if err != nil {
		return nil, fmt.Errorf("imapx: fetch from %s (uids %s): %w", path, set, err)
	}

	messages := make([]Message, 0, len(bufs))
	for _, buf := range bufs {
		msg := Message{
			UID:          uint32(buf.UID),
			Size:         buf.RFC822Size,
			InternalDate: buf.InternalDate,
		}
		for _, flag := range buf.Flags {
			msg.Flags = append(msg.Flags, string(flag))
		}
		if !opts.FlagsOnly {
			section := buf.FindBodySection(fetchOpts.BodySection[0])
			if opts.WithBody {
				msg.Body = section
				msg.Header = splitHeader(section)
			} else {
				msg.Header = section
			}
		}
		messages = append(messages, msg)
	}
	return messages, nil
}

// largestUID reports the largest UID in use in the selected mailbox, or 0
// when the mailbox is empty. ESEARCH's RETURN (MAX) is used where advertised,
// with a full UID SEARCH as the fallback for older servers.
func (c *Client) largestUID() (uint32, error) {
	caps, err := c.capabilities()
	if err != nil {
		return 0, err
	}
	if caps.Has(imap.CapESearch) {
		data, err := c.client.UIDSearch(&imap.SearchCriteria{}, &imap.SearchOptions{ReturnMax: true}).Wait()
		if err != nil {
			return 0, fmt.Errorf("imapx: resolve largest uid: %w", err)
		}
		return data.Max, nil
	}
	data, err := c.client.UIDSearch(&imap.SearchCriteria{}, nil).Wait()
	if err != nil {
		return 0, fmt.Errorf("imapx: resolve largest uid: %w", err)
	}
	uids, ok := data.All.(imap.UIDSet)
	if !ok {
		return 0, fmt.Errorf("imapx: resolve largest uid: unexpected search result")
	}
	nums, ok := uids.Nums()
	if !ok {
		return 0, fmt.Errorf("imapx: resolve largest uid: dynamic search result")
	}
	var largest uint32
	for _, uid := range nums {
		if uint32(uid) > largest {
			largest = uint32(uid)
		}
	}
	return largest, nil
}

// splitHeader cuts the raw message into the header block (including the blank
// line that ends it) and the rest, tolerating bare-LF line endings. When no
// header terminator exists the whole input counts as header.
func splitHeader(raw []byte) []byte {
	if i := bytes.Index(raw, []byte("\r\n\r\n")); i >= 0 {
		return raw[:i+4]
	}
	if i := bytes.Index(raw, []byte("\n\n")); i >= 0 {
		return raw[:i+2]
	}
	return raw
}
