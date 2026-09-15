package imapx

import "testing"

func TestSplitHeader(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "crlf message",
			raw:  "From: a@example.org\r\nSubject: hi\r\n\r\nbody line\r\n",
			want: "From: a@example.org\r\nSubject: hi\r\n\r\n",
		},
		{
			name: "bare lf message",
			raw:  "From: a@example.org\nSubject: hi\n\nbody line\n",
			want: "From: a@example.org\nSubject: hi\n\n",
		},
		{
			name: "headers only, no body",
			raw:  "From: a@example.org\r\nSubject: hi\r\n",
			want: "From: a@example.org\r\nSubject: hi\r\n",
		},
		{
			name: "empty message",
			raw:  "",
			want: "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := string(splitHeader([]byte(tc.raw))); got != tc.want {
				t.Errorf("splitHeader(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}
