package mail

import (
	"bufio"
	"bytes"
	"io"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/emersion/go-message"
	"github.com/emersion/go-message/mail"
	"github.com/emersion/go-message/textproto"
)

// msgIDPattern matches a single <...> msg-id token inside a raw header value.
var msgIDPattern = regexp.MustCompile(`<[^<>]*>`)

// extractHeaders fills the header-derived fields of p from h. Every field is
// best-effort: a value the strict parser rejects falls back to a lenient scan
// so a message is never dropped from threading or the list just because one
// header is malformed.
func extractHeaders(p *Parsed, h mail.Header) {
	p.MessageID = headerMessageID(h)
	p.InReplyTo = headerInReplyTo(h)
	p.References = dedupe(headerReferences(h))

	if from := parseAddressList(h.Get("From")); len(from) > 0 {
		p.FromName, p.FromAddress = from[0].Name, from[0].Address
	}
	p.To = parseAddressList(h.Get("To"))
	p.CC = parseAddressList(h.Get("Cc"))
	p.BCC = parseAddressList(h.Get("Bcc"))

	// Subject returns the raw undecoded value alongside a decode error, so
	// keep whatever came back rather than dropping the field.
	subject, _ := h.Subject()
	p.Subject = strings.TrimSpace(subject)

	if date, err := h.Date(); err == nil {
		p.Date = date
	}
}

// headerMessageID returns the Message-ID with angle brackets stripped.
func headerMessageID(h mail.Header) string {
	if id, err := h.MessageID(); err == nil && id != "" {
		return id
	}
	ids := extractMsgIDs(h.Get("Message-Id"))
	if len(ids) > 0 {
		return ids[0]
	}
	return ""
}

// headerInReplyTo returns the first referenced parent Message-ID, or empty.
func headerInReplyTo(h mail.Header) string {
	ids, _ := h.MsgIDList("In-Reply-To")
	if len(ids) == 0 {
		ids = extractMsgIDs(h.Get("In-Reply-To"))
	}
	if len(ids) > 0 {
		return ids[0]
	}
	return ""
}

// headerReferences returns the ordered ancestor list from the References
// header. A partial list from the strict parser is kept when it stopped at a
// malformed token; the lenient scan only runs when nothing was recovered.
func headerReferences(h mail.Header) []string {
	refs, _ := h.MsgIDList("References")
	if len(refs) == 0 {
		refs = extractMsgIDs(h.Get("References"))
	}
	return refs
}

// extractMsgIDs scans a raw header value for <...> msg-id tokens. It backs up
// the strict parser, which rejects the whole field on a single bad token.
func extractMsgIDs(value string) []string {
	var ids []string
	for _, match := range msgIDPattern.FindAllString(value, -1) {
		if id := strings.TrimSpace(match[1 : len(match)-1]); id != "" {
			ids = append(ids, id)
		}
	}
	return ids
}

// dedupe removes repeated ids while keeping first-occurrence order; real
// References headers often repeat the immediate parent.
func dedupe(ids []string) []string {
	if len(ids) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

// parseAddressList parses an address header value. A single malformed address
// makes the strict list parser reject everything, so on failure the value is
// split on structural commas and parsed address by address; a remainder that
// still resists parsing is kept verbatim so the sender is never silently lost.
func parseAddressList(value string) []Address {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}

	if parsed, err := mail.ParseAddressList(value); err == nil {
		return convertAddresses(parsed)
	}

	var out []Address
	for _, chunk := range splitAddressList(value) {
		parsed, err := mail.ParseAddress(chunk)
		if err == nil {
			out = append(out, Address{Name: parsed.Name, Address: parsed.Address})
		}
	}
	if len(out) == 0 {
		return []Address{{Address: value}}
	}
	return out
}

// convertAddresses maps go-message addresses onto the package's own type so
// callers never see library types.
func convertAddresses(parsed []*mail.Address) []Address {
	out := make([]Address, 0, len(parsed))
	for _, a := range parsed {
		out = append(out, Address{Name: a.Name, Address: a.Address})
	}
	return out
}

// splitAddressList splits on commas that sit outside quoted strings, angle
// address brackets and comments, so a display name like "Doe, Jane" does not
// split its address in two.
func splitAddressList(value string) []string {
	var parts []string
	var quoted, escaped bool
	angleDepth, commentDepth := 0, 0
	start := 0

	for i := 0; i < len(value); i++ {
		c := value[i]
		switch {
		case escaped:
			escaped = false
		case c == '\\':
			escaped = true
		case c == '"':
			quoted = !quoted
		case quoted:
			// Structural characters inside a quoted display name are literal.
		case c == '<':
			angleDepth++
		case c == '>':
			if angleDepth > 0 {
				angleDepth--
			}
		case c == '(':
			commentDepth++
		case c == ')':
			if commentDepth > 0 {
				commentDepth--
			}
		case angleDepth == 0 && commentDepth == 0 && c == ',':
			parts = append(parts, value[start:i])
			start = i + 1
		}
	}
	parts = append(parts, value[start:])

	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// extractFallbackHeaders salvages header fields when the MIME reader rejects
// the header block outright. textproto keeps every field parsed before the
// first malformed line, which is usually enough to thread and list a message;
// when nothing parsed at all the input most likely had no header block, and
// the whole body is treated as plain text (when it is valid UTF-8).
func extractFallbackHeaders(p *Parsed, raw []byte) {
	br := bufio.NewReader(bytes.NewReader(raw))
	rawHeader, _ := textproto.ReadHeader(br)

	// ReadHeader returns every field parsed before the failure point, so an
	// error here still leaves a usable (possibly empty) header.
	header := mail.Header{Header: message.Header{Header: rawHeader}}

	if rawHeader.Len() == 0 {
		if utf8.Valid(raw) {
			p.BodyText = normalizeText(string(raw))
		}
		return
	}

	extractHeaders(p, header)

	rest, readErr := io.ReadAll(br)
	if readErr != nil && len(rest) == 0 {
		return
	}
	if !utf8.Valid(rest) {
		return
	}
	if mediaType, _ := declaredMediaType(header.Header); normalizeMediaType(mediaType) == "text/plain" {
		p.BodyText = normalizeText(string(rest))
	}
}
