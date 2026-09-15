package send

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/mail"
	"net/textproto"
	"strings"
	"testing"

	"github.com/mefiz0/posthaste/internal/attachment"
	"github.com/mefiz0/posthaste/internal/auth"
	"github.com/mefiz0/posthaste/internal/store"

	gomailsmtp "github.com/wneessen/go-mail/smtp"
)

// sampleOutbox is a fully addressable row for composition tests.
func sampleOutbox() store.OutboxMessage {
	return store.OutboxMessage{
		ID:           NewOutboxID(),
		FromAddress:  "ada@sender.test",
		FromName:     "Ada Lovelace",
		ToAddresses:  SerializeAddresses([]mail.Address{addr("Grace Hopper", "grace@other.test")}),
		CCAddresses:  SerializeAddresses([]mail.Address{addr("Copy", "copy@third.test")}),
		BCCAddresses: SerializeAddresses([]mail.Address{addr("Quiet", "quiet@fourth.test")}),
		Subject:      "Reports",
		BodyText:     "Hello there",
		BodyHTML:     "<p>Hello there</p>",
		InReplyTo:    "<original@other.test>",
		References:   "<first@other.test> <original@other.test>",
	}
}

// fakeBlobs serves fixed content per hash and rejects everything else.
type fakeBlobs struct {
	content map[string]string
}

func (f fakeBlobs) Open(ctx context.Context, hash string) (io.ReadCloser, error) {
	content, ok := f.content[hash]
	if !ok {
		return nil, fmt.Errorf("attachment: %s: %w", hash, attachment.ErrNotFound)
	}
	return io.NopCloser(strings.NewReader(content)), nil
}

func TestGoMailerComposeRendersMessage(t *testing.T) {
	mailer := NewMailer(TransportConfig{Host: "smtp.test", Port: 587, TLS: TLSStartTLS}, nil)
	message, closers, err := mailer.compose(context.Background(), sampleOutbox())
	if err != nil {
		t.Fatalf("compose: %v", err)
	}
	defer closeAll(closers)

	var rendered bytes.Buffer
	if _, err := message.WriteTo(&rendered); err != nil {
		t.Fatalf("WriteTo: %v", err)
	}
	output := rendered.String()

	// go-mail quotes display names in address headers.
	for _, want := range []string{
		`From: "Ada Lovelace" <ada@sender.test>`,
		`"Grace Hopper" <grace@other.test>`,
		`"Copy" <copy@third.test>`,
		"Subject: Reports",
		"In-Reply-To: <original@other.test>",
		"<first@other.test> <original@other.test>",
		"Hello there",
	} {
		if !strings.Contains(output, want) {
			t.Errorf("rendered message misses %q\n%s", want, output)
		}
	}
	// Bcc recipients are envelope-only: they must never be visible in the
	// message body other clients store and forward.
	if strings.Contains(output, "quiet@fourth.test") || strings.Contains(output, "Quiet") {
		t.Errorf("rendered message exposes the Bcc recipient:\n%s", output)
	}
	if !strings.Contains(output, "text/html") {
		t.Error("rendered message misses the HTML alternative part")
	}
}

func TestGoMailerComposeValidation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*store.OutboxMessage)
	}{
		{"empty sender", func(m *store.OutboxMessage) { m.FromAddress = "" }},
		{"no recipients", func(m *store.OutboxMessage) {
			m.ToAddresses, m.CCAddresses, m.BCCAddresses = "", "", ""
		}},
		{"malformed recipient", func(m *store.OutboxMessage) { m.ToAddresses = "definitely not an address" }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mailer := NewMailer(TransportConfig{Host: "smtp.test", Port: 587}, nil)
			m := sampleOutbox()
			tc.mutate(&m)
			_, _, err := mailer.compose(context.Background(), m)
			if !errors.Is(err, ErrInvalidMessage) {
				t.Errorf("compose error = %v, want it to wrap ErrInvalidMessage", err)
			}
			if Permanent(err) != true {
				t.Errorf("Permanent(%v) = false, want true for unsendable input", err)
			}
		})
	}
}

func TestGoMailerComposeAttachments(t *testing.T) {
	hash := strings.Repeat("a", 64)
	blobs := fakeBlobs{content: map[string]string{hash: "first file"}}
	mailer := NewMailer(TransportConfig{Host: "smtp.test", Port: 587}, blobs)
	m := sampleOutbox()
	m.AttachmentHashes = SerializeAttachments([]AttachmentRef{{Hash: hash, Name: "notes.txt"}})

	message, closers, err := mailer.compose(context.Background(), m)
	if err != nil {
		t.Fatalf("compose: %v", err)
	}

	var rendered bytes.Buffer
	if _, err := message.WriteTo(&rendered); err != nil {
		t.Fatalf("WriteTo: %v", err)
	}
	// The stored file name must reach the MIME part header.
	if !strings.Contains(rendered.String(), "notes.txt") {
		t.Errorf("rendered message misses the attachment name:\n%s", rendered.String())
	}
	closeAll(closers)

	// A hash the blob store cannot resolve fails the compose and keeps the
	// message queued rather than sending a partial message.
	m.AttachmentHashes = SerializeAttachments([]AttachmentRef{{Hash: strings.Repeat("b", 64)}})
	_, _, err = mailer.compose(context.Background(), m)
	if !errors.Is(err, attachment.ErrNotFound) {
		t.Errorf("compose with missing blob error = %v, want it to wrap the store's ErrNotFound", err)
	}
}

