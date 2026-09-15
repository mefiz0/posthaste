package mail

import (
	"html"
	"strings"
)

// voidTextTags are elements whose entire content is invisible on a rendered
// page, so their content is dropped when deriving plain text.
var voidTextTags = map[string]struct{}{
	"head":   {},
	"script": {},
	"style":  {},
	"title":  {},
}

// blockTags start and end a visual block, so both their opening and closing
// tags become line breaks in the derived text.
var blockTags = map[string]struct{}{
	"address":    {},
	"article":    {},
	"aside":      {},
	"blockquote": {},
	"center":     {},
	"div":        {},
	"dl":         {},
	"fieldset":   {},
	"figure":     {},
	"figcaption": {},
	"footer":     {},
	"form":       {},
	"h1":         {},
	"h2":         {},
	"h3":         {},
	"h4":         {},
	"h5":         {},
	"h6":         {},
	"header":     {},
	"hr":         {},
	"li":         {},
	"main":       {},
	"nav":        {},
	"ol":         {},
	"p":          {},
	"pre":        {},
	"section":    {},
	"table":      {},
	"tbody":      {},
	"tfoot":      {},
	"thead":      {},
	"tr":         {},
	"ul":         {},
}

// cellTags separate table cells with a space rather than a line break.
var cellTags = map[string]struct{}{
	"td": {},
	"th": {},
}

// HTMLToText reduces HTML markup to plain text as a display fallback for
// HTML-only messages and as the FTS5 indexing source. It is deliberately a
// small structural stripper rather than a rendering engine: element content is
// preserved, active elements (script/style) and invisible scaffolding are
// dropped, block boundaries become line breaks, entities are decoded. The
// result is display/index text only — never rendered as HTML — so imperfect
// reduction is cosmetic, not a security concern.
func HTMLToText(s string) string {
	var b strings.Builder
	lower := strings.ToLower(s)

	for i := 0; i < len(s); {
		if s[i] != '<' {
			b.WriteByte(s[i])
			i++
			continue
		}

		if strings.HasPrefix(lower[i:], "<!--") {
			if end := strings.Index(lower[i:], "-->"); end >= 0 {
				i += end + 3
			} else {
				i = len(s)
			}
			continue
		}

		end := tagEnd(s, i)
		name, closing := tagName(s[i+1 : end])

		if _, skipped := voidTextTags[name]; !closing && skipped {
			// Drop the element's content up to its matching close tag.
			closeTag := "</" + name
			if pos := strings.Index(lower[end:], closeTag); pos >= 0 {
				next := end + pos
				i = tagEnd(s, next) + 1
			} else {
				i = len(s)
			}
			continue
		}

		// Block boundaries become blank lines for readability, br is a soft
		// break, and table cells join with a space on the opening tag only.
		switch {
		case isBlockTag(name):
			b.WriteString("\n\n")
		case !closing && name == "br":
			b.WriteByte('\n')
		case !closing && isCellTag(name):
			b.WriteByte(' ')
		}

		if end == len(s) {
			break
		}
		i = end + 1
	}

	out := html.UnescapeString(b.String())
	out = strings.ReplaceAll(out, "\u00a0", " ")
	return collapseBlankLines(out)
}

// tagEnd returns the index of the '>' closing the tag opened at i, skipping
// over quoted attribute values, or len(s) for an unterminated tag.
func tagEnd(s string, i int) int {
	var quote byte
	for j := i + 1; j < len(s); j++ {
		c := s[j]
		if quote != 0 {
			if c == quote {
				quote = 0
			}
			continue
		}
		switch c {
		case '"', '\'':
			quote = c
		case '>':
			return j
		}
	}
	return len(s)
}

// tagName extracts the lowercased element name from the text between angle
// brackets, reporting whether it is a closing tag.
func tagName(raw string) (name string, closing bool) {
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "/") {
		closing = true
		raw = raw[1:]
	}
	end := 0
	for end < len(raw) {
		c := raw[end]
		isAlnum := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
		if !isAlnum && c != '-' {
			break
		}
		end++
	}
	return strings.ToLower(raw[:end]), closing
}

// isBlockTag reports whether the element starts or ends a visual block.
func isBlockTag(name string) bool {
	_, ok := blockTags[name]
	return ok
}

// isCellTag reports whether the element is a table cell.
func isCellTag(name string) bool {
	_, ok := cellTags[name]
	return ok
}

// collapseBlankLines trims whitespace on each line and squeezes runs of blank
// lines down to a single blank line.
func collapseBlankLines(s string) string {
	var b strings.Builder
	pendingBlank := false
	wrote := false

	for line := range strings.SplitSeq(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			if wrote {
				pendingBlank = true
			}
			continue
		}
		if wrote {
			b.WriteByte('\n')
			if pendingBlank {
				b.WriteByte('\n')
			}
		}
		b.WriteString(line)
		wrote = true
		pendingBlank = false
	}

	return strings.TrimSpace(b.String())
}
