package send

import (
	"context"
	"net/mail"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/mefiz0/posthaste/internal/store"
)

func TestComposeToOutboxMapping(t *testing.T) {
	input := ComposeInput{
		AccountID: "acct-1",
		From:      addr("Ada Lovelace", "ada@sender.test"),
		To: []mail.Address{
			addr("Grace Hopper", "grace@other.test"),
			addr("", "plain@other.test"),
		},
		Cc:             []mail.Address{addr("Copy Person", "copy@third.test")},
		Bcc:            []mail.Address{addr("Quiet Person", "quiet@fourth.test")},
		Subject:        "Reports",
		BodyText:       "Hello there",
		BodyHTML:       "<p>Hello there</p>",
		InReplyTo:      "<original@other.test>",
		References:     "<first@other.test> <original@other.test>",
		AttachmentRefs: []AttachmentRef{{Hash: "aaaa", Name: "notes.txt"}, {Hash: "bbbb"}},
	}

	got := ComposeToOutbox(input)

	if got.AccountID != "acct-1" {
		t.Errorf("account = %q, want acct-1", got.AccountID)
	}
	if got.FromAddress != "ada@sender.test" || got.FromName != "Ada Lovelace" {
		t.Errorf("from = %q/%q, want Ada Lovelace/ada@sender.test", got.FromName, got.FromAddress)
	}
	if !strings.Contains(got.ToAddresses, "grace@other.test") ||
		!strings.Contains(got.ToAddresses, "plain@other.test") {
		t.Errorf("to = %q, want both recipients", got.ToAddresses)
	}
	if !strings.Contains(got.CCAddresses, "copy@third.test") {
		t.Errorf("cc = %q, want the recipient", got.CCAddresses)
	}
	if !strings.Contains(got.BCCAddresses, "quiet@fourth.test") {
		t.Errorf("bcc = %q, want the recipient", got.BCCAddresses)
	}
	if got.Subject != "Reports" || got.BodyText != "Hello there" || got.BodyHTML != "<p>Hello there</p>" {
		t.Errorf("content = %+v, want the compose fields copied", got)
	}
	if got.InReplyTo != "<original@other.test>" {
		t.Errorf("in-reply-to = %q, want the original message id", got.InReplyTo)
	}
	if got.References != "<first@other.test> <original@other.test>" {
		t.Errorf("references = %q, want the verbatim chain", got.References)
	}
	if got.AttachmentHashes != `[{"hash":"aaaa","name":"notes.txt"},{"hash":"bbbb","name":""}]` {
		t.Errorf("attachment hashes = %q, want the JSON object array", got.AttachmentHashes)
	}
	if got.State != store.SendDraft {
		t.Errorf("state = %q, want draft", got.State)
	}
	if !got.NextAttemptAt.IsZero() {
		t.Errorf("next attempt = %v, want zero for a draft", got.NextAttemptAt)
	}
	if !uuidPattern.MatchString(got.ID) {
		t.Errorf("id = %q, want a UUID-shaped string", got.ID)
	}
	if again := ComposeToOutbox(input); again.ID == got.ID {
		t.Errorf("two composes share id %q, want distinct identifiers", got.ID)
	}
}

// uuidPattern matches the textual shape of a version 4 UUID.
var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestNewOutboxIDIsVersion4UUID(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 100; i++ {
		id := NewOutboxID()
		if !uuidPattern.MatchString(id) {
			t.Fatalf("id %q is not a version 4 UUID", id)
		}
		if seen[id] {
			t.Fatalf("id %q generated twice", id)
		}
		seen[id] = true
	}
}

