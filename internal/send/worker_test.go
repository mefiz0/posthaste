package send

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/mefiz0/posthaste/internal/backoff"
	"github.com/mefiz0/posthaste/internal/store"
)

func TestNewValidatesDependencies(t *testing.T) {
	db := newTestStore(t)
	mailer := &fakeMailer{}
	auth := func(ctx context.Context) (string, string, string, bool, error) {
		return "u", "s", "password", false, nil
	}

	tests := []struct {
		name    string
		deps    Deps
		wantErr bool
	}{
		{"no store", Deps{Mailer: mailer, Auth: auth}, true},
		{"no mailer", Deps{DB: db, Auth: auth}, true},
		{"no auth", Deps{DB: db, Mailer: mailer}, true},
		{"complete", Deps{DB: db, Mailer: mailer, Auth: auth}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := New(tc.deps)
			if (err != nil) != tc.wantErr {
				t.Fatalf("New(...) error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

func TestProcessNextSendsDueMessage(t *testing.T) {
	h := newHarness(t, nil)
	seeded := h.seedOutbox(t, nil)

	processed, err := h.worker.ProcessNext(context.Background())
	if err != nil {
		t.Fatalf("ProcessNext: %v", err)
	}
	if !processed {
		t.Fatal("ProcessNext = false, want true for a due message")
	}

	got := h.load(t, seeded.ID)
	if got.State != store.SendSent {
		t.Errorf("state = %q, want %q", got.State, store.SendSent)
	}
	if got.Attempts != 1 {
		t.Errorf("attempts = %d, want 1", got.Attempts)
	}
	if got.LastError != "" {
		t.Errorf("last error = %q, want empty after success", got.LastError)
	}
	// Sent history stays in the outbox; the sync worker moves a copy to the
	// Sent folder. Deleting here would lose the user's record of the send.
	if _, err := h.db.OutboxByID(context.Background(), seeded.ID); err != nil {
		t.Errorf("sent row deleted: %v", err)
	}

	delivered := h.mailer.deliveries()
	if len(delivered) != 1 || delivered[0].ID != seeded.ID {
		t.Fatalf("delivered = %+v, want exactly the seeded message", delivered)
	}
	if h.mailer.authUser != "smtp-user" {
		t.Errorf("delivery used auth user %q, want the provider's", h.mailer.authUser)
	}

	events := h.events.all()
	if len(events) != 2 {
		t.Fatalf("events = %+v, want one per transition", events)
	}
	if events[0].State != store.SendSending || events[1].State != store.SendSent {
		t.Errorf("event states = %v, want [sending sent]", []store.SendState{events[0].State, events[1].State})
	}
	if events[1].AccountID != "acct-1" || events[1].OutboxID != seeded.ID {
		t.Errorf("sent event = %+v, want the account and outbox IDs", events[1])
	}
	if events[1].Err != "" {
		t.Errorf("sent event error = %q, want empty", events[1].Err)
	}
}

func TestProcessNextNothingDue(t *testing.T) {
	h := newHarness(t, nil)

	processed, err := h.worker.ProcessNext(context.Background())
	if err != nil || processed {
		t.Fatalf("empty outbox: processed = %v, err = %v, want false nil", processed, err)
	}

	// A queued message that is not yet due must not be touched.
	h.seedOutbox(t, func(m *store.OutboxMessage) {
		m.NextAttemptAt = h.clock.Now().Add(time.Minute)
	})
	processed, err = h.worker.ProcessNext(context.Background())
	if err != nil || processed {
		t.Fatalf("future message: processed = %v, err = %v, want false nil", processed, err)
	}
	if events := h.events.all(); len(events) != 0 {
		t.Errorf("events = %+v, want none", events)
	}
}

func TestTransientFailureSchedulesRetry(t *testing.T) {
	h := newHarness(t, nil)
	seeded := h.seedOutbox(t, nil)
	h.mailer.script(errors.New("dial tcp: connection refused"))

	if _, err := h.worker.ProcessNext(context.Background()); err != nil {
		t.Fatalf("ProcessNext: %v", err)
	}

	got := h.load(t, seeded.ID)
	if got.State != store.SendQueued {
		t.Errorf("state = %q, want %q after a transient failure", got.State, store.SendQueued)
	}
	if got.Attempts != 1 {
		t.Errorf("attempts = %d, want 1", got.Attempts)
	}
	// Zero jitter makes the shared policy's first delay exactly one second.
	wantDue := h.clock.Now().Add(time.Second)
	if !got.NextAttemptAt.Equal(wantDue) {
		t.Errorf("next attempt = %v, want %v", got.NextAttemptAt, wantDue)
	}
	if got.LastError == "" {
		t.Error("last error empty, want the failure description")
	}

	events := h.events.all()
	if len(events) != 2 || events[1].State != store.SendQueued {
		t.Fatalf("events = %+v, want sending then queued", events)
	}
	if events[1].Err == "" {
		t.Error("retry event carries no error text, want the failure description")
	}
}

func TestBackoffScheduleAdvancesAcrossRetries(t *testing.T) {
	h := newHarness(t, nil)
	seeded := h.seedOutbox(t, nil)
	h.mailer.script(errors.New("connection reset"), errors.New("connection reset"))

	if _, err := h.worker.ProcessNext(context.Background()); err != nil {
		t.Fatalf("first attempt: %v", err)
	}
	firstDue := h.load(t, seeded.ID).NextAttemptAt
	if want := h.clock.Now().Add(time.Second); !firstDue.Equal(want) {
		t.Fatalf("first retry due = %v, want %v", firstDue, want)
	}

	// The schedule must continue from where it is, not restart: the second
	// failure waits two seconds, not one.
	h.clock.Advance(time.Second)
	if _, err := h.worker.ProcessNext(context.Background()); err != nil {
		t.Fatalf("second attempt: %v", err)
	}
	got := h.load(t, seeded.ID)
	if got.Attempts != 2 {
		t.Errorf("attempts = %d, want 2", got.Attempts)
	}
	if want := h.clock.Now().Add(2 * time.Second); !got.NextAttemptAt.Equal(want) {
		t.Errorf("second retry due = %v, want %v", got.NextAttemptAt, want)
	}
}

func TestPermanentFailureFailsImmediately(t *testing.T) {
	h := newHarness(t, nil)
	seeded := h.seedOutbox(t, nil)
	h.mailer.script(fmt.Errorf("send: deliver: %w", &Rejection{Code: 550, Text: "mailbox unavailable"}))

	if _, err := h.worker.ProcessNext(context.Background()); err != nil {
		t.Fatalf("ProcessNext: %v", err)
	}

	got := h.load(t, seeded.ID)
	if got.State != store.SendFailed {
		t.Errorf("state = %q, want %q after a permanent rejection", got.State, store.SendFailed)
	}
	if got.Attempts != 1 {
		t.Errorf("attempts = %d, want 1", got.Attempts)
	}
	events := h.events.all()
	if len(events) != 2 || events[1].State != store.SendFailed {
		t.Fatalf("events = %+v, want sending then failed", events)
	}
	if events[1].Err == "" {
		t.Error("failure event carries no error text")
	}

	// A failed message is terminal: no amount of scanning retries it.
	processed, err := h.worker.ProcessNext(context.Background())
	if err != nil || processed {
		t.Errorf("re-scan after failure: processed = %v, err = %v, want false nil", processed, err)
	}
}

func TestAuthRejectionClassifiedPermanent(t *testing.T) {
	h := newHarness(t, nil)
	seeded := h.seedOutbox(t, nil)
	// A plain-text credential rejection must fail at once: retrying cannot
	// fix a rejected secret.
	h.mailer.script(errors.New("535 5.7.8 Error: authentication credentials rejected"))

	if _, err := h.worker.ProcessNext(context.Background()); err != nil {
		t.Fatalf("ProcessNext: %v", err)
	}
	if got := h.load(t, seeded.ID); got.State != store.SendFailed {
		t.Errorf("state = %q, want %q for an auth rejection", got.State, store.SendFailed)
	}
}

func TestExhaustionAfterTenAttempts(t *testing.T) {
	h := newHarness(t, nil)
	// Nine attempts already burned before this run; the tenth is the last
	// one the budget allows.
	seeded := h.seedOutbox(t, func(m *store.OutboxMessage) {
		m.Attempts = 9
		m.NextAttemptAt = h.clock.Now().Add(-time.Minute)
	})
	h.mailer.script(errors.New("connection reset"))

	processed, err := h.worker.ProcessNext(context.Background())
	if err != nil || !processed {
		t.Fatalf("ProcessNext = %v, %v, want true nil", processed, err)
	}

	got := h.load(t, seeded.ID)
	if got.State != store.SendFailed {
		t.Errorf("state = %q, want %q after exhausting the budget", got.State, store.SendFailed)
	}
	if got.Attempts != 10 {
		t.Errorf("attempts = %d, want 10", got.Attempts)
	}
}

func TestExhaustionByRetryCeiling(t *testing.T) {
	// A slow policy outruns the default 24-hour ceiling: the second retry
	// would land past it, so the message fails instead of waiting.
	h := newHarness(t, func() backoff.Policy {
		return backoff.Policy{
			Initial:     20 * time.Hour,
			Max:         20 * time.Hour,
			Multiplier:  2,
			MaxAttempts: 100,
		}
	})
	seeded := h.seedOutbox(t, nil)
	h.mailer.script(errors.New("connection reset"))

	if _, err := h.worker.ProcessNext(context.Background()); err != nil {
		t.Fatalf("first attempt: %v", err)
	}
	if got := h.load(t, seeded.ID); got.State != store.SendQueued {
		t.Fatalf("state after first failure = %q, want queued", got.State)
	}

	h.clock.Advance(20 * time.Hour)
	if _, err := h.worker.ProcessNext(context.Background()); err != nil {
		t.Fatalf("second attempt: %v", err)
	}
	got := h.load(t, seeded.ID)
	if got.State != store.SendFailed {
		t.Errorf("state = %q, want %q past the retry ceiling", got.State, store.SendFailed)
	}
	if got.Attempts != 2 {
		t.Errorf("attempts = %d, want 2", got.Attempts)
	}
}

func TestLastErrorScrubbedBeforeStoreAndNotify(t *testing.T) {
	h := newHarness(t, nil)
	seeded := h.seedOutbox(t, nil)
	h.mailer.script(errors.New("550 5.1.1 <bob@recipient.test>: recipient address rejected"))

	if _, err := h.worker.ProcessNext(context.Background()); err != nil {
		t.Fatalf("ProcessNext: %v", err)
	}

	got := h.load(t, seeded.ID)
	events := h.events.all()
	for _, stored := range []string{got.LastError, events[len(events)-1].Err} {
		if strings.Contains(stored, "bob@recipient.test") {
			t.Errorf("failure text leaks the address: %q", stored)
		}
		if !strings.Contains(stored, redactedAddress) {
			t.Errorf("failure text not scrubbed: %q", stored)
		}
	}
}

func TestRecoverInterruptedMovesSendingToQueued(t *testing.T) {
	h := newHarness(t, nil)
	seeded := h.seedOutbox(t, func(m *store.OutboxMessage) {
		m.State = store.SendSending
		m.Attempts = 3
		m.LastError = "earlier failure"
	})

	if err := h.worker.recoverInterrupted(context.Background()); err != nil {
		t.Fatalf("recoverInterrupted: %v", err)
	}

	got := h.load(t, seeded.ID)
	if got.State != store.SendQueued {
		t.Errorf("state = %q, want %q after recovery", got.State, store.SendQueued)
	}
	if got.Attempts != 3 || got.LastError != "earlier failure" {
		t.Errorf("row = %+v, want attempts and error preserved", got)
	}
	// Recovery makes the row due immediately so the send resumes at once.
	if got.NextAttemptAt.After(h.clock.Now()) {
		t.Errorf("next attempt = %v, want due now or earlier", got.NextAttemptAt)
	}
}

func TestWorkerRunDeliversAndExitsOnCancel(t *testing.T) {
	h := newHarness(t, nil)
	seeded := h.seedOutbox(t, nil)

	called := make(chan struct{})
	h.mailer.mu.Lock()
	h.mailer.called = called
	h.mailer.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- h.worker.Run(ctx) }()

	select {
	case <-called:
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not deliver the due message")
	}
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("Run returned %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not exit after cancellation")
	}

	if got := h.load(t, seeded.ID); got.State != store.SendSent {
		t.Errorf("state = %q, want sent", got.State)
	}
}

func TestTriggerDoesNotBlock(t *testing.T) {
	h := newHarness(t, nil)
	// More wake signals than the buffer holds must be dropped, never block
	// the caller queuing them.
	for i := 0; i < 5; i++ {
		h.worker.Trigger()
	}
}
