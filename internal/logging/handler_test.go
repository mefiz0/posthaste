package logging

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

// newTestLogger builds a scrubbing handler over a JSON handler writing to buf,
// plus a helper that decodes the last written record.
func newTestLogger(buf *bytes.Buffer) *slog.Logger {
	inner := slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	return slog.New(newScrubbingHandler(inner))
}

// decodeLast parses the final JSON line of buf into a raw key/value map so
// assertions can check presence without losing number/string fidelity.
func decodeLast(t *testing.T, buf *bytes.Buffer) map[string]json.RawMessage {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) == 0 || lines[len(lines)-1] == "" {
		t.Fatalf("no log record written, buffer: %q", buf.String())
	}
	line := lines[len(lines)-1]
	if !json.Valid([]byte(line)) {
		t.Fatalf("last record is not valid JSON: %q", line)
	}
	var record map[string]json.RawMessage
	if err := json.Unmarshal([]byte(line), &record); err != nil {
		t.Fatalf("decode record %q: %v", line, err)
	}
	return record
}

func TestHandlerDropsSensitiveKeys(t *testing.T) {
	tests := []struct {
		name    string
		log     func(l *slog.Logger)
		wantIn  []string
		wantOut []string
	}{
		{
			name: "sensitive keys at top level dropped",
			log: func(l *slog.Logger) {
				l.Info("sync done", "account_id", "acct-1", "subject", "secret plans", "from", "a@b.example")
			},
			wantIn:  []string{"acct-1", "sync done"},
			wantOut: []string{"secret plans", "a@b.example", `"subject"`, `"from"`},
		},
		{
			name: "sensitive keys inside group dropped, group kept",
			log: func(l *slog.Logger) {
				l.Info("stored",
					slog.Group("message",
						slog.String("body", "hidden text"),
						slog.String("snippet", "hidden snip"),
						slog.Int("uid", 7)))
			},
			wantIn:  []string{`"message"`, `"uid":7`},
			wantOut: []string{"hidden text", "hidden snip", `"body"`, `"snippet"`},
		},
		{
			name: "fully sensitive group dropped whole",
			log: func(l *slog.Logger) {
				l.Info("auth",
					slog.Group("credentials",
						slog.String("password", "hunter2"),
						slog.String("token", "t0k3n")),
					"attempt", 2)
			},
			wantIn:  []string{`"attempt":2`},
			wantOut: []string{"hunter2", "t0k3n", `"credentials"`},
		},
		{
			name: "sensitive keys under handler groups dropped",
			log: func(l *slog.Logger) {
				l.WithGroup("sync").Info("finished", "email", "x@y.example", "attempt", 3)
			},
			wantIn:  []string{`"attempt":3`},
			wantOut: []string{"x@y.example", `"email"`},
		},
		{
			name: "sensitive keys nested in group of groups dropped",
			log: func(l *slog.Logger) {
				l.Info("deep",
					slog.Group("outer",
						slog.Group("inner",
							slog.String("body_html", "<b>x</b>"),
							slog.String("note", "kept"))))
			},
			wantIn:  []string{"kept"},
			wantOut: []string{"body_html", "<b>x</b>"},
		},
		{
			name: "sensitive keys attached via With dropped",
			log: func(l *slog.Logger) {
				l.With("subject", "s", "account_id", "acct-9").Info("hello")
			},
			wantIn:  []string{"acct-9", "hello"},
			wantOut: []string{`"subject"`, `"s"`},
		},
		{
			name: "sensitive group attached via With dropped",
			log: func(l *slog.Logger) {
				l.With(slog.Group("credentials", slog.String("secret", "s3cret"))).
					Info("with-group record", "id", 1)
			},
			wantIn:  []string{`"id":1`},
			wantOut: []string{"s3cret", `"credentials"`},
		},
		{
			name: "case-insensitive key match",
			log: func(l *slog.Logger) {
				l.Info("mixed case", "SUBJECT", "s", "FileName", "f.pdf", "kept", "yes")
			},
			wantIn:  []string{"yes"},
			wantOut: []string{"f.pdf", `"FileName"`, `"SUBJECT"`},
		},
		{
			name: "string values redacted, key kept",
			log: func(l *slog.Logger) {
				l.Info("ok", "detail", "reply-to user@host.example arrived")
			},
			wantIn:  []string{`"detail":"reply-to [addr] arrived"`},
			wantOut: []string{"user@host.example"},
		},
		{
			name: "error values redacted",
			log: func(l *slog.Logger) {
				l.Error("send failed", "err", errors.New("reject recipient bob@x.example"))
			},
			wantIn:  []string{`reject recipient [addr]`},
			wantOut: []string{"bob@x.example"},
		},
		{
			name: "message redacted",
			log: func(l *slog.Logger) {
				l.Info("failed for carol@x.example with token eyJhbGciOiJIUzI1NiJ9.cGF5bG9hZDEyMzQ1Ng.sig123456789")
			},
			wantIn:  []string{"failed for [addr] with token [redacted]"},
			wantOut: []string{"carol@x.example", "eyJ"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			tt.log(newTestLogger(&buf))
			record := decodeLast(t, &buf)
			if _, ok := record["msg"]; !ok {
				t.Fatalf("record has no msg field:\n%s", buf.String())
			}
			out := buf.String()
			for _, want := range tt.wantIn {
				if !strings.Contains(out, want) {
					t.Errorf("log output missing %q:\n%s", want, out)
				}
			}
			for _, unwanted := range tt.wantOut {
				if strings.Contains(out, unwanted) {
					t.Errorf("log output leaks %q:\n%s", unwanted, out)
				}
			}
		})
	}
}

func TestHandlerRecordShape(t *testing.T) {
	var buf bytes.Buffer
	logger := newTestLogger(&buf)
	logger.Info("stored message", slog.Group("message", slog.Int("uid", 7)))
	record := decodeLast(t, &buf)

	msg, ok := record["msg"]
	if !ok || string(msg) != `"stored message"` {
		t.Errorf("msg = %s, want %q", msg, `"stored message"`)
	}
	level, ok := record["level"]
	if !ok || string(level) != `"INFO"` {
		t.Errorf("level = %s, want %q", level, `"INFO"`)
	}
	var group map[string]json.RawMessage
	if err := json.Unmarshal(record["message"], &group); err != nil {
		t.Fatalf("decode message group: %v", err)
	}
	if string(group["uid"]) != "7" {
		t.Errorf("message.uid = %s, want 7", group["uid"])
	}
}
