package send

import (
	"context"
	"errors"
	"fmt"
	"net/textproto"
	"strings"
	"testing"
)

func TestPermanentClassification(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"cancelled", context.Canceled, false},
		{"deadline wrapped", fmt.Errorf("send: deliver: %w", context.DeadlineExceeded), false},
		{"typed 5xx rejection", &Rejection{Code: 550, Text: "mailbox unavailable"}, true},
		{"typed 4xx rejection", &Rejection{Code: 451, Text: "try again later"}, false},
		{
			"typed rejection through wrapping",
			fmt.Errorf("outer: %w", &Rejection{Code: 551, Text: "user not local"}),
			true,
		},
		{"invalid message", fmt.Errorf("send: %w: no recipients", ErrInvalidMessage), true},
		{"proto 5xx", &textproto.Error{Code: 550, Msg: "relay denied"}, true},
		{"proto 4xx", &textproto.Error{Code: 454, Msg: "TLS not available"}, false},
		{"connection refused", errors.New("dial tcp: connect: connection refused"), false},
		{"timeout", errors.New("dial tcp: i/o timeout"), false},
		{"text 5xx reply", errors.New("send failed: 550 5.1.1 recipient rejected"), true},
		{"text enhanced 5xx", errors.New("5.7.8 authentication failed"), true},
		{"text enhanced 4xx", errors.New("454 4.7.1 try again later"), false},
		{"text 4xx reply", errors.New("send failed: 451 4.3.0 mail system full"), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Permanent(tc.err); got != tc.want {
				t.Errorf("Permanent(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestRejectionErrorFormat(t *testing.T) {
	tests := []struct {
		name    string
		err     *Rejection
		contain string
	}{
		{"with enhanced code", &Rejection{Code: 550, Enhanced: "5.1.1", Text: "user unknown"}, "smtp 550 5.1.1"},
		{"without enhanced code", &Rejection{Code: 550, Text: "relay denied"}, "smtp 550"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.err.Error(); !strings.Contains(got, tc.contain) {
				t.Errorf("Error() = %q, want it to contain %q", got, tc.contain)
			}
			if !strings.Contains(tc.err.Error(), tc.err.Text) {
				t.Errorf("Error() = %q, want the reply text %q", tc.err.Error(), tc.err.Text)
			}
		})
	}
}

func TestRejectionMatchesErrRejected(t *testing.T) {
	wrapped := fmt.Errorf("send: deliver: %w", &Rejection{Code: 550, Text: "no"})
	if !errors.Is(wrapped, ErrRejected) {
		t.Error("errors.Is(wrapped, ErrRejected) = false, want true through the wrapper")
	}
}

func TestRejectionNilIsSafe(t *testing.T) {
	var rejection *Rejection
	if got := rejection.Error(); got != ErrRejected.Error() {
		t.Errorf("nil rejection Error() = %q, want the sentinel text", got)
	}
}
