package send

import (
	"context"
	"net/mail"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/mefiz0/posthaste/internal/backoff"
	"github.com/mefiz0/posthaste/internal/store"
)

// addr builds a mail address for compose tests.
func addr(name, address string) mail.Address {
	return mail.Address{Name: name, Address: address}
}

// newTestStore opens a throwaway account database for one test.
func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(context.Background(),
		filepath.Join(t.TempDir(), "acct.sqlite"), store.Options{})
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// fakeClock is an injected clock so tests drive retry schedules without
// waiting on real time.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock(start time.Time) *fakeClock {
	return &fakeClock{now: start}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// fakeMailer records deliveries and plays back scripted results; the last
// result repeats once the script runs out.
type fakeMailer struct {
	mu        sync.Mutex
	delivered []store.OutboxMessage
	authUser  string
	results   []error
	next      int
	called    chan struct{}
}

func (f *fakeMailer) Deliver(ctx context.Context, m store.OutboxMessage, auth AuthProvider) error {
	username, _, _, _, _ := auth(ctx)
	f.mu.Lock()
	f.delivered = append(f.delivered, m)
	f.authUser = username
	var result error
	if len(f.results) > 0 {
		result = f.results[len(f.results)-1]
		if f.next < len(f.results) {
			result = f.results[f.next]
			f.next++
		}
	}
	if f.called != nil {
		close(f.called)
		f.called = nil
	}
	f.mu.Unlock()
	return result
}

func (f *fakeMailer) deliveries() []store.OutboxMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]store.OutboxMessage(nil), f.delivered...)
}

// script arranges the delivery outcomes the fake plays back in order.
func (f *fakeMailer) script(results ...error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.results = results
	f.next = 0
}

// eventLog collects state-transition notifications in order.
type eventLog struct {
	mu     sync.Mutex
	record []Event
}

func (l *eventLog) Record(e Event) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.record = append(l.record, e)
}

func (l *eventLog) all() []Event {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]Event(nil), l.record...)
}

// harness bundles everything one worker behaviour test needs.
type harness struct {
	db     *store.Store
	mailer *fakeMailer
	clock  *fakeClock
	events *eventLog
	worker *Worker
}

// newHarness builds a worker against a temp-dir store and a deterministic
// schedule (zero jitter, exact policy), so assertions on retry dates are
// exact.
func newHarness(t *testing.T, policyFn func() backoff.Policy) *harness {
	t.Helper()

	db := newTestStore(t)
	mailer := &fakeMailer{}
	clock := newFakeClock(time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC))
	events := &eventLog{}

	deps := Deps{
		DB: db,
		Account: store.Account{
			ID: "acct-1",
			// The address is never logged or echoed; events carry the ID.
			Email: "sender.test@localhost",
		},
		Mailer: mailer,
		Auth: func(ctx context.Context) (string, string, string, bool, error) {
			return "smtp-user", "secret", "password", false, nil
		},
		Notify:    events.Record,
		Now:       clock.Now,
		PollEvery: time.Hour,
		Rand:      func() float64 { return 0 },
	}
	if policyFn != nil {
		deps.Policy = policyFn()
	}
	worker, err := New(deps)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return &harness{db: db, mailer: mailer, clock: clock, events: events, worker: worker}
}

// seedOutbox inserts an outbox row, applying optional mutations first.
func (h *harness) seedOutbox(t *testing.T, mutate func(*store.OutboxMessage)) store.OutboxMessage {
	t.Helper()
	m := store.OutboxMessage{
		ID:          NewOutboxID(),
		AccountID:   "acct-1",
		FromAddress: "me@sender.test",
		FromName:    "Sender",
		ToAddresses: "dest@other.test",
		Subject:     "Hello",
		BodyText:    "Body",
		State:       store.SendQueued,
	}
	if mutate != nil {
		mutate(&m)
	}
	if err := h.db.UpsertOutbox(context.Background(), m); err != nil {
		t.Fatalf("seed outbox: %v", err)
	}
	return m
}

func (h *harness) load(t *testing.T, id string) store.OutboxMessage {
	t.Helper()
	m, err := h.db.OutboxByID(context.Background(), id)
	if err != nil {
		t.Fatalf("load outbox %s: %v", id, err)
	}
	return m
}
