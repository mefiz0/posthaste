// Package mail parses raw RFC 822/MIME message bytes into the derived fields
// stored per message: threading headers, addresses, subject, date, normalized
// body text, the raw HTML body, and attachment/inline parts. Parsing is
// best-effort by design because message content is hostile input: a malformed
// message yields whatever headers and bodies are recoverable instead of
// failing outright, and nothing here can panic on arbitrary bytes.
package mail

import (
	"bytes"
	"errors"
	"strings"
	"time"

	"github.com/emersion/go-message"
	"github.com/emersion/go-message/mail"

	// Registers non-UTF8 charset decoders (ISO-8859-*, windows-*, ...) on the
	// go-message reader so legacy message bodies decode to UTF-8. The blank
	// import is the documented activation mechanism.
	_ "github.com/emersion/go-message/charset"
)

// ErrEmptyMessage is returned when the input carries no bytes at all.
var ErrEmptyMessage = errors.New("mail: empty message")

// maxEmbeddedDepth bounds recursion into message/rfc822 parts so a crafted
// chain of nested messages cannot drive unbounded recursion.
const maxEmbeddedDepth = 5

// Address is a parsed RFC 5322 address with its display name decoded to UTF-8.
type Address struct {
	Name    string
	Address string
}

// Part is one attachment or inline part extracted from the message. Data is
// held in memory; the sync engine moves it into the content-addressed
// attachment store.
type Part struct {
	// Filename is decoded per RFC 2231/2047; empty when the sender gave none.
	Filename string
	// MIMEType is the lowercased media type, e.g. "application/pdf".
	MIMEType string
	// ContentID is the cid without angle brackets, used to resolve inline
	// image references in the HTML body.
	ContentID string
	// IsInline is true for parts referenced by a cid: reference.
	IsInline bool
	Data     []byte
}

// Parsed is the decoded content of a raw message.
type Parsed struct {
	// MessageID is the RFC 5322 Message-ID with angle brackets stripped.
	MessageID string
	// InReplyTo is the referenced parent Message-ID, angle brackets stripped,
	// empty when absent.
	InReplyTo string
	// References is the ordered ancestor list from the References header with
	// brackets stripped and duplicates dropped, first occurrence kept.
	References []string
	// FromName and FromAddress describe the first From address.
	FromName    string
	FromAddress string
	// To, CC and BCC are the parsed recipient lists. Unparsable addresses are
	// kept verbatim in the Address field rather than dropped.
	To, CC, BCC []Address
	// Subject is the RFC 2047-decoded subject line.
	Subject string
	// Date is the parsed Date header, zero when absent or unparsable.
	Date time.Time
	// BodyText is normalized plain text: charset-decoded, UTF-8-safe, with LF
	// line endings. For an HTML-only message it is derived by stripping tags.
	BodyText string
	// BodyHTML is the raw HTML part if present. It is UNSANITIZED here;
	// sanitization happens in internal/sanitize at ingest.
	BodyHTML string
	// Parts holds attachment and inline parts, both referenced by cid: and
	// plain attachments.
	Parts []Part
	// SizeBytes is the size of the raw input, independent of parse success.
	SizeBytes int64
}

// Parse decodes raw message bytes into a Parsed value. It only fails for
// empty input; malformed messages return a best-effort result carrying the
// header fields that were recoverable, and never panic on hostile bytes.
func Parse(raw []byte) (*Parsed, error) {
	if len(raw) == 0 {
		return nil, ErrEmptyMessage
	}

	p := &Parsed{SizeBytes: int64(len(raw))}
	parseInto(p, raw, 0)

	if p.BodyText == "" && p.BodyHTML != "" {
		p.BodyText = HTMLToText(p.BodyHTML)
	}
	return p, nil
}

// parseInto parses one message (or embedded message) into p. Threading and
// address headers are only taken from the outermost message so a forwarded or
// quoted message cannot rewrite the parent's metadata.
func parseInto(p *Parsed, raw []byte, depth int) {
	entity, err := message.Read(bytes.NewReader(raw))
	if err != nil && entity == nil {
		// The header block is too malformed for the MIME reader; salvage
		// whatever header fields parsed before the corruption.
		extractFallbackHeaders(p, raw)
		return
	}

	if depth == 0 {
		extractHeaders(p, mail.Header{Header: entity.Header})
	}
	walkParts(p, entity, depth)
}

// Preview returns the first non-empty line of bodyText with internal
// whitespace collapsed, for message-list previews. The result is at most
// maxRunes runes; a truncation ellipsis counts toward the limit. maxRunes <= 0
// yields an empty string.
func Preview(bodyText string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}

	for line := range strings.SplitSeq(bodyText, "\n") {
		collapsed := strings.Join(strings.Fields(line), " ")
		if collapsed == "" {
			continue
		}
		runes := []rune(collapsed)
		if len(runes) <= maxRunes {
			return collapsed
		}
		return string(runes[:maxRunes-1]) + "…"
	}
	return ""
}
