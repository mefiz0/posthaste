package app

import (
	"strings"
)

// Notifier posts a native desktop notification. Implementations receive only
// counts, titles, and account display names — never message content, which
// must not escape the engine boundary.
type Notifier interface {
	Notify(title, body string)
}

// noopNotifier is the Notifier used when the shell supplies none, so call
// sites never nil-check.
type noopNotifier struct{}

func (noopNotifier) Notify(string, string) {}

// oauthProviderForHost infers which OAuth2 provider an account talks to, from
// the IMAP host the account was configured with. The provider choice only
// selects token endpoints and scopes, so a host-based mapping covers the
// providers that require OAuth at all.
func oauthProviderForHost(host string) (string, bool) {
	h := strings.ToLower(host)
	switch {
	case strings.Contains(h, "gmail"), strings.Contains(h, "google"):
		return "gmail", true
	case strings.Contains(h, "office365"), strings.Contains(h, "outlook"), strings.Contains(h, "microsoft"):
		return "microsoft365", true
	default:
		return "", false
	}
}

// oauthProviderForEmail infers the OAuth2 provider from an email address
// domain, used when a flow starts before any server settings exist.
func oauthProviderForEmail(email string) (string, bool) {
	_, domain, _ := strings.Cut(email, "@")
	return oauthProviderForHost(domain)
}
