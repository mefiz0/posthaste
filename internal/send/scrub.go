package send

import "regexp"

// emailAddressPattern matches the addr-spec of a common email address.
// SMTP rejection transcripts quote recipient addresses, and those addresses
// must never reach a stored row or a notification, so the failure text is
// reduced to a placeholder at the moment the failure is captured.
var emailAddressPattern = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)

// redactedAddress is what an email address in failure text becomes. The
// brackets make a redacted address recognisable to a reader debugging a
// failure without revealing the address itself.
const redactedAddress = "[addr]"

// maxLastErrorLen caps the stored failure description. SMTP transcripts can
// be long and the outbox row is read back into the UI; the first part of a
// reply carries the code and reason, which is everything a user needs.
const maxLastErrorLen = 500

// ScrubAddresses replaces every email address in text with [addr]. It is
// applied to LastError before it is stored or pushed as an event, so there
// is no path where a rejection transcript leaks a recipient address.
func ScrubAddresses(text string) string {
	return emailAddressPattern.ReplaceAllString(text, redactedAddress)
}

// describeError renders a delivery failure for storage and notification:
// scrubbed of addresses and bounded in length.
func describeError(err error) string {
	if err == nil {
		return ""
	}
	return truncate(ScrubAddresses(err.Error()), maxLastErrorLen)
}

// truncate cuts text to at most limit runes, marking the cut with an
// ellipsis so a truncated transcript is visibly incomplete.
func truncate(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	const ellipsis = "…"
	kept := runes[:limit-len(ellipsis)]
	return string(kept) + ellipsis
}