func TestWrapDeliveryTagsRejections(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		wantCode int
	}{
		{
			"protocol error",
			fmt.Errorf("send failed: %w", &textproto.Error{Code: 550, Msg: "mailbox unavailable"}),
			550,
		},
		{
			"plain transport error keeps its shape",
			fmt.Errorf("dial failed: %w", errors.New("connect: connection refused")),
			0,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			wrapped := wrapDelivery(tc.err)
			if !strings.HasPrefix(wrapped.Error(), "send: deliver:") {
				t.Errorf("wrapped error = %v, want the send: deliver prefix", wrapped)
			}
			var rejection *Rejection
			if tc.wantCode == 0 {
				if errors.As(wrapped, &rejection) {
					t.Errorf("transport error became %v, want no rejection", wrapped)
				}
				return
			}
			if !errors.As(wrapped, &rejection) || rejection.Code != tc.wantCode {
				t.Errorf("wrapped error = %v, want a rejection with code %d", wrapped, tc.wantCode)
			}
			if !errors.Is(wrapped, ErrRejected) {
				t.Errorf("wrapped error = %v, want it to match ErrRejected", wrapped)
			}
			if Permanent(wrapped) != true {
				t.Errorf("Permanent(%v) = false, want true for a 5xx rejection", wrapped)
			}
		})
	}
}

func TestSASLAdapterSpeaksMechanism(t *testing.T) {
	tests := []struct {
		name      string
		authMeth  string
		isOAuth   bool
		wantMech  string
		wantFirst string
	}{
		{
			"plain",
			"password", false, "PLAIN", "\x00smtp-user\x00secret",
		},
		{
			"xoauth2",
			"xoauth2", true, "XOAUTH2", "user=smtp-user\x01auth=Bearer tok-1\x01\x01",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			secret := "secret"
			if tc.isOAuth {
				secret = "tok-1"
			}
			mechanism, err := auth.SASLMechanism(tc.authMeth, "smtp-user", secret, tc.isOAuth)
			if err != nil {
				t.Fatalf("SASLMechanism: %v", err)
			}
			adapter := newSASLAuth(mechanism)
			proto, response, err := adapter.Start(&gomailsmtp.ServerInfo{Name: "smtp.test", TLS: true})
			if err != nil {
				t.Fatalf("Start: %v", err)
			}
			if proto != tc.wantMech {
				t.Errorf("mechanism = %q, want %q", proto, tc.wantMech)
			}
			if string(response) != tc.wantFirst {
				t.Errorf("initial response = %q, want %q", response, tc.wantFirst)
			}
			// The adapter must not answer a challenge when the server has
			// ended the exchange.
			if next, err := adapter.Next(nil, false); next != nil || err != nil {
				t.Errorf("Next(more=false) = %q, %v, want nil nil", next, err)
			}
		})
	}
}

func TestBlobStoreAdapter(t *testing.T) {
	blobStore, err := attachment.Open(t.TempDir())
	if err != nil {
		t.Fatalf("attachment.Open: %v", err)
	}
	hash, _, err := blobStore.Put(strings.NewReader("attachment bytes"))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	adapter := NewBlobStore(blobStore)
	reader, err := adapter.Open(context.Background(), hash)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = reader.Close() }()
	content, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(content) != "attachment bytes" {
		t.Errorf("content = %q, want the stored bytes", content)
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := adapter.Open(cancelled, hash); err == nil {
		t.Error("Open with a cancelled context returned no error")
	}
}

func TestDeliverWithoutServerStaysTransient(t *testing.T) {
	// Port 1 on loopback refuses connections instantly and needs no
	// network; the failure must classify as transient so the worker
	// retries a server that is merely down.
	mailer := NewMailer(TransportConfig{
		Host:     "127.0.0.1",
		Port:     1,
		TLS:      TLSNone,
		Username: "smtp-user",
	}, nil)
	err := mailer.Deliver(context.Background(), sampleOutbox(),
		func(ctx context.Context) (string, string, string, bool, error) {
			return "", "secret", "password", false, nil
		})
	if err == nil {
		t.Fatal("Deliver against a dead endpoint returned no error")
	}
	if Permanent(err) {
		t.Errorf("Permanent(%v) = true, want false for a connection fault", err)
	}
	if !strings.Contains(err.Error(), "send:") {
		t.Errorf("error = %v, want the send package prefix", err)
	}
}
