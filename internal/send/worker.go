package send

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/mefiz0/posthaste/internal/backoff"
	"github.com/mefiz0/posthaste/internal/store"
)

// Defaults for the worker cadence. They are variables-shaped constants so
// callers can read the chosen rhythm without reaching for magic numbers.
const (
	// DefaultPollEvery is how often the worker scans for due sends when
	// nothing wakes it.
	DefaultPollEvery = 15 * time.Second
	// DefaultAttemptTimeout bounds one delivery attempt, covering dial,
	// TLS handshake, and the whole SMTP conversation.
	DefaultAttemptTimeout = 60 * time.Second
	// DefaultRetryCeiling bounds the scheduled retry window of one message.
	// With the shared default policy the attempt cap binds first; the
	// ceiling guards a caller that configured a slower policy.
	DefaultRetryCeiling = 24 * time.Hour
)

// sendPolicy is the retry policy for outgoing messages: the shared backoff
// shape (start at one second, double, cap at five minutes, jitter up to a
// quarter) with an attempt budget of ten.
func sendPolicy() backoff.Policy {
	return backoff.DefaultPolicy()
}

// Event reports one outbox state transition. It is what the app layer
// forwards to the UI; Err carries the already-scrubbed failure description.
type Event struct {
	AccountID string
	OutboxID  string
	State     store.SendState
	// Err is the scrubbed failure text for failed and retrying transitions,
	// empty for sending and sent ones.
	Err string
}

// Deps wires a send worker to one account's outbox. Everything is injected:
// the worker owns no global state and talks to no network itself.
type Deps struct {
	// DB is the account's store; the outbox lives per account.
	DB *store.Store
	// Account is the account this worker serves. Events carry its ID.
	Account store.Account
	// Mailer performs one delivery attempt.
	Mailer Mailer
	// Auth supplies credentials per attempt.
	Auth AuthProvider
	// Notify observes every state transition. Optional.
	Notify func(Event)
	// PollEvery is the idle scan cadence; zero uses DefaultPollEvery. A
	// Trigger call wakes the worker earlier.
	PollEvery time.Duration
	// Now returns the current time. Zero value uses the wall clock.
	Now func() time.Time
	// Policy overrides the retry policy; zero value uses the send default.
	Policy backoff.Policy
	// RetryCeiling bounds the scheduled retry window; zero uses
	// DefaultRetryCeiling.
	RetryCeiling time.Duration
	// AttemptTimeout bounds one delivery; zero uses DefaultAttemptTimeout.
	AttemptTimeout time.Duration
	// Rand injects the jitter randomness source so schedules are
	// reproducible in tests; nil uses the package default.
	Rand backoff.RandFloat64
}

// Worker drains one account's outbox: it scans for queued sends that are
// due, drives each through a delivery attempt, and records the outcome.
// It is safe for concurrent use; the app shell runs one worker per account,
// mirroring the sync workers.
type Worker struct {
	deps   Deps
	policy backoff.Policy
	wake   chan struct{}

	// mu guards the per-message retry state below. Both maps only ever hold
	// entries for messages this worker has seen fail; terminal states drop
	// the entries again.
	mu         sync.Mutex
	backoffs   map[string]*backoff.Backoff
	cycleStart map[string]time.Time
}

