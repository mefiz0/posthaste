package mail

import (
	"io"
	"mime"
	"regexp"
	"strings"

	"github.com/emersion/go-message"
	"github.com/emersion/go-message/mail"
)

// walkParts visits every leaf part of a MIME tree in document order,
// multiparts and nested multiparts included, filling the body slots and the
// part list. A structural error in the tree stops the walk but keeps
// everything collected before it.
func walkParts(p *Parsed, entity *message.Entity, depth int) {
	reader := mail.NewReader(entity)
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		// An unknown charset or encoding still yields a usable part; any
		// other error comes back without one and ends the walk.
		if part == nil {
			break
		}
		handleLeaf(p, part, depth)
	}
}

// handleLeaf classifies one non-multipart part as a body slot or a stored
// part, recursing into embedded messages.
func handleLeaf(p *Parsed, part *mail.Part, depth int) {
	header := partHeader(part)
	mediaType, params := declaredMediaType(header)
	mediaType = normalizeMediaType(mediaType)

	// An embedded message is traversed for bodies and parts, but its headers
	// never overwrite the outer message's threading metadata.
	if (mediaType == "message/rfc822" || mediaType == "message/global") && depth < maxEmbeddedDepth {
		embedded, err := io.ReadAll(part.Body)
		if err != nil && len(embedded) == 0 {
			return
		}
		parseInto(p, embedded, depth+1)
		return
	}

	// Part bodies must be drained before the next part is requested; a read
	// error keeps whatever bytes made it through.
	data, err := io.ReadAll(part.Body)
	if err != nil && len(data) == 0 {
		return
	}

	disposition, dispParams := declaredDisposition(header)
	filename := firstNonEmpty(dispParams["filename"], params["name"])
	contentID := trimAngleBrackets(header.Get("Content-Id"))

	// Unnamed, unattached, unreferenced text parts are the message body; the
	// first of each kind wins because alternative bodies arrive in pairs.
	if disposition != "attachment" && filename == "" && contentID == "" {
		switch mediaType {
		case "text/plain":
			if p.BodyText == "" {
				p.BodyText = normalizeText(string(data))
			}
			return
		case "text/html":
			if p.BodyHTML == "" {
				p.BodyHTML = normalizeText(string(data))
			}
			return
		}
	}

	if disposition != "attachment" && filename == "" && contentID == "" {
		// An unknown leaf that is neither named nor addressable cannot be
		// displayed or downloaded, so there is nothing worth recording.
		return
	}

	p.Parts = append(p.Parts, Part{
		Filename:  filename,
		MIMEType:  mediaType,
		ContentID: contentID,
		IsInline:  contentID != "",
		Data:      data,
	})
}

// partHeader recovers the underlying header for either part flavor the reader
// hands out; both wrap the same header type.
func partHeader(part *mail.Part) message.Header {
	switch typed := part.Header.(type) {
	case *mail.InlineHeader:
		return typed.Header
	case *mail.AttachmentHeader:
		return typed.Header
	default:
		// Unreachable with the stock reader, but keep the fields readable
		// rather than panicking should a future flavor appear.
		return message.HeaderFromMap(map[string][]string{
			"Content-Type":        {part.Header.Get("Content-Type")},
			"Content-Disposition": {part.Header.Get("Content-Disposition")},
			"Content-Id":          {part.Header.Get("Content-Id")},
		})
	}
}

// declaredMediaType parses the Content-Type header. go-message decodes RFC
// 2231 continuations and RFC 2047 words in parameters; the manual retry covers
// values go-message refuses outright.
func declaredMediaType(h message.Header) (string, map[string]string) {
	t, params, err := h.ContentType()
	if err == nil {
		return t, params
	}
	if t, params, err = mime.ParseMediaType(h.Get("Content-Type")); err == nil {
		return t, params
	}
	return "", nil
}

// declaredDisposition parses the Content-Disposition header the same way.
func declaredDisposition(h message.Header) (string, map[string]string) {
	disp, params, err := h.ContentDisposition()
	if err == nil {
		return strings.ToLower(disp), params
	}
	if disp, params, err = mime.ParseMediaType(h.Get("Content-Disposition")); err == nil {
		return strings.ToLower(disp), params
	}
	return "", nil
}

// mediaTypePattern validates a bare type/subtype token so a malformed
// Content-Type value cannot leak verbatim into stored metadata.
var mediaTypePattern = regexp.MustCompile(`^[\w!#$%&'.^` + "`" + `{|}~+-]+/[\w!#$%&'.^` + "`" + `{|}~+-]+$`)

// normalizeMediaType validates a declared media type; a missing or malformed
// value falls back to text/plain, the RFC 2045 default outside digests.
func normalizeMediaType(mediaType string) string {
	mediaType = strings.ToLower(strings.TrimSpace(mediaType))
	if mediaTypePattern.MatchString(mediaType) {
		return mediaType
	}
	return "text/plain"
}

// normalizeText makes body text safe to store and display: valid UTF-8 with
// normalized line endings and trimmed transport whitespace. Line unwrapping is
// deliberately not attempted — paragraph reflow is lossy and meaningful line
// breaks are preserved.
func normalizeText(s string) string {
	s = strings.ToValidUTF8(s, "\uFFFD")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return strings.TrimSpace(s)
}

// trimAngleBrackets strips the delimiters around a Content-ID value.
func trimAngleBrackets(v string) string {
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(v, "<")
	v = strings.TrimSuffix(v, ">")
	return strings.TrimSpace(v)
}

// firstNonEmpty returns the first non-empty string, or an empty string.
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
