package send

import (
	"errors"
	"strings"
	"testing"
)

func TestScrubAddresses(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			"bracketed address",
			"550 5.1.1 <bob@recipient.test>: recipient rejected",
			"550 5.1.1 <[addr]>: recipient rejected",
		},
		{
			"bare address",
			"relay denied for bob@recipient.test",
			"relay denied for [addr]",
		},
		{
			"plus addressing",
			"mail to user+tag@mail.example.co.uk bounced",
			"mail to [addr] bounced",
		},
		{"no address", "connection reset by peer", "connection reset by peer"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ScrubAddresses(tc.in); got != tc.want {
				t.Errorf("ScrubAddresses(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestDescribeErrorScrubsAndBounds(t *testing.T) {
	overflow := strings.Repeat("x", 2*maxLastErrorLen)
	err := errors.New("550 5.1.1 <leak@corp.test>: " + overflow)

	got := describeError(err)
	if strings.Contains(got, "leak@corp.test") {
		t.Error("description leaks the address")
	}
	if !strings.Contains(got, redactedAddress) {
		t.Error("description missing the redaction placeholder")
	}
	runes := []rune(got)
	if len(runes) > maxLastErrorLen {
		t.Errorf("description is %d runes, want at most %d", len(runes), maxLastErrorLen)
	}
	if !strings.HasSuffix(got, "…") {
		t.Error("truncated description does not mark the cut")
	}
	if describeError(nil) != "" {
		t.Error("describeError(nil) not empty")
	}
}
