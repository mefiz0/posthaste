package mail

import "testing"

// FuzzParseMessage drives Parse with arbitrary bytes, asserting the invariants
// that must hold for any hostile input: Parse either returns a result or an
// error (never both, never neither), never panics, and always reports the raw
// input size. Run continuously with:
//
//	go test -run '^$' -fuzz FuzzParseMessage ./internal/mail/
func FuzzParseMessage(f *testing.F) {
	for _, seed := range fixtureSeeds() {
		f.Add([]byte(seed))
	}
	// A few extra adversarial seeds beyond the table fixtures.
	f.Add([]byte{0x00, 0x01, 0x02, 0xff, 0xfe})
	f.Add([]byte("Content-Type: multipart/mixed; boundary=\"\xff\xfe\"\r\n\r\n--\r\n"))
	f.Add([]byte("Content-Type: message/rfc822\r\n\r\n" +
		"Content-Type: message/rfc822\r\n\r\n" +
		"Content-Type: message/rfc822\r\n\r\nbody"))
	f.Add([]byte("Subject: =?bogus-charset?q?unparseable?=\r\n\r\n\x80\x81\x82"))

	f.Fuzz(func(t *testing.T, data []byte) {
		parsed, err := Parse(data)

		if (err == nil) != (parsed != nil) {
			t.Fatalf("Parse returned result=%v error=%v, want exactly one of the two", parsed != nil, err)
		}
		if parsed == nil {
			if !isParseError(err) {
				t.Fatalf("Parse returned unexpected error type: %v", err)
			}
			return
		}
		if parsed.SizeBytes != int64(len(data)) {
			t.Fatalf("SizeBytes = %d, want %d", parsed.SizeBytes, len(data))
		}
		// Exercise the preview path over fuzzed bodies as well.
		_ = Preview(parsed.BodyText, 100)
	})
}

// isParseError reports whether err is one of the errors Parse documents.
func isParseError(err error) bool {
	return err == ErrEmptyMessage
}
