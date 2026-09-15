// Package auth owns everything the account setup flow and the sync/send
// workers need to prove an account's identity: OS keyring credential storage,
// SASL mechanism selection (including the in-tree XOAUTH2 client Microsoft 365
// requires), the OAuth2 loopback flow, and ordered provider autodiscovery.
//
// Secrets live only in the OS keyring; the account table stores the
// deterministic CredentialRef lookup key, never the secret itself. Errors
// produced by this package deliberately never contain the account's email
// address, so they can be logged without leaking it.
package auth

// Auth method names as stored in the account table's auth_method column.
const (
	// AuthMethodPassword is classic IMAP/SMTP login via the PLAIN SASL
	// mechanism over TLS. Providers with two-factor auth expect an
	// app-specific password here, not the account password.
	AuthMethodPassword = "password"
	// AuthMethodXOAUTH2 is the legacy OAuth2 SASL mechanism Microsoft 365
	// still requires and that go-sasl deliberately does not ship.
	AuthMethodXOAUTH2 = "xoauth2"
	// AuthMethodOAUTHBEARER is the RFC 7628 mechanism, accepted by Gmail and
	// other providers that follow the current SASL profile.
	AuthMethodOAUTHBEARER = "oauthbearer"
)
