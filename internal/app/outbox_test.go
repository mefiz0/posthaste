package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/mefiz0/posthaste/internal/send"
	"github.com/mefiz0/posthaste/internal/store"
)

// TestOutboxRetryEditDiscard drives the failure UX end to end: a failed send
// is listed, can be reopened for editing, retried, and discarded.
func TestOutboxRetryEditDiscard(t *testing.T) {
	h := newHarness(t, 0)
	accountID := h.addAccount(t, "me@here.test")
	ctx := context.Background()

	rt, err := h.manager.Runtime(accountID)
	if err != nil {
		t.Fatalf("Runtime: %v", err)
	}

	row := store.OutboxMessage{
		ID:          "outbox-1",
		AccountID:   accountIDString(accountID),
		FromAddress: "me@here.test",
		FromName:    "Me",
		ToAddresses: "peer@other.test",
		Subject:     "stuck message",
		BodyText:    "the body",
		AttachmentHashes: send.SerializeAttachments([]send.AttachmentRef{
			{Hash: "abc123", Name: "notes.txt"},
		}),
		State:     store.SendFailed,
		Attempts:  10,
		LastError: "smtp 550: recipient rejected",
		CreatedAt: time.Unix(1700000000, 0).UTC(),
	}
	if err := rt.store.UpsertOutbox(ctx, row); err != nil {
		t.Fatalf("UpsertOutbox: %v", err)
	}

	svc := NewComposeService(h.manager)

	// The failed row appears in the merged outbox with its failure detail.
	items, err := svc.GetOutbox(ctx, 0)
	if err != nil {
		t.Fatalf("GetOutbox: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("GetOutbox returned %d items, want 1", len(items))
	}
	if items[0].OutboxID != "outbox-1" || items[0].State != string(store.SendFailed) {
		t.Fatalf("outbox item = %+v, want failed outbox-1", items[0])
	}
	if items[0].Error != "smtp 550: recipient rejected" {
		t.Errorf("error detail = %q", items[0].Error)
	}

	// Edit and resend can reopen the composed fields and kept attachments.
	draft, err := svc.GetOutboxDraft(ctx, accountID, "outbox-1")
	if err != nil {
		t.Fatalf("GetOutboxDraft: %v", err)
	}
	if draft.Draft.Subject != "stuck message" || draft.Draft.BodyText != "the body" {
		t.Errorf("draft content = %+v", draft.Draft)
	}
	if draft.Draft.DraftID != "outbox-1" || draft.Draft.AccountID != accountID {
		t.Errorf("draft identity = %+v", draft.Draft)
	}
	if len(draft.Draft.ToAddresses) != 1 || !strings.Contains(draft.Draft.ToAddresses[0], "peer@other.test") {
		t.Errorf("draft recipients = %v", draft.Draft.ToAddresses)
	}
	if len(draft.AttachmentNames) != 1 || draft.AttachmentNames[0] != "notes.txt" {
		t.Errorf("attachment names = %v, want [notes.txt]", draft.AttachmentNames)
	}

	// Retry puts the message back on the queue with a fresh budget; the
	// worker then delivers it successfully through the fake mailer.
	before := h.mailer.deliveries()
	if err := svc.RetrySend(ctx, accountID, "outbox-1"); err != nil {
		t.Fatalf("RetrySend: %v", err)
	}
	waitForSendState(t, rt.store, "outbox-1", store.SendSent)
	if h.mailer.deliveries() <= before {
		t.Errorf("retry did not reach the mailer (deliveries %d -> %d)", before, h.mailer.deliveries())
	}

	// Retrying a message that already left the failed state is rejected.
	if err := svc.RetrySend(ctx, accountID, "outbox-1"); err == nil {
		t.Error("RetrySend on a sent message = nil error, want a rejection")
	}

	// Discard removes a failed message outright.
	if err := rt.store.UpdateOutboxState(ctx, "outbox-1", store.SendFailed, 1, "x", 0); err != nil {
		t.Fatalf("UpdateOutboxState: %v", err)
	}
	if err := svc.DiscardOutbox(ctx, accountID, "outbox-1"); err != nil {
		t.Fatalf("DiscardOutbox: %v", err)
	}
	if _, err := rt.store.OutboxByID(ctx, "outbox-1"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("row after discard: err = %v, want ErrNotFound", err)
	}
}

// waitForSendState polls until the outbox row reaches want, so the test does
// not race the send worker's asynchronous delivery.
func waitForSendState(t *testing.T, db *store.Store, id string, want store.SendState) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		row, err := db.OutboxByID(context.Background(), id)
		if err == nil && row.State == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("outbox row %s never reached state %s", id, want)
}
