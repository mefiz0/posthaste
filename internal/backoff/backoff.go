// Package backoff is the single retry/backoff policy shared by the sync engine,
// the send queue, and account-failure handling. Keeping one implementation
// prevents those call sites from drifting apart.
package backoff

import (
	"context"
	"errors"
	"math"
	"math/rand/v2"
	"time"
)

// ErrExhausted is returned by Next when the attempt budget is spent.
var ErrExhausted = errors.New("backoff: retries exhausted")

// Policy describes an exponential backoff with jitter and an attempt cap.
type Policy struct {
	// Initial is the delay before the first retry.
	Initial time.Duration
	// Max caps the exponential growth of the delay.
	Max time.Duration
	// Multiplier grows the delay each attempt; values <= 1 are treated as 2.
	Multiplier float64
	// Jitter is the fraction (0..1) of the computed delay that may be randomly
	// subtracted, spreading retries across clients.
	Jitter float64
	// MaxAttempts is the total number of attempts allowed. Zero means unlimited.
	MaxAttempts int
}

// DefaultPolicy is the shared default: start at one second, double, cap at five
// minutes, with up to 25% jitter and an attempt cap of 10.
func DefaultPolicy() Policy {
	return Policy{
		Initial:     time.Second,
		Max:         5 * time.Minute,
		Multiplier:  2,
		Jitter:      0.25,
		MaxAttempts: 10,
	}
}

// RandFloat64 is the source of randomness used for jitter. It is injectable so
// tests are deterministic.
type RandFloat64 func() float64

// Backoff is a stateful iterator over a Policy. It is safe for a single
// goroutine; callers that retry concurrently use one Backoff per operation.
type Backoff struct {
	policy    Policy
	randFloat RandFloat64
	attempts  int
	current   time.Duration
}

// New returns a Backoff using the default random source.
func New(policy Policy) *Backoff {
	return NewWithRand(policy, rand.Float64)
}

// NewWithRand returns a Backoff with an injected randomness source.
func NewWithRand(policy Policy, randFloat RandFloat64) *Backoff {
	if policy.Multiplier <= 1 {
		policy.Multiplier = 2
	}
	if policy.Initial <= 0 {
		policy.Initial = time.Second
	}
	if policy.Max < policy.Initial {
		policy.Max = policy.Initial
	}
	if randFloat == nil {
		randFloat = rand.Float64
	}
	return &Backoff{policy: policy, randFloat: randFloat, current: policy.Initial}
}

// Attempts reports how many delays have been produced so far.
func (b *Backoff) Attempts() int { return b.attempts }

// Reset clears the attempt counter and returns to the initial delay.
func (b *Backoff) Reset() {
	b.attempts = 0
	b.current = b.policy.Initial
}

// Next returns the delay before the next attempt and whether one is allowed.
// When it returns false the caller should treat the operation as a persistent
// failure rather than retrying forever.
func (b *Backoff) Next() (time.Duration, bool) {
	if b.policy.MaxAttempts > 0 && b.attempts >= b.policy.MaxAttempts {
		return 0, false
	}

	delay := b.current
	if b.policy.Jitter > 0 {
		delay -= time.Duration(float64(delay) * b.policy.Jitter * b.randFloat())
		if delay < 0 {
			delay = 0
		}
	}

	b.attempts++
	next := time.Duration(float64(b.current) * b.policy.Multiplier)
	if next > b.policy.Max {
		next = b.policy.Max
	}
	b.current = next
	return delay, true
}

// SleepFunc abstracts waiting so tests can advance an injected clock instead of
// sleeping in real time.
type SleepFunc func(ctx context.Context, d time.Duration) error

// Wait calls Next and, if a retry is allowed, sleeps for the delay while
// honouring context cancellation. It returns ErrExhausted when the attempt
// budget is spent and ctx.Err() when the context ends first.
func (b *Backoff) Wait(ctx context.Context, sleep SleepFunc) error {
	delay, ok := b.Next()
	if !ok {
		return ErrExhausted
	}
	if err := sleep(ctx, delay); err != nil {
		return err
	}
	return nil
}

// Sleep waits for d or until ctx is done, whichever comes first.
func Sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// CeilingPolicy returns a copy of p whose Max is no larger than limit and whose
// MaxAttempts is at most attempts. It is used by the send queue, which tolerates
// a longer overall retry window than a background sync reconnect because an
// unsent message is more valuable to keep trying.
func CeilingPolicy(p Policy, limit time.Duration, attempts int) Policy {
	if limit > 0 && (p.Max == 0 || p.Max > limit) {
		p.Max = limit
	}
	if attempts > 0 && (p.MaxAttempts == 0 || p.MaxAttempts > attempts) {
		p.MaxAttempts = attempts
	}
	return p
}

// JitterFraction is exposed for callers that need to reason about the spread of
// delays (for example, tests asserting the cap is respected).
func (p Policy) JitterFraction() float64 { return math.Min(math.Max(p.Jitter, 0), 1) }