// New validates the dependencies and returns a ready worker.
func New(d Deps) (*Worker, error) {
	if d.DB == nil {
		return nil, errors.New("send: no store configured")
	}
	if d.Mailer == nil {
		return nil, errors.New("send: no mailer configured")
	}
	if d.Auth == nil {
		return nil, errors.New("send: no auth provider configured")
	}
	if d.PollEvery <= 0 {
		d.PollEvery = DefaultPollEvery
	}
	if d.RetryCeiling <= 0 {
		d.RetryCeiling = DefaultRetryCeiling
	}
	if d.AttemptTimeout <= 0 {
		d.AttemptTimeout = DefaultAttemptTimeout
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	// A zero policy would degrade inside the backoff to "1s initial, 1s
	// cap"; the shared send policy is the meaningful default.
	if d.Policy == (backoff.Policy{}) {
		d.Policy = sendPolicy()
	}
	return &Worker{
		deps:       d,
		policy:     d.Policy,
		wake:       make(chan struct{}, 1),
		backoffs:   make(map[string]*backoff.Backoff),
		cycleStart: make(map[string]time.Time),
	}, nil
}

// Run drains due sends until ctx is done, scanning every PollEvery interval
// and immediately whenever Trigger is called. It returns ctx's error on
// shutdown; any other error means the outbox could not be read or written
// and the worker stopped so the caller can see and restart it.
func (w *Worker) Run(ctx context.Context) error {
	// A crash mid-delivery would otherwise strand the row in sending
	// forever, because only queued rows are picked up. Requeueing risks a
	// duplicate delivery when the crash happened after the server accepted
	// the message; that is the lesser fault compared to silence.
	if err := w.recoverInterrupted(ctx); err != nil {
		return err
	}

	ticker := time.NewTicker(w.deps.PollEvery)
	defer ticker.Stop()

	for {
		for {
			processed, err := w.ProcessNext(ctx)
			if err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				return err
			}
			if !processed {
				break
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		case <-w.wake:
		}
	}
}

// recoverInterrupted moves rows stuck in sending back to queued. It runs
// once at worker start; within a live worker only ProcessNext writes
// sending, and it always resolves the row before returning.
func (w *Worker) recoverInterrupted(ctx context.Context) error {
	stuck, err := w.deps.DB.ListOutbox(ctx, store.SendSending)
	if err != nil {
		return fmt.Errorf("send: recover interrupted sends: %w", err)
	}
	for _, m := range stuck {
		if err := w.deps.DB.UpdateOutboxState(ctx, m.ID, store.SendQueued,
			m.Attempts, m.LastError, toUnix(w.deps.Now())); err != nil {
			return fmt.Errorf("send: requeue interrupted send %s: %w", m.ID, err)
		}
	}
	return nil
}

// Trigger wakes the worker without blocking. The app layer calls it after
// enqueueing a message or moving a retry date so a send starts at once
// instead of on the next tick.
func (w *Worker) Trigger() {
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

// ProcessNext delivers the oldest due queued message and reports whether it
// handled one. The app layer can call it directly to drive a retry without
// waiting for the worker loop.
func (w *Worker) ProcessNext(ctx context.Context) (bool, error) {
	m, err := w.deps.DB.NextDueSend(ctx, toUnix(w.deps.Now()))
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("send: find due message: %w", err)
	}

	// Claim the row before delivering: a concurrent scan must not see it
	// as queued, and a crash while sending is recovered at the next start.
	if err := w.deps.DB.UpdateOutboxState(ctx, m.ID, store.SendSending,
		m.Attempts, m.LastError, toUnix(m.NextAttemptAt)); err != nil {
		return false, fmt.Errorf("send: claim send %s: %w", m.ID, err)
	}
	w.notify(m.ID, store.SendSending, "")

	attemptCtx, cancel := context.WithTimeout(ctx, w.deps.AttemptTimeout)
	err = w.deps.Mailer.Deliver(attemptCtx, m, w.deps.Auth)
	cancel()

	if err == nil {
		// The delivery completed; recording it must survive a worker
		// shutdown racing this write, or the send would be repeated later.
		return true, w.setState(context.WithoutCancel(ctx), m.ID, store.SendSent, m.Attempts+1, "", m.NextAttemptAt)
	}

	// The worker shutting down mid-attempt is not a delivery verdict: put
	// the row back untouched so the budget is spent on real attempts only.
	if ctx.Err() != nil {
		restoreCtx := context.WithoutCancel(ctx)
		if restoreErr := w.deps.DB.UpdateOutboxState(restoreCtx, m.ID, store.SendQueued,
			m.Attempts, m.LastError, toUnix(w.deps.Now())); restoreErr != nil {
			return true, fmt.Errorf("send: restore send %s: %w", m.ID, restoreErr)
		}
		return true, nil
	}

	return true, w.recordFailure(ctx, m, err)
}

// recordFailure applies the retry policy to one failed attempt. Permanent
// rejections and an exhausted budget or window fail the message; anything
// else schedules the next attempt per the shared backoff.
func (w *Worker) recordFailure(ctx context.Context, m store.OutboxMessage, err error) error {
	description := describeError(err)
	attempts := m.Attempts + 1

	if Permanent(err) {
		return w.setState(ctx, m.ID, store.SendFailed, attempts, description, m.NextAttemptAt)
	}

	delay, retry := w.nextDelay(m)
	now := w.deps.Now()
	if !retry || w.attemptsExhausted(attempts) || w.ceilingExceeded(m.ID, delay, now) {
		return w.setState(ctx, m.ID, store.SendFailed, attempts, description, m.NextAttemptAt)
	}

	w.beginCycle(m.ID, now)
	return w.setState(ctx, m.ID, store.SendQueued, attempts, description, now.Add(delay))
}

// attemptsExhausted reports whether the delivery budget is spent after this
// many attempts. The backoff iterator counts delays, which schedule one
// fewer delivery than the attempt budget implies, so the worker counts
// deliveries itself and treats the policy cap as the authority.
func (w *Worker) attemptsExhausted(attempts int) bool {
	return w.policy.MaxAttempts > 0 && attempts >= w.policy.MaxAttempts
}

// nextDelay advances the message's backoff iterator and reports the delay
// before the next attempt. The iterator is per message and replayed to the
// message's attempt count when first seen, so a restarted worker continues
// the schedule instead of starting over.
func (w *Worker) nextDelay(m store.OutboxMessage) (time.Duration, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	b, ok := w.backoffs[m.ID]
	if !ok {
		b = backoff.NewWithRand(w.policy, w.deps.Rand)
		for i := 0; i < m.Attempts; i++ {
			_, _ = b.Next()
		}
		w.backoffs[m.ID] = b
	}
	return b.Next()
}

// ceilingExceeded reports whether scheduling another delay now would push
// the message past its total retry window. The window opens at the first
// failure this worker observes; it is not persisted, so a restart reopens
// it — the attempt cap still bounds the worst case across restarts.
func (w *Worker) ceilingExceeded(outboxID string, delay time.Duration, now time.Time) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	start, seen := w.cycleStart[outboxID]
	if !seen {
		return false
	}
	return now.Sub(start)+delay >= w.deps.RetryCeiling
}

