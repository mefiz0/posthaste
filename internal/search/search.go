// Package search parses the free-text and operator syntax a user types into the
// search box into a structured filter. Free text is the default so search stays
// approachable without learning syntax; operators are optional power-user sugar.
// The zero value of Query means "no filter".
package search

import (
	"strconv"
	"strings"
	"time"
	"unicode"
)

// Query is a structured search filter. A nil slice or pointer field imposes no
// constraint, so the zero value matches everything and free text alone is a
// valid query.
type Query struct {
	// Text is the free-text portion of the query, with terms joined by spaces.
	Text string
	// From restricts results to messages whose sender matches any value.
	From []string
	// To restricts results to messages whose recipient matches any value.
	To []string
	// Subject restricts results to messages whose subject contains any value.
	Subject []string
	// Folder restricts results to a single folder name.
	Folder string
	// HasAttachment, when non-nil, requires (true) or excludes (false)
	// messages carrying attachments.
	HasAttachment *bool
	// IsUnread, when non-nil, requires messages to be read (false) or unread
	// (true).
	IsUnread *bool
	// IsStarred, when non-nil, requires messages to be starred (true) or not
	// (false).
	IsStarred *bool
	// After, when non-nil, is the lower bound on the message date.
	After *time.Time
	// Before, when non-nil, is the upper bound on the message date.
	Before *time.Time
}

// Parse converts input into a Query using the current time to resolve relative
// date terms such as "today" or "7d". It never fails: unrecognized operators and
// malformed date values are kept as free text.
func Parse(input string) Query {
	return ParseWithClock(input, time.Now())
}

// ParseWithClock is Parse with an injected clock so relative date terms are
// deterministic in tests.
func ParseWithClock(input string, now time.Time) Query {
	var q Query
	terms := make([]string, 0)

	for _, tok := range tokenize(input) {
		if tok.quoted {
			terms = append(terms, tok.text)
			continue
		}

		name, value, ok := strings.Cut(tok.text, ":")
		if !ok || value == "" {
			terms = append(terms, tok.text)
			continue
		}

		switch strings.ToLower(name) {
		case "from":
			q.From = append(q.From, value)
		case "to":
			q.To = append(q.To, value)
		case "subject":
			q.Subject = append(q.Subject, value)
		case "has":
			if strings.EqualFold(value, "attachment") {
				q.HasAttachment = boolPtr(true)
			} else {
				terms = append(terms, tok.text)
			}
		case "is":
			switch strings.ToLower(value) {
			case "unread":
				q.IsUnread = boolPtr(true)
			case "read":
				q.IsUnread = boolPtr(false)
			case "starred", "flagged":
				q.IsStarred = boolPtr(true)
			default:
				terms = append(terms, tok.text)
			}
		case "in", "folder":
			q.Folder = value
		case "after":
			if t, ok := parseDate(value, now); ok {
				q.After = &t
			} else {
				terms = append(terms, tok.text)
			}
		case "before":
			if t, ok := parseDate(value, now); ok {
				q.Before = &t
			} else {
				terms = append(terms, tok.text)
			}
		default:
			terms = append(terms, tok.text)
		}
	}

	q.Text = strings.Join(terms, " ")
	return q
}

// token is one whitespace-delimited unit of the input. Quoted reports whether
// the token began with a double quote, which prevents it from being read as an
// operator even when its contents contain a colon.
type token struct {
	text   string
	quoted bool
}

// tokenize splits input on unquoted whitespace. Double quotes group words into a
// single token and are removed from the token text; an unterminated quote runs
// to the end of the input.
func tokenize(input string) []token {
	runes := []rune(input)
	var tokens []token
	for i := 0; i < len(runes); {
		for i < len(runes) && unicode.IsSpace(runes[i]) {
			i++
		}
		if i >= len(runes) {
			break
		}

		startsQuoted := runes[i] == '"'
		var b strings.Builder
		inQuote := false
		for i < len(runes) {
			r := runes[i]
			switch {
			case inQuote:
				if r == '"' {
					inQuote = false
				} else {
					b.WriteRune(r)
				}
			case r == '"':
				inQuote = true
			case unicode.IsSpace(r):
				goto done
			default:
				b.WriteRune(r)
			}
			i++
		}
	done:
		tokens = append(tokens, token{text: b.String(), quoted: startsQuoted})
	}
	return tokens
}

// parseDate resolves a date value against now. It accepts the layouts
// "2006-01-02" and "2006/01/02", the words "today" and "yesterday", and a count
// with a d (days), w (weeks), m (months), or y (years) suffix.
func parseDate(value string, now time.Time) (time.Time, bool) {
	lower := strings.ToLower(value)
	switch lower {
	case "today":
		return startOfDay(now), true
	case "yesterday":
		return startOfDay(now).AddDate(0, 0, -1), true
	}

	for _, layout := range []string{"2006-01-02", "2006/01/02"} {
		if t, err := time.Parse(layout, value); err == nil {
			return t, true
		}
	}

	return parseRelative(lower, now)
}

// parseRelative interprets a count-and-unit value such as "7d" or "2w" as a
// point in the past relative to now.
func parseRelative(value string, now time.Time) (time.Time, bool) {
	if len(value) < 2 {
		return time.Time{}, false
	}
	count, err := strconv.Atoi(value[:len(value)-1])
	if err != nil || count < 0 {
		return time.Time{}, false
	}

	switch value[len(value)-1] {
	case 'd':
		return now.AddDate(0, 0, -count), true
	case 'w':
		return now.AddDate(0, 0, -7*count), true
	case 'm':
		return now.AddDate(0, -count, 0), true
	case 'y':
		return now.AddDate(-count, 0, 0), true
	default:
		return time.Time{}, false
	}
}

// startOfDay returns midnight at the beginning of t's day in its location.
func startOfDay(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}

// boolPtr returns a pointer to v, used for the tri-state filter flags where nil
// means "unconstrained".
func boolPtr(v bool) *bool { return &v }
