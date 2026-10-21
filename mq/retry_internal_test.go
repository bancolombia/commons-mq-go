package mq

import (
	"context"
	"testing"
	"time"
)

// TestRetryDelay_Capped verifies that retryDelay never exceeds MaxRetryInterval.
func TestRetryDelay_Capped(t *testing.T) {
	cfg := &Config{
		InitialRetryInterval: time.Second,
		RetryMultiplier:      2.0,
		MaxRetryInterval:     5 * time.Second,
	}

	if got := retryDelay(cfg, 0); got != time.Second {
		t.Fatalf("attempt 0: got %v, want 1s", got)
	}
	for _, attempt := range []int{1, 2, 3, 10, 100} {
		if got := retryDelay(cfg, attempt); got > cfg.MaxRetryInterval {
			t.Fatalf("attempt %d: delay %v exceeds cap %v", attempt, got, cfg.MaxRetryInterval)
		}
	}
}

// TestSleepWithJitterCtx_Cancelled verifies prompt return on context cancellation.
func TestSleepWithJitterCtx_Cancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	err := sleepWithJitterCtx(ctx, time.Hour)
	if err != context.Canceled {
		t.Fatalf("got %v, want context.Canceled", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("cancelled sleep took %v", elapsed)
	}
}

// TestSleepWithJitterCtx_ZeroDelay_NoPanic verifies the jitter guard.
func TestSleepWithJitterCtx_ZeroDelay_NoPanic(t *testing.T) {
	if err := sleepWithJitterCtx(context.Background(), 0); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