// beginCycle opens the retry window for a message on its first observed
// failure.
func (w *Worker) beginCycle(outboxID string, now time.Time) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, seen := w.cycleStart[outboxID]; !seen {
		w.cycleStart[outboxID] = now
	}
}

// forgetMessage drops the per-message retry state once its fate is decided,
// keeping the maps bounded and a resent message starting fresh.
func (w *Worker) forgetMessage(outboxID string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.backoffs, outboxID)
	delete(w.cycleStart, outboxID)
}

// setState persists a transition and notifies observers. The failure text,
// when present, has been scrubbed by the caller before it reaches here.
func (w *Worker) setState(ctx context.Context, outboxID string, state store.SendState, attempts int, lastErr string, next time.Time) error {
	if err := w.deps.DB.UpdateOutboxState(ctx, outboxID, state, attempts, lastErr, toUnix(next)); err != nil {
		return fmt.Errorf("send: mark %s as %s: %w", outboxID, state, err)
	}
	switch state {
	case store.SendSent, store.SendFailed:
		w.forgetMessage(outboxID)
	}
	w.notify(outboxID, state, lastErr)
	return nil
}

// notify pushes one transition to the observer. The failure text has been
// scrubbed before it reaches the event.
func (w *Worker) notify(outboxID string, state store.SendState, lastErr string) {
	if w.deps.Notify == nil {
		return
	}
	w.deps.Notify(Event{
		AccountID: w.deps.Account.ID,
		OutboxID:  outboxID,
		State:     state,
		Err:       lastErr,
	})
}