func TestSerializeAddressesRoundTrip(t *testing.T) {
	tests := []struct {
		name  string
		addrs []mail.Address
	}{
		{"empty", nil},
		{"single", []mail.Address{addr("One", "one@x.test")}},
		{
			"comma in display name",
			[]mail.Address{addr("Doe, Jane", "jane@x.test"), addr("Bob", "bob@y.test")},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			parsed, err := ParseAddresses(SerializeAddresses(tc.addrs))
			if err != nil {
				t.Fatalf("round trip: %v", err)
			}
			if len(parsed) != len(tc.addrs) {
				t.Fatalf("parsed %d addresses, want %d", len(parsed), len(tc.addrs))
			}
			for i, want := range tc.addrs {
				if parsed[i].Address != want.Address || parsed[i].Name != want.Name {
					t.Errorf("parsed[%d] = %+v, want %+v", i, parsed[i], want)
				}
			}
		})
	}
}

func TestParseAddressesRejectsGarbage(t *testing.T) {
	if _, err := ParseAddresses("not-an-address"); err == nil {
		t.Error("ParseAddresses accepted a malformed address list")
	}
}

func TestSerializeAttachmentsRoundTrip(t *testing.T) {
	if got := SerializeAttachments(nil); got != "" {
		t.Errorf("SerializeAttachments(nil) = %q, want empty", got)
	}
	refs := []AttachmentRef{{Hash: "c1", Name: "a.pdf"}, {Hash: "c2", Name: "b.txt"}}
	parsed, err := ParseAttachments(SerializeAttachments(refs))
	if err != nil {
		t.Fatalf("round trip: %v", err)
	}
	if len(parsed) != len(refs) {
		t.Fatalf("parsed %d refs, want %d", len(parsed), len(refs))
	}
	for i := range refs {
		if parsed[i] != refs[i] {
			t.Errorf("parsed[%d] = %+v, want %+v", i, parsed[i], refs[i])
		}
	}
	if _, err := ParseAttachments(`[{"hash":`); err == nil {
		t.Error("ParseAttachments accepted malformed JSON")
	}
}

func TestParseAttachmentsReadsLegacyHashRows(t *testing.T) {
	// Rows written before attachment names were stored carry a bare hash
	// array; they parse with empty names instead of failing, so a queued
	// message from an older build still sends.
	refs, err := ParseAttachments(`["c1","c2"]`)
	if err != nil {
		t.Fatalf("legacy parse: %v", err)
	}
	if len(refs) != 2 || refs[0].Hash != "c1" || refs[0].Name != "" || refs[1].Hash != "c2" {
		t.Errorf("legacy refs = %+v, want the hashes with empty names", refs)
	}
	if _, err := ParseAttachments(`{"hash":"c1"}`); err == nil {
		t.Error("ParseAttachments accepted an unsupported JSON shape")
	}
}

func TestEnqueueMovesDraftToQueued(t *testing.T) {
	h := newHarness(t, nil)
	draft := h.seedOutbox(t, func(m *store.OutboxMessage) {
		m.State = store.SendDraft
		m.Attempts = 7
		m.LastError = "old failure text"
	})

	now := h.clock.Now()
	loaded := h.load(t, draft.ID)
	if err := Enqueue(context.Background(), h.db, now, loaded); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	got := h.load(t, draft.ID)
	if got.State != store.SendQueued {
		t.Errorf("state = %q, want queued", got.State)
	}
	if !got.NextAttemptAt.Equal(now) {
		t.Errorf("next attempt = %v, want the enqueue time %v", got.NextAttemptAt, now)
	}
	if got.Attempts != 0 {
		t.Errorf("attempts = %d, want a fresh budget", got.Attempts)
	}
	if got.LastError != "" {
		t.Errorf("last error = %q, want cleared", got.LastError)
	}
	// The worker must pick the row up on its next scan or trigger.
	row, err := h.db.NextDueSend(context.Background(), now.Add(time.Minute).Unix())
	if err != nil {
		t.Fatalf("NextDueSend: %v", err)
	}
	if row.ID != draft.ID {
		t.Errorf("NextDueSend = %s, want the enqueued row", row.ID)
	}
}
