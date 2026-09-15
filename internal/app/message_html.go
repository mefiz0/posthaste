package app

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/mefiz0/posthaste/internal/mail"
	"github.com/mefiz0/posthaste/internal/sanitize"
	"github.com/mefiz0/posthaste/internal/store"
)

// maxMessageHTMLBytes caps the HTML handed to the webview in one piece. A
// hostile or pathological message cannot blow up the iframe (and the whole
// webview with it) by carrying a gigabyte of markup.
const maxMessageHTMLBytes = 4 << 20

// GetMessageHTML returns the message's HTML body for the reading pane. With
// allowRemote it serves the HTML stored at ingest, whose sanitizer already
// stripped remote references. With the opt-in set it re-reads the on-disk raw
// message source, re-parses it, and re-sanitizes with remote loads allowed —
// the stored, sanitized form is never modified. The result goes to the caller
// only: it is never logged or emitted.
func (s *MailService) GetMessageHTML(ctx context.Context, accountID, messageID int64, allowRemote bool) (string, error) {
	rt, err := s.manager.Runtime(accountID)
	if err != nil {
		return "", err
	}
	message, err := rt.store.MessageByID(ctx, messageID)
	if err != nil {
		return "", err
	}
	if !allowRemote {
		return message.BodyHTML, nil
	}
	rawHTML, err := rawHTMLFor(message)
	if err != nil {
		return "", err
	}
	clean := sanitize.HTML(rawHTML, true)
	if len(clean) > maxMessageHTMLBytes {
		return "", fmt.Errorf("app: message HTML is too large to display (over %d MiB)", maxMessageHTMLBytes>>20)
	}
	return clean, nil
}

// messageHasRemoteContent reports whether the message's raw HTML references
// remote resources, best effort: when the raw source is gone the answer is
// simply no, because then there is nothing the opt-in could load either.
func (s *MailService) messageHasRemoteContent(ctx context.Context, rt *accountRuntime, message store.Message) bool {
	if message.BodyHTML == "" {
		return false
	}
	rawHTML, err := rawHTMLFor(message)
	if err != nil {
		s.manager.logger.Warn("app: detect remote content", "account", rt.ID(), "err", err)
		return false
	}
	return sanitize.HasRemoteContent(rawHTML)
}

// rawHTMLFor loads the message's HTML body from its stored raw MIME source on
// disk. The stored rows keep only the sanitized HTML; the raw source is the
// place any opt-in variant must come from.
func rawHTMLFor(message store.Message) (string, error) {
	if message.RawMIMEPath == "" {
		return "", errors.New("app: the message source is not available locally")
	}
	raw, err := os.ReadFile(message.RawMIMEPath)
	if err != nil {
		return "", fmt.Errorf("app: read message source: %w", err)
	}
	parsed, err := mail.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("app: parse message source: %w", err)
	}
	return parsed.BodyHTML, nil
}
