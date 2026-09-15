package auth

import (
	"errors"
	"testing"

	"github.com/emersion/go-sasl"
)

// saslStart is a small helper that returns Start's outputs in a comparable
// form for table assertions.
func saslStart(t *testing.T, client sasl.Client) (string, []byte, error) {
	t.Helper()
	mech, ir, err := client.Start()
	return mech, ir, err
}

func TestXOAUTH2StartWireBytes(t *testing.T) {
	tests := []struct {
		name        string
		username    string
		accessToken string
		wantMech    string
		wantIR      string
		wantErr     bool
	}{
		{
			name:        "exact initial response bytes",
			username:    "user@example.com",
			accessToken: "ya29.token-value",
			wantMech:    "XOAUTH2",
			wantIR:      "user=user@example.com\x01auth=Bearer ya29.token-value\x01\x01",
		},
		{
			name:        "empty username is rejected before any wire traffic",
			username:    "",
			accessToken: "token",
			wantMech:    "XOAUTH2",
			wantErr:     true,
		},
		{
			name:        "empty access token is rejected",
			username:    "user@example.com",
			accessToken: "",
			wantMech:    "XOAUTH2",
			wantErr:     true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mech, ir, err := saslStart(t, NewXOAUTH2Client(tc.username, tc.accessToken))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("Start() = %q, %q, nil error, want error", mech, ir)
				}
				return
			}
			if err != nil {
				t.Fatalf("Start: %v", err)
			}
			if mech != tc.wantMech {
				t.Fatalf("Start mechanism = %q, want %q", mech, tc.wantMech)
			}
			if string(ir) != tc.wantIR {
				t.Fatalf("Start initial response = %q, want %q", ir, tc.wantIR)
			}
		})
	}
}

func TestXOAUTH2ErrorChallenge(t *testing.T) {
	client := NewXOAUTH2Client("user@example.com", "token")
	if _, _, err := client.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// The server's rejection arrives as a base64-decoded JSON blob challenge.
	challenge := []byte(`{"status":"400","schemes":"bearer","scope":"https://mail.google.com/"}`)
	response, err := client.Next(challenge)
	if err != nil {
		t.Fatalf("Next on error challenge: %v", err)
	}
	if response == nil {
		t.Fatal("Next on error challenge must return an empty non-nil response; nil means SASL cancellation in go-imap")
	}
	if len(response) != 0 {
		t.Fatalf("Next on error challenge = %q, want empty response", response)
	}

	// A well-behaved server fails the protocol exchange now. If it sends a
	// second challenge anyway, the parsed server error must surface.
	_, err = client.Next([]byte(`{"status":"401","schemes":"bearer"}`))
	if err == nil {
		t.Fatal("second Next after error challenge: want error")
	}
	var serverErr *XOAUTH2Error
	if !errors.As(err, &serverErr) {
		t.Fatalf("second Next error = %v, want *XOAUTH2Error", err)
	}
	if serverErr.Status != "400" {
		t.Fatalf("surfaced status = %q, want the first challenge's %q", serverErr.Status, "400")
	}
}

func TestXOAUTH2MalformedChallenge(t *testing.T) {
	client := NewXOAUTH2Client("user@example.com", "token")
	if _, _, err := client.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, err := client.Next([]byte("this is not json")); err == nil {
		t.Fatal("Next with malformed challenge: want error")
	}
}

func TestSASLMechanism(t *testing.T) {
	tests := []struct {
		name       string
		authMethod string
		username   string
		secret     string
		isOAuth    bool
		wantMech   string
		wantIR     string
		wantErr    bool
	}{
		{
			name:       "password account selects PLAIN",
			authMethod: AuthMethodPassword,
			username:   "jane@example.org",
			secret:     "app-password",
			isOAuth:    false,
			wantMech:   "PLAIN",
			wantIR:     "\x00jane@example.org\x00app-password",
		},
		{
			name:       "empty method with password falls back to PLAIN",
			authMethod: "",
			username:   "jane@example.org",
			secret:     "app-password",
			isOAuth:    false,
			wantMech:   "PLAIN",
			wantIR:     "\x00jane@example.org\x00app-password",
		},
		{
			name:       "oauth account selects XOAUTH2",
			authMethod: AuthMethodXOAUTH2,
			username:   "jane@gmail.com",
			secret:     "access-token",
			isOAuth:    true,
			wantMech:   "XOAUTH2",
			wantIR:     "user=jane@gmail.com\x01auth=Bearer access-token\x01\x01",
		},
		{
			name:       "oauth account with empty method defaults to XOAUTH2",
			authMethod: "",
			username:   "jane@gmail.com",
			secret:     "access-token",
			isOAuth:    true,
			wantMech:   "XOAUTH2",
			wantIR:     "user=jane@gmail.com\x01auth=Bearer access-token\x01\x01",
		},
		{
			name:       "oauthbearer method uses the go-sasl builtin",
			authMethod: AuthMethodOAUTHBEARER,
			username:   "jane@gmail.com",
			secret:     "access-token",
			isOAuth:    true,
			wantMech:   "OAUTHBEARER",
			wantIR:     "n,a=jane@gmail.com,\x01auth=Bearer access-token\x01\x01",
		},
		{
			name:       "password method with oauth credential is rejected",
			authMethod: AuthMethodPassword,
			username:   "jane@gmail.com",
			secret:     "access-token",
			isOAuth:    true,
			wantErr:    true,
		},
		{
			name:       "oauth method with password credential is rejected",
			authMethod: AuthMethodXOAUTH2,
			username:   "jane@example.org",
			secret:     "app-password",
			isOAuth:    false,
			wantErr:    true,
		},
		{
			name:       "empty username is rejected",
			authMethod: AuthMethodPassword,
			username:   "",
			secret:     "app-password",
			isOAuth:    false,
			wantErr:    true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			client, err := SASLMechanism(tc.authMethod, tc.username, tc.secret, tc.isOAuth)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("SASLMechanism(...) = %v, nil error, want error", client)
				}
				return
			}
			if err != nil {
				t.Fatalf("SASLMechanism: %v", err)
			}
			mech, ir, err := saslStart(t, client)
			if err != nil {
				t.Fatalf("Start: %v", err)
			}
			if mech != tc.wantMech {
				t.Fatalf("mechanism = %q, want %q", mech, tc.wantMech)
			}
			if string(ir) != tc.wantIR {
				t.Fatalf("initial response = %q, want %q", ir, tc.wantIR)
			}
		})
	}
}
