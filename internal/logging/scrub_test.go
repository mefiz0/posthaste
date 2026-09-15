package logging

import "testing"

func TestScrubString(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "empty string",
			input: "",
			want:  "",
		},
		{
			name:  "ordinary text untouched",
			input: "folder INBOX has 42 unread messages",
			want:  "folder INBOX has 42 unread messages",
		},
		{
			name:  "plain words and identifiers untouched",
			input: "sync of account acct-7f3c failed on uidvalidity 987654321",
			want:  "sync of account acct-7f3c failed on uidvalidity 987654321",
		},
		{
			name:  "url untouched",
			input: "see https://example.com/docs for help",
			want:  "see https://example.com/docs for help",
		},
		{
			name:  "mention without domain untouched",
			input: "ping @alice about the release",
			want:  "ping @alice about the release",
		},
		{
			name:  "bearer scheme word redacts what follows, conservative trade-off",
			input: "auth scheme bearer was rejected",
			want:  "auth scheme [redacted] rejected",
		},
		{
			name:  "email in longer string",
			input: "sync failed for alice.doe+lists@example.co.uk at 10:00",
			want:  "sync failed for [addr] at 10:00",
		},
		{
			name:  "multiple emails",
			input: "from alice@a.example to bob@other.org",
			want:  "from [addr] to [addr]",
		},
		{
			name:  "bearer token",
			input: "Authorization: Bearer abcDEF123._-~+/=token",
			want:  "Authorization: [redacted]",
		},
		{
			name:  "bearer token case-insensitive",
			input: "BEARER xyza0987654321",
			want:  "[redacted]",
		},
		{
			name:  "jwt-like token",
			input: "got token eyJhbGciOiJIUzI1NiJ9.cGF5bG9hZDEyMzQ1Ng.sig123456789 mid-log",
			want:  "got token [redacted] mid-log",
		},
		{
			name:  "long base64 run",
			input: "blob QUJDREVGR0hJSktMTU5PUFFSU1RVVldYWVphYmNkZWZnaGlqa2xtbm9wcXJzdHV2d3h5eg== kept",
			want:  "blob [redacted] kept",
		},
		{
			name:  "long hex run",
			input: "checksum d41d8cd98f00b204e9800998ecf8427e12345678",
			want:  "checksum [redacted]",
		},
		{
			name:  "short dashed text untouched",
			input: "retry-with-jitter after 30s",
			want:  "retry-with-jitter after 30s",
		},
		{
			name:  "email and token in one string",
			input: "login as user@host.net failed with Bearer eyJhbGciOiJIUzI1NiJ9.cGF5bG9hZDEyMzQ1Ng.sig123456789",
			want:  "login as [addr] failed with [redacted]",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ScrubString(tt.input); got != tt.want {
				t.Errorf("ScrubString(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
