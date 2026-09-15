package sync

import (
	"strings"
)

// authMarkers are the phrases that identify an authentication failure in a
// dial error: the structured IMAP response codes servers return for rejected
// logins, plus the wording used by the credential source itself (a missing
// keyring entry is an account problem, not a network one).
var authMarkers = []string{
	"authenticationfailed",
	"authfailed",
	"auth",
	"login failed",
	"loginfailed",
	"credential",
	"unauthorized",
	"password",
}

// DefaultIsAuthError reports whether a dial failure looks like an
// authentication problem rather than a connectivity problem, so the worker
// can park for explicit user action instead of retrying forever. It matches
// on the error text because servers report rejected logins in their own
// words; callers with a structured error source can override the heuristic
// through Deps.IsAuthError.
func DefaultIsAuthError(err error) bool {
	if err == nil {
		return false
	}
	flat := flattenErrText(err)
	for _, marker := range authMarkers {
		if strings.Contains(flat, marker) {
			return true
		}
	}
	return false
}

// flattenErrText lowercases an error and collapses whitespace so marker
// matching is insensitive to line wrapping and letter case.
func flattenErrText(err error) string {
	return strings.Join(strings.Fields(strings.ToLower(err.Error())), " ")
}
