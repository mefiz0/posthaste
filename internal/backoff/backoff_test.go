package backoff

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestNextGrowsExponentiallyAndCaps(t *testing.T) {
	p := Policy{Initial: time.Second, Max: 5 * time.Second, Multiplier: 2, MaxAttempts: 0}
	b := NewWithRand(p, func() float64 { return 0 })

	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 5 * time.Second, 5 * time.Second}
	for i, w := range want {
		got, ok := b.Next()
		if !ok {
			t.Fatalf("attempt %d: unexpectedly exhausted", i)
		}
		if got != w {
			t.Fatalf("attempt %d: got %s, want %s", i, got, w)
		}
	}
}

func TestJitterOnlySubtractsWithinFraction(t *testing.T) {
	p := Policy{Initial: time.Second, Max: time.Minute, Multiplier: 2, Jitter: 0.25, MaxAttempts: 0}
	b := NewWithRand(p, func() float64 { return 1 })

	got, _ := b.Next()
	if got != 750*time.Millisecond {
		t.Fatalf("got %s, want 750ms", got)
	}
}

func TestMaxAttemptsThenExhausted(t *testing.T) {
	b := NewWithRand(Policy{Initial: time.Millisecond, Max: time.Second, MaxAttempts: 3}, func() float64 { return 0 })
	for i := 0; i < 3; i++ {
		if _, ok := b.Next(); !ok {
			t.Fatalf("attempt %d should be allowed", i)
		}
	}
	if _, ok := b.Next(); ok {
		t.Fatal("fourth attempt should be exhausted")
	}
}

func TestResetRestartsBudget(t *testing.T) {
	b := NewWithRand(Policy{Initial: time.Millisecond, Max: time.Second, MaxAttempts: 1}, func() float64 { return 0 })
	b.Next()
	if _, ok := b.Next(); ok {
		t.Fatal("expected exhaustion")
	}
	b.Reset()
	if b.Attempts() != 0 {
		t.Fatalf("attempts = %d after reset", b.Attempts())
	}
	if _, ok := b.Next(); !ok {
		t.Fatal("attempt after reset should be allowed")
	}
}

func TestWaitHonoursContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	b := NewWithRand(Policy{Initial: time.Hour, MaxAttempts: 5}, func() float64 { return 0 })
	err := b.Wait(ctx, Sleep)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want context.Canceled", err)
	}
}

func TestWaitReturnsExhausted(t *testing.T) {
	b := NewWithRand(Policy{Initial: time.Millisecond, MaxAttempts: 1}, func() float64 { return 0 })
	noSleep := func(context.Context, time.Duration) error { return nil }
	if err := b.Wait(context.Background(), noSleep); err != nil {
		t.Fatalf("first wait: %v", err)
	}
	if err := b.Wait(context.Background(), noSleep); !errors.Is(err, ErrExhausted) {
		t.Fatalf("got %v, want ErrExhausted", err)
	}
}

func TestCeilingPolicyTightens(t *testing.T) {
	p := CeilingPolicy(DefaultPolicy(), 24*time.Hour, 10)
	if p.Max != DefaultPolicy().Max {
		t.Fatalf("max should not grow: %s", p.Max)
	}
	if p.MaxAttempts != 10 {
		t.Fatalf("attempts = %d", p.MaxAttempts)
	}

	p2 := CeilingPolicy(Policy{Initial: time.Second, Max: time.Hour, MaxAttempts: 100}, 5*time.Minute, 10)
	if p2.Max != 5*time.Minute {
		t.Fatalf("max = %s, want 5m", p2.Max)
	}
	if p2.MaxAttempts != 10 {
		t.Fatalf("attempts = %d, want 10", p2.MaxAttempts)
	}
}

func TestPolicyJitterFractionClamps(t *testing.T) {
	if got := (Policy{Jitter: -1}).JitterFraction(); got != 0 {
		t.Fatalf("got %v", got)
	}
	if got := (Policy{Jitter: 2}).JitterFraction(); got != 1 {
		t.Fatalf("got %v", got)
	}
}
