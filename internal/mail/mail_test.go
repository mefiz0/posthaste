package mail

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// fixtureSeeds returns raw MIME messages covering the shapes the sync engine
// feeds in, including deliberately broken ones. They double as fuzz seeds.
func fixtureSeeds() []string {
	return []string{
		// Simple plain text.
		"From: Ada Lovelace <ada@example.com>\r\n" +
			"To: Bob <bob@example.com>, \"Grace, Hopper\" <grace@example.com>\r\n" +
			"Cc: carol@example.com\r\n" +
			"Bcc: secret@example.com\r\n" +
			"Subject: Greetings\r\n" +
			"Date: Mon, 13 Sep 2026 10:00:00 +0000\r\n" +
			"Message-ID: <msg1@example.com>\r\n" +
			"\r\n" +
			"This is the body.\r\n" +
			"Second line.\r\n",

		// Multipart/alternative with both bodies.
		"From: alt@example.com\r\n" +
			"Subject: Alternative\r\n" +
			"Message-ID: <alt@example.com>\r\n" +
			"MIME-Version: 1.0\r\n" +
			"Content-Type: multipart/alternative; boundary=\"B1\"\r\n" +
			"\r\n" +
			"--B1\r\n" +
			"Content-Type: text/plain; charset=utf-8\r\n" +
			"\r\n" +
			"Plain body.\r\n" +
			"--B1\r\n" +
			"Content-Type: text/html; charset=utf-8\r\n" +
			"\r\n" +
			"<p><b>HTML</b> body.</p>\r\n" +
			"--B1--\r\n",

		// HTML-only message: BodyText must be derived by stripping tags.
		"From: html@example.com\r\n" +
			"Subject: HTML only\r\n" +
			"Message-ID: <html1@example.com>\r\n" +
			"Content-Type: text/html; charset=utf-8\r\n" +
			"\r\n" +
			"<html><head><title>Ignored</title></head><body>" +
			"<p>Hello <b>world</b></p>" +
			"<script>alert(1)</script>" +
			"<p>Bye &amp; bye</p>" +
			"</body></html>\r\n",

		// RFC 2047 encoded words in the subject and the From display name,
		// including a non-UTF-8 charset that needs the charset registry.
		"From: =?iso-8859-1?Q?Andr=E9?= <andre@example.com>\r\n" +
			"To: =?utf-8?B?UsOpc3Vtw6k=?= <resume@example.com>\r\n" +
			"Subject: =?utf-8?B?SGVsbG8gV8O2cmxk?=\r\n" +
			"Message-ID: <enc@example.com>\r\n" +
			"\r\n" +
			"body\r\n",

		// Attachment plus inline cid image.
		"From: att@example.com\r\n" +
			"Subject: Parts\r\n" +
			"Message-ID: <parts@example.com>\r\n" +
			"MIME-Version: 1.0\r\n" +
			"Content-Type: multipart/mixed; boundary=\"OUT\"\r\n" +
			"\r\n" +
			"--OUT\r\n" +
			"Content-Type: text/plain; charset=utf-8\r\n" +
			"\r\n" +
			"See attachment.\r\n" +
			"--OUT\r\n" +
			"Content-Type: multipart/related; boundary=\"IN\"\r\n" +
			"\r\n" +
			"--IN\r\n" +
			"Content-Type: text/html; charset=utf-8\r\n" +
			"\r\n" +
			"<p>Logo: <img src=\"cid:image001@example.com\"></p>\r\n" +
			"--IN\r\n" +
			"Content-Type: image/png; name=\"logo.png\"\r\n" +
			"Content-Transfer-Encoding: base64\r\n" +
			"Content-ID: <image001@example.com>\r\n" +
			"Content-Disposition: inline; filename=\"logo.png\"\r\n" +
			"\r\n" +
			"UE5HREFUQQ==\r\n" +
			"--IN--\r\n" +
			"--OUT\r\n" +
			"Content-Type: application/pdf\r\n" +
			"Content-Transfer-Encoding: base64\r\n" +
			"Content-Disposition: attachment; filename=\"report.pdf\"\r\n" +
			"\r\n" +
			"JVBERi0xLjQ=\r\n" +
			"--OUT--\r\n",

		// Nested multipart: alternative inside mixed.
		"From: nested@example.com\r\n" +
			"Subject: Nested\r\n" +
			"Message-ID: <nested@example.com>\r\n" +
			"MIME-Version: 1.0\r\n" +
			"Content-Type: multipart/mixed; boundary=\"OUTER\"\r\n" +
			"\r\n" +
			"--OUTER\r\n" +
			"Content-Type: multipart/alternative; boundary=\"INNER\"\r\n" +
			"\r\n" +
			"--INNER\r\n" +
			"Content-Type: text/plain; charset=utf-8\r\n" +
			"\r\n" +
			"Deep plain.\r\n" +
			"--INNER\r\n" +
			"Content-Type: text/html; charset=utf-8\r\n" +
			"\r\n" +
			"<p>Deep html</p>\r\n" +
			"--INNER--\r\n" +
			"--OUTER\r\n" +
			"Content-Type: text/csv\r\n" +
			"Content-Disposition: attachment; filename=\"data.csv\"\r\n" +
			"\r\n" +
			"a,b,c\r\n" +
			"--OUTER--\r\n",

		// Embedded message/rfc822 with its own text body and attachment.
		"From: fwd@example.com\r\n" +
			"Subject: Forwarded\r\n" +
			"Message-ID: <fwd@example.com>\r\n" +
			"MIME-Version: 1.0\r\n" +
			"Content-Type: multipart/mixed; boundary=\"FW\"\r\n" +
			"\r\n" +
			"--FW\r\n" +
			"Content-Type: message/rfc822\r\n" +
			"\r\n" +
			"From: original@example.com\r\n" +
			"Subject: Original\r\n" +
			"Message-ID: <original@example.com>\r\n" +
			"Content-Type: multipart/mixed; boundary=\"ORIG\"\r\n" +
			"\r\n" +
			"--ORIG\r\n" +
			"Content-Type: text/plain; charset=utf-8\r\n" +
			"\r\n" +
			"Inner body.\r\n" +
			"--ORIG\r\n" +
			"Content-Type: application/zip\r\n" +
			"Content-Disposition: attachment; filename=\"inner.zip\"\r\n" +
			"\r\n" +
			"ZIPDATA\r\n" +
			"--ORIG--\r\n" +
			"--FW--\r\n",

		// References with duplicates and an In-Reply-To.
		"From: thread@example.com\r\n" +
			"Subject: Re: Thread\r\n" +
			"Message-ID: <reply@example.com>\r\n" +
			"In-Reply-To: <parent@example.com>\r\n" +
			"References: <root@example.com> <parent@example.com> <root@example.com>\r\n" +
			"\r\n" +
			"Reply body.\r\n",

		// Non-UTF-8 body charset.
		"From: latin@example.com\r\n" +
			"Subject: Latin\r\n" +
			"Message-ID: <latin@example.com>\r\n" +
			"Content-Type: text/plain; charset=iso-8859-1\r\n" +
			"\r\n" +
			"caf\xe9 au lait\r\n",

		// RFC 2231 extended filename parameter.
		"From: names@example.com\r\n" +
			"Subject: Names\r\n" +
			"Message-ID: <names@example.com>\r\n" +
			"MIME-Version: 1.0\r\n" +
			"Content-Type: multipart/mixed; boundary=\"NN\"\r\n" +
			"\r\n" +
			"--NN\r\n" +
			"Content-Type: text/plain\r\n" +
			"\r\n" +
			"see attached\r\n" +
			"--NN\r\n" +
			"Content-Type: text/plain\r\n" +
			"Content-Disposition: attachment; filename*=utf-8''caf%C3%A9.txt\r\n" +
			"\r\n" +
			"content\r\n" +
			"--NN--\r\n",

		// Malformed: not MIME at all.
		"this is not a mime message \x00\xff garbage",

		// Malformed: a header line without a colon corrupts the block; fields
		// parsed before it must survive.
		"Subject: kept\r\n" +
			"this line has no colon\r\n" +
			"From: lost@example.com\r\n" +
			"\r\n" +
			"body text\r\n",

		// Truncated multipart: boundary never closes.
		"From: trunc@example.com\r\n" +
			"Subject: Truncated\r\n" +
			"Message-ID: <trunc@example.com>\r\n" +
			"Content-Type: multipart/mixed; boundary=\"TR\"\r\n" +
			"\r\n" +
			"--TR\r\n" +
			"Content-Type: text/plain\r\n" +
			"\r\n" +
			"partial body with no closing boundary",

		// Lowercase header names and no Message-ID or Date.
		"from: lower@example.com\r\n" +
			"subject: lowercase headers\r\n" +
			"\r\n" +
			"body\r\n",
	}
}

