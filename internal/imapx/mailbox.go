package imapx

import (
	"context"
	"fmt"
	"strings"
)

// Well-known mailbox roles reported in Mailbox.Role.
const (
	RoleInbox   = "inbox"
	RoleSent    = "sent"
	RoleDrafts  = "drafts"
	RoleArchive = "archive"
	RoleTrash   = "trash"
	RoleJunk    = "junk"
)

// Mailbox describes one mailbox exposed by the server.
type Mailbox struct {
	// Path is the full server-side path, e.g. "INBOX" or "Archive/2026".
	Path string
	// Delimiter is the server hierarchy separator, e.g. "/" or ".". Empty
	// when the server did not report one.
	Delimiter string
	// Attrs are the raw mailbox attributes, e.g. "\\HasChildren".
	Attrs []string
	// Role classifies well-known mailboxes; empty when none applies.
	Role string
}

// List returns every mailbox the server exposes for the account.
func (c *Client) List(ctx context.Context) ([]Mailbox, error) {
	end := c.beginOp(ctx)
	defer end()

	cmd := c.client.List("", "*", nil)
	var mailboxes []Mailbox
	for {
		data := cmd.Next()
		if data == nil {
			break
		}
		mbox := Mailbox{Path: data.Mailbox}
		if data.Delim != 0 {
			mbox.Delimiter = string(data.Delim)
		}
		for _, attr := range data.Attrs {
			mbox.Attrs = append(mbox.Attrs, string(attr))
		}
		mbox.Role = mailboxRole(mbox.Attrs, mbox.Path, mbox.Delimiter)
		mailboxes = append(mailboxes, mbox)
	}
	if err := cmd.Close(); err != nil {
		return nil, fmt.Errorf("imapx: list mailboxes: %w", err)
	}
	return mailboxes, nil
}

// mailboxRole maps special-use attributes to a role. Many servers do not
// implement the SPECIAL-USE extension, so it falls back to a case-insensitive
// match on the mailbox's own name (the part after the last delimiter).
func mailboxRole(attrs []string, path, delim string) string {
	for _, attr := range attrs {
		switch strings.ToLower(attr) {
		case "\\sent":
			return RoleSent
		case "\\drafts":
			return RoleDrafts
		case "\\archive", "\\all":
			// \All (Gmail's "all mail") doubles as the archive destination
			// there, so it maps to the archive role.
			return RoleArchive
		case "\\trash":
			return RoleTrash
		case "\\junk":
			return RoleJunk
		}
	}
	switch strings.ToLower(lastPathElement(path, delim)) {
	case "inbox":
		return RoleInbox
	case "sent":
		return RoleSent
	case "drafts":
		return RoleDrafts
	case "archive":
		return RoleArchive
	case "trash":
		return RoleTrash
	case "junk":
		return RoleJunk
	}
	return ""
}

// lastPathElement returns the part of path after the final delimiter, or the
// whole path when there is no delimiter.
func lastPathElement(path, delim string) string {
	if delim == "" {
		return path
	}
	if i := strings.LastIndex(path, delim); i >= 0 {
		return path[i+len(delim):]
	}
	return path
}

// hierarchyPrefixes returns the mailbox paths to create, in order, so that
// the full path exists: each ancestor prefix followed by the path itself.
// For "Archive/2026" with delimiter "/" that is "Archive" then
// "Archive/2026". Without a known delimiter the whole path is returned as
// the single entry.
func hierarchyPrefixes(path, delim string) []string {
	if delim == "" {
		return []string{path}
	}
	var kept []string
	for _, elem := range strings.Split(path, delim) {
		if elem != "" {
			kept = append(kept, elem)
		}
	}
	if len(kept) == 0 {
		return []string{path}
	}
	prefixes := make([]string, 0, len(kept))
	for i := range kept {
		prefixes = append(prefixes, strings.Join(kept[:i+1], delim))
	}
	return prefixes
}

// hierarchyDelimiter discovers the server's mailbox hierarchy separator by
// probing LIST with an empty mailbox name and falling back to the first
// listed mailbox. An empty result means the server reported no delimiter.
func (c *Client) hierarchyDelimiter(ctx context.Context) (string, error) {
	end := c.beginOp(ctx)
	defer end()

	if delim := probeDelimiter(c); delim != "" {
		return delim, nil
	}

	cmd := c.client.List("", "*", nil)
	delim := ""
	for {
		data := cmd.Next()
		if data == nil {
			break
		}
		if data.Delim != 0 {
			delim = string(data.Delim)
			break
		}
	}
	if err := cmd.Close(); err != nil {
		return "", fmt.Errorf("imapx: probe hierarchy delimiter: %w", err)
	}
	return delim, nil
}

// probeDelimiter sends the classic LIST "" "" delimiter probe and reports the
// separator the server answers with, if any.
func probeDelimiter(c *Client) string {
	cmd := c.client.List("", "", nil)
	defer func() { _ = cmd.Close() }()
	for {
		data := cmd.Next()
		if data == nil {
			return ""
		}
		if data.Delim != 0 {
			return string(data.Delim)
		}
	}
}
