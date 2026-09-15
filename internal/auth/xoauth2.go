package auth

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/emersion/go-sasl"
)

// XOAUTH2 is the SASL mechanism name for the legacy OAuth2 exchange. go-sasl
// deliberately ships only OAUTHBEARER, but Microsoft 365 still authenticates
// IMAP and SMTP exclusively with XOAUTH2, so the short, well-documented
// exchange is implemented here rather than by swapping out the dependency.
const XOAUTH2 = "XOAUTH2"

// XOAUTH2Error is the JSON blob a server sends as the SASL challenge when it
// rejects an XOAUTH2 login. The server's real failure is then delivered at the
// protocol level (an IMAP NO response, an SMTP 5xx), after the client answers
// the challenge with an empty response.
type XOAUTH2Error struct {
	Status  string `json:"status"`
	Schemes string `json:"schemes"`
	Scope   string `json:"scope"`
}

// Error makes XOAUTH2Error usable as an error value describing the rejection.
func (e *XOAUTH2Error) Error() string {
	return fmt.Sprintf("auth: XOAUTH2 rejected by server (status %s)", e.Status)
}

// xoauth2Client performs the XOAUTH2 exchange for one login.
type xoauth2Client struct {
	username    string
	accessToken string
	// serverError holds the parsed blob from the first error challenge, so a
	// misbehaving server that sends a second challenge gets the real reason
	// surfaced instead of an opaque unexpected-challenge error.
	serverError error
}

// NewXOAUTH2Client returns a sasl.Client for the XOAUTH2 mechanism. It
// satisfies the same interface as go-sasl's built-in clients, so go-imap/v2
// can drive it directly and go-mail can wire it through its custom auth hook.
// The username is the account's email address; the access token must be live.
func NewXOAUTH2Client(username, accessToken string) sasl.Client {
	return &xoauth2Client{username: username, accessToken: accessToken}
}

// Start returns the mechanism name and the initial response
// "user=<u>\x01auth=Bearer <token>\x01\x01"; the protocol layer base64-encodes
// it, so the raw bytes follow the XOAUTH2 wire format exactly.
func (c *xoauth2Client) Start() (string, []byte, error) {
	if c.username == "" {
		return XOAUTH2, nil, errors.New("auth: XOAUTH2: empty username")
	}
	if c.accessToken == "" {
		return XOAUTH2, nil, errors.New("auth: XOAUTH2: empty access token")
	}
	response := "user=" + c.username + "\x01auth=Bearer " + c.accessToken + "\x01\x01"
	return XOAUTH2, []byte(response), nil
}

// Next answers one server challenge. On the documented error challenge the
// mechanism requires an empty client response so the server can deliver its
// failure at the protocol level; an empty (not nil) slice is returned on
// purpose, because a nil response means SASL cancellation in go-imap. A
// malformed challenge aborts the exchange immediately.
func (c *xoauth2Client) Next(challenge []byte) ([]byte, error) {
	if c.serverError != nil {
		return nil, fmt.Errorf("auth: XOAUTH2: %w", c.serverError)
	}
	var blob XOAUTH2Error
	if err := json.Unmarshal(challenge, &blob); err != nil {
		return nil, fmt.Errorf("auth: XOAUTH2: unexpected server challenge: %w", err)
	}
	c.serverError = &blob
	return []byte{}, nil
}

// SASLMechanism returns the SASL client matching an account's auth method:
// XOAUTH2 (or OAUTHBEARER when the account names it) for OAuth accounts and
// PLAIN for password accounts. The secret is a password for PLAIN and an
// access token for the OAuth mechanisms. isOAuth comes from the setup flow's
// provider decision and supplies the default mechanism when the account label
// is empty; a label that contradicts the credential kind is rejected rather
// than silently downgraded, so a mislabelled account fails loudly at login.
func SASLMechanism(authMethod, username, secret string, isOAuth bool) (sasl.Client, error) {
	if username == "" {
		return nil, errors.New("auth: SASL: empty username")
	}
	if isOAuth {
		switch authMethod {
		case "", AuthMethodXOAUTH2:
			return NewXOAUTH2Client(username, secret), nil
		case AuthMethodOAUTHBEARER:
			return sasl.NewOAuthBearerClient(&sasl.OAuthBearerOptions{
				Username: username,
				Token:    secret,
			}), nil
		default:
			return nil, fmt.Errorf("auth: SASL: auth method %q cannot be used with an OAuth token", authMethod)
		}
	}
	switch authMethod {
	case "", AuthMethodPassword:
		return sasl.NewPlainClient("", username, secret), nil
	default:
		return nil, fmt.Errorf("auth: SASL: auth method %q requires an OAuth token, not a password", authMethod)
	}
}