// partByContentID finds a part by its cid, or nil.
func partByContentID(p *Parsed, contentID string) *Part {
	for i := range p.Parts {
		if p.Parts[i].ContentID == contentID {
			return &p.Parts[i]
		}
	}
	return nil
}

// partByFilename finds a part by filename, or nil.
func partByFilename(p *Parsed, filename string) *Part {
	for i := range p.Parts {
		if p.Parts[i].Filename == filename {
			return &p.Parts[i]
		}
	}
	return nil
}

func TestParse(t *testing.T) {
	tests := []struct {
		name  string
		raw   string
		check func(t *testing.T, p *Parsed)
	}{
		{
			name: "simple plain text",
			raw:  fixtureSeeds()[0],
			check: func(t *testing.T, p *Parsed) {
				if p.MessageID != "msg1@example.com" {
					t.Errorf("MessageID = %q, want %q", p.MessageID, "msg1@example.com")
				}
				if p.FromName != "Ada Lovelace" || p.FromAddress != "ada@example.com" {
					t.Errorf("From = %q <%q>", p.FromName, p.FromAddress)
				}
				if len(p.To) != 2 {
					t.Fatalf("len(To) = %d, want 2", len(p.To))
				}
				if p.To[0] != (Address{Name: "Bob", Address: "bob@example.com"}) {
					t.Errorf("To[0] = %+v", p.To[0])
				}
				if p.To[1].Name != "Grace, Hopper" || p.To[1].Address != "grace@example.com" {
					t.Errorf("To[1] = %+v, quoted comma must not split the address", p.To[1])
				}
				if len(p.CC) != 1 || p.CC[0].Address != "carol@example.com" {
					t.Errorf("CC = %+v", p.CC)
				}
				if len(p.BCC) != 1 || p.BCC[0].Address != "secret@example.com" {
					t.Errorf("BCC = %+v", p.BCC)
				}
				if p.Subject != "Greetings" {
					t.Errorf("Subject = %q", p.Subject)
				}
				wantDate := time.Date(2026, 9, 13, 10, 0, 0, 0, time.UTC)
				if !p.Date.Equal(wantDate) {
					t.Errorf("Date = %v, want %v", p.Date, wantDate)
				}
				wantBody := "This is the body.\nSecond line."
				if p.BodyText != wantBody {
					t.Errorf("BodyText = %q, want %q", p.BodyText, wantBody)
				}
				if p.BodyHTML != "" {
					t.Errorf("BodyHTML = %q, want empty", p.BodyHTML)
				}
				if len(p.Parts) != 0 {
					t.Errorf("Parts = %+v, want none", p.Parts)
				}
			},
		},
		{
			name: "multipart alternative extracts both bodies",
			raw:  fixtureSeeds()[1],
			check: func(t *testing.T, p *Parsed) {
				if p.BodyText != "Plain body." {
					t.Errorf("BodyText = %q, want %q", p.BodyText, "Plain body.")
				}
				if !strings.Contains(p.BodyHTML, "<b>HTML</b>") {
					t.Errorf("BodyHTML = %q, want the raw HTML part", p.BodyHTML)
				}
			},
		},
		{
			name: "html only message derives text",
			raw:  fixtureSeeds()[2],
			check: func(t *testing.T, p *Parsed) {
				if !strings.Contains(p.BodyHTML, "<script>alert(1)</script>") {
					t.Errorf("BodyHTML = %q, must stay raw and unsanitized", p.BodyHTML)
				}
				want := "Hello world\n\nBye & bye"
				if p.BodyText != want {
					t.Errorf("BodyText = %q, want %q", p.BodyText, want)
				}
			},
		},
		{
			name: "encoded headers decode",
			raw:  fixtureSeeds()[3],
			check: func(t *testing.T, p *Parsed) {
				if p.Subject != "Hello Wörld" {
					t.Errorf("Subject = %q, want %q", p.Subject, "Hello Wörld")
				}
				if p.FromName != "André" || p.FromAddress != "andre@example.com" {
					t.Errorf("From = %q <%q>, want %q <%q>", p.FromName, p.FromAddress, "André", "andre@example.com")
				}
				if len(p.To) != 1 || p.To[0].Name != "Résumé" {
					t.Errorf("To = %+v, want display name %q", p.To, "Résumé")
				}
			},
		},
		{
			name: "attachment and inline cid image",
			raw:  fixtureSeeds()[4],
			check: func(t *testing.T, p *Parsed) {
				if p.BodyText != "See attachment." {
					t.Errorf("BodyText = %q", p.BodyText)
				}
				if !strings.Contains(p.BodyHTML, `src="cid:image001@example.com"`) {
					t.Errorf("BodyHTML = %q, cid reference must be preserved", p.BodyHTML)
				}
				if len(p.Parts) != 2 {
					t.Fatalf("len(Parts) = %d, want 2: %+v", len(p.Parts), p.Parts)
				}
				logo := partByContentID(p, "image001@example.com")
				if logo == nil {
					t.Fatalf("inline image with cid missing: %+v", p.Parts)
				}
				if !logo.IsInline {
					t.Errorf("logo.IsInline = false, want true")
				}
				if logo.Filename != "logo.png" {
					t.Errorf("logo.Filename = %q", logo.Filename)
				}
				if logo.MIMEType != "image/png" {
					t.Errorf("logo.MIMEType = %q", logo.MIMEType)
				}
				if string(logo.Data) != "PNGDATA" {
					t.Errorf("logo.Data = %q, want base64-decoded %q", logo.Data, "PNGDATA")
				}
				report := partByFilename(p, "report.pdf")
				if report == nil {
					t.Fatalf("attachment report.pdf missing: %+v", p.Parts)
				}
				if report.IsInline {
					t.Errorf("report.IsInline = true, want false")
				}
				if report.MIMEType != "application/pdf" {
					t.Errorf("report.MIMEType = %q", report.MIMEType)
				}
				if string(report.Data) != "%PDF-1.4" {
					t.Errorf("report.Data = %q", report.Data)
				}
			},
		},
		{
			name: "nested multipart",
			raw:  fixtureSeeds()[5],
			check: func(t *testing.T, p *Parsed) {
				if p.BodyText != "Deep plain." {
					t.Errorf("BodyText = %q, want %q", p.BodyText, "Deep plain.")
				}
				if !strings.Contains(p.BodyHTML, "Deep html") {
					t.Errorf("BodyHTML = %q", p.BodyHTML)
				}
				csv := partByFilename(p, "data.csv")
				if csv == nil {
					t.Fatalf("attachment data.csv missing: %+v", p.Parts)
				}
				if string(csv.Data) != "a,b,c" {
					t.Errorf("data.csv data = %q", csv.Data)
				}
			},
		},
		{
			name: "embedded message traversed without hijacking headers",
			raw:  fixtureSeeds()[6],
			check: func(t *testing.T, p *Parsed) {
				if p.MessageID != "fwd@example.com" {
					t.Errorf("MessageID = %q, the outer message must win", p.MessageID)
				}
				if p.Subject != "Forwarded" {
					t.Errorf("Subject = %q, the outer subject must win", p.Subject)
				}
				if p.FromAddress != "fwd@example.com" {
					t.Errorf("FromAddress = %q, want the outer sender, not the embedded one", p.FromAddress)
				}
				// The outer message has no text body, so the inner one fills it.
				if p.BodyText != "Inner body." {
					t.Errorf("BodyText = %q, want the embedded body", p.BodyText)
				}
				inner := partByFilename(p, "inner.zip")
				if inner == nil {
					t.Fatalf("embedded attachment inner.zip missing: %+v", p.Parts)
				}
				if string(inner.Data) != "ZIPDATA" {
					t.Errorf("inner.zip data = %q", inner.Data)
				}
			},
		},
		{
			name: "references deduped in order",
			raw:  fixtureSeeds()[7],
			check: func(t *testing.T, p *Parsed) {
				want := []string{"root@example.com", "parent@example.com"}
				if len(p.References) != len(want) {
					t.Fatalf("References = %+v, want %+v", p.References, want)
				}
				for i, id := range want {
					if p.References[i] != id {
						t.Errorf("References[%d] = %q, want %q", i, p.References[i], id)
					}
				}
				if p.InReplyTo != "parent@example.com" {
					t.Errorf("InReplyTo = %q", p.InReplyTo)
				}
			},
		},
		{
			name: "non utf8 body charset decoded",
			raw:  fixtureSeeds()[8],
			check: func(t *testing.T, p *Parsed) {
				if p.BodyText != "café au lait" {
					t.Errorf("BodyText = %q, want %q", p.BodyText, "café au lait")
				}
			},
		},
		{
			name: "rfc 2231 extended filename decoded",
			raw:  fixtureSeeds()[9],
			check: func(t *testing.T, p *Parsed) {
				part := partByFilename(p, "café.txt")
				if part == nil {
					t.Fatalf("attachment with decoded filename missing: %+v", p.Parts)
				}
			},
		},
		{
			name: "garbage input yields best effort result",
			raw:  fixtureSeeds()[10],
			check: func(t *testing.T, p *Parsed) {
				if p.SizeBytes != int64(len(fixtureSeeds()[10])) {
					t.Errorf("SizeBytes = %d, want %d", p.SizeBytes, len(fixtureSeeds()[10]))
				}
			},
		},
		{
			name: "corrupt header line keeps earlier fields",
			raw:  fixtureSeeds()[11],
			check: func(t *testing.T, p *Parsed) {
				if p.Subject != "kept" {
					t.Errorf("Subject = %q, want %q", p.Subject, "kept")
				}
			},
		},
		{
			name: "truncated multipart keeps collected bodies",
			raw:  fixtureSeeds()[12],
			check: func(t *testing.T, p *Parsed) {
				if p.BodyText != "partial body with no closing boundary" {
					t.Errorf("BodyText = %q", p.BodyText)
				}
				if p.Subject != "Truncated" {
					t.Errorf("Subject = %q", p.Subject)
				}
			},
		},
		{
			name: "lowercase headers parsed",
			raw:  fixtureSeeds()[13],
			check: func(t *testing.T, p *Parsed) {
				if p.FromAddress != "lower@example.com" {
					t.Errorf("FromAddress = %q", p.FromAddress)
				}
				if p.Subject != "lowercase headers" {
					t.Errorf("Subject = %q", p.Subject)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := Parse([]byte(tt.raw))
			if err != nil {
				t.Fatalf("Parse returned error %v, best-effort results must not fail", err)
			}
			if p.SizeBytes != int64(len(tt.raw)) {
				t.Errorf("SizeBytes = %d, want %d", p.SizeBytes, len(tt.raw))
			}
			tt.check(t, p)
		})
	}
}

func TestParseEmpty(t *testing.T) {
	if _, err := Parse(nil); !errors.Is(err, ErrEmptyMessage) {
		t.Errorf("Parse(nil) error = %v, want ErrEmptyMessage", err)
	}
	if _, err := Parse([]byte{}); !errors.Is(err, ErrEmptyMessage) {
		t.Errorf("Parse(empty) error = %v, want ErrEmptyMessage", err)
	}
}

func TestParseMissingOptionalHeaders(t *testing.T) {
	p, err := Parse([]byte("Subject: only subject\r\n\r\nbody"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if p.MessageID != "" || p.InReplyTo != "" || p.References != nil {
		t.Errorf("threading fields not empty: %+v", p)
	}
	if !p.Date.IsZero() {
		t.Errorf("Date = %v, want zero", p.Date)
	}
	if p.FromAddress != "" {
		t.Errorf("FromAddress = %q, want empty", p.FromAddress)
	}
}

func TestParseJunkBodyWithoutHeaders(t *testing.T) {
	// A body with no header block at all is treated as plain text.
	p, err := Parse([]byte("just some text\nno headers"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if p.BodyText != "just some text\nno headers" {
		t.Errorf("BodyText = %q", p.BodyText)
	}
}

func TestParseUnparsableAddressKeptVerbatim(t *testing.T) {
	p, err := Parse([]byte("From: MAILER-DAEMON\r\nTo: not an address\r\n\r\nbounce"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if p.FromAddress != "MAILER-DAEMON" {
		t.Errorf("FromAddress = %q, want the raw value kept", p.FromAddress)
	}
	if len(p.To) != 1 || p.To[0].Address != "not an address" {
		t.Errorf("To = %+v, want the raw value kept", p.To)
	}
}

func TestPreview(t *testing.T) {
	tests := []struct {
		name     string
		bodyText string
		maxRunes int
		want     string
	}{
		{name: "empty body", bodyText: "", maxRunes: 20, want: ""},
		{name: "zero limit", bodyText: "hello", maxRunes: 0, want: ""},
		{name: "negative limit", bodyText: "hello", maxRunes: -3, want: ""},
		{name: "skips leading blank lines", bodyText: "\n \n\t\nsecond line first", maxRunes: 30, want: "second line first"},
		{name: "first line wins", bodyText: "first\nsecond", maxRunes: 30, want: "first"},
		{
			name:     "internal whitespace collapsed",
			bodyText: "lots   of   spaces\t  here",
			maxRunes: 40,
			want:     "lots of spaces here",
		},
		{
			name:     "multiline body takes only the first line",
			bodyText: "first\n\n\tindented second",
			maxRunes: 40,
			want:     "first",
		},
		{
			name:     "truncation counts runes and keeps ellipsis within limit",
			bodyText: "héllo wörld",
			maxRunes: 8,
			want:     "héllo w…",
		},
		{name: "exact fit not truncated", bodyText: "12345678", maxRunes: 8, want: "12345678"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Preview(tt.bodyText, tt.maxRunes)
			if got != tt.want {
				t.Errorf("Preview(%q, %d) = %q, want %q", tt.bodyText, tt.maxRunes, got, tt.want)
			}
			if got != "" && len([]rune(got)) > tt.maxRunes {
				t.Errorf("Preview(%q, %d) = %q, exceeds the rune limit", tt.bodyText, tt.maxRunes, got)
			}
		})
	}
}

func TestHTMLToText(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "plain text passes through", in: "no markup", want: "no markup"},
		{
			name: "block tags break lines",
			in:   "<div>a<p>b</p><div>c</div></div>",
			want: "a\n\nb\n\nc",
		},
		{
			name: "table cells join with spaces rows with breaks",
			in:   "<table><tr><td>a</td><td>b</td></tr><tr><td>c</td><td>d</td></tr></table>",
			want: "a b\n\nc d",
		},
		{
			name: "entities decoded and nbsp normalized",
			in:   "<p>Fish&nbsp;&amp; Chips &lt;3</p>",
			want: "Fish & Chips <3",
		},
		{
			name: "script and style content dropped",
			in:   "<style>p{color:red}</style>keep<script>evil()</script>this",
			want: "keepthis",
		},
		{
			name: "comments dropped",
			in:   "a<!-- hidden -->b",
			want: "ab",
		},
		{
			name: "unterminated script drops the rest",
			in:   "text<script>never ends",
			want: "text",
		},
		{
			name: "quoted greater-than inside attributes",
			in:   `<a title="a > b" href="x">link</a>`,
			want: "link",
		},
		{
			name: "case insensitive tags",
			in:   "<P>Upper</P><BR/>next",
			want: "Upper\n\nnext",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := HTMLToText(tt.in); got != tt.want {
				t.Errorf("HTMLToText(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
