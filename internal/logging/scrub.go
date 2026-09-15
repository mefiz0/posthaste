package logging

import (
	"log/slog"
	"regexp"
	"strings"
)

// Redaction placeholders. [addr] marks an email address; [redacted] marks a
// token-shaped string. Both are chosen to be obvious in a log excerpt while
// carrying none of the original value.
const (
	redactedAddress = "[addr]"
	redactedToken   = "[redacted]"
)

// sensitiveKeys are attribute names that must never reach the log file in any
// form. Matching is exact and case-insensitive: callers are expected to log
// identifiers (an account ID, a message ID) rather than any of these values,
// so catching the bare names is enough to stop a mistake before it lands on
// disk.
var sensitiveKeys = map[string]struct{}{
	"access_token":  {},
	"address":       {},
	"body":          {},
	"body_html":     {},
	"body_text":     {},
	"cc":            {},
	"credential":    {},
	"email":         {},
	"filename":      {},
	"from":          {},
	"password":      {},
	"preview":       {},
	"refresh_token": {},
	"secret":        {},
	"snippet":       {},
	"subject":       {},
	"to":            {},
	"token":         {},
}

// sensitiveKey reports whether key names a value that must never be logged.
func sensitiveKey(key string) bool {
	_, ok := sensitiveKeys[strings.ToLower(key)]
	return ok
}

// Conservative patterns, applied in order. Each is deliberately narrow so
// ordinary text survives: an address needs a dotted domain, a bearer token
// must follow the "Bearer" scheme word, a JWT must look like one, and the
// generic rule requires forty consecutive token characters — longer than any
// real word or hostname label.
var (
	emailPattern = regexp.MustCompile(
		`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)
	bearerPattern = regexp.MustCompile(
		`(?i)\bbearer\s+[A-Za-z0-9\-._~+/=]+`)
	jwtPattern = regexp.MustCompile(
		`\beyJ[A-Za-z0-9_\-]{8,}\.[A-Za-z0-9_\-]{8,}(?:\.[A-Za-z0-9_\-]+)?`)
	longTokenPattern = regexp.MustCompile(
		`[A-Za-z0-9+/=_\-]{40,}`)
)

// ScrubString redacts address- and token-shaped substrings from s. Capture
// points outside this package (a future panic wrapper, for example) must call
// it so the redaction rules cannot drift between call sites.
func ScrubString(s string) string {
	out := bearerPattern.ReplaceAllString(s, redactedToken)
	out = emailPattern.ReplaceAllString(out, redactedAddress)
	out = jwtPattern.ReplaceAllString(out, redactedToken)
	out = longTokenPattern.ReplaceAllString(out, redactedToken)
	return out
}

// scrubAttr applies the capture rules to one attribute: sensitive keys are
// dropped entirely, groups are filtered recursively and dropped when they
// empty out, and string-shaped values are redacted in place. The second
// return is false when the attribute must not be logged at all.
func scrubAttr(a slog.Attr) (slog.Attr, bool) {
	if sensitiveKey(a.Key) {
		return slog.Attr{}, false
	}
	switch a.Value.Kind() {
	case slog.KindString:
		return slog.String(a.Key, ScrubString(a.Value.String())), true
	case slog.KindGroup:
		attrs := a.Value.Group()
		if len(attrs) == 0 {
			return a, true
		}
		cleaned := make([]slog.Attr, 0, len(attrs))
		for _, child := range attrs {
			if c, ok := scrubAttr(child); ok {
				cleaned = append(cleaned, c)
			}
		}
		if len(cleaned) == 0 {
			return slog.Attr{}, false
		}
		return slog.Attr{Key: a.Key, Value: slog.GroupValue(cleaned...)}, true
	case slog.KindAny:
		// Errors are the one non-string kind worth scrubbing: their text is
		// free-form and routinely embeds addresses and tokens. Replacing the
		// value with a string renders identically in JSON output.
		switch v := a.Value.Any().(type) {
		case string:
			return slog.String(a.Key, ScrubString(v)), true
		case error:
			return slog.String(a.Key, ScrubString(v.Error())), true
		}
	}
	return a, true
}
