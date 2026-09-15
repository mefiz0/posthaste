package thread

import "strings"

// NormalizeSubject reduces a subject to its conversation identity for the
// subject fallback: reply and forward markers ("Re:", "FW:", "Fwd[3]:") are
// stripped repeatedly, whitespace collapses to single spaces, and the result
// is lowercased. The empty result means no usable subject; such messages
// never group by subject.
func NormalizeSubject(subject string) string {
	s := strings.TrimSpace(subject)
	for {
		stripped, ok := stripListMarker(s)
		if !ok {
			break
		}
		s = strings.TrimSpace(stripped)
	}
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return ""
	}
	return strings.ToLower(strings.Join(fields, " "))
}

// stripListMarker removes one leading reply or forward marker — "re:", "fw:",
// or "fwd:", each optionally numbered as in "re[2]:" — case-insensitively.
// The marker must be followed directly by the colon so ordinary words like
// "restart:" are left alone. The second return reports whether a marker was
// stripped.
func stripListMarker(s string) (string, bool) {
	lower := strings.ToLower(s)
	for _, marker := range []string{"re", "fw", "fwd"} {
		if !strings.HasPrefix(lower, marker) {
			continue
		}
		rest := s[len(marker):]
		// An optional counter inside brackets; only digits count, so a
		// mailing-list tag like "[topic]" is not mistaken for one.
		if strings.HasPrefix(rest, "[") {
			end := strings.Index(rest, "]")
			if end < 0 || !isDigits(rest[1:end]) {
				continue
			}
			rest = rest[end+1:]
		}
		if strings.HasPrefix(rest, ":") {
			return rest[1:], true
		}
	}
	return s, false
}

// isDigits reports whether s is one or more ASCII digits.
func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
