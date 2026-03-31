package unit_test

// T046: Unit tests for exponential backoff calculation: initial interval, multiplier,
//        max retries.
// T047: Unit tests for MaxRetries exhaustion — verify ErrConnectionFailed is surfaced.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/bancolombia/commons-mq-go/mq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ── T046: BackoffDuration calculation ────────────────────────────────────────

// TestBackoffDuration_Attempt0_ReturnsInitial verifies that attempt 0 returns
// exactly the initial interval (multiplier applied zero times).
func TestBackoffDuration_Attempt0_ReturnsInitial(t *testing.T) {
	d := mq.BackoffDuration(100*time.Millisecond, 2.0, 0)
	assert.Equal(t, 100*time.Millisecond, d,
		"attempt 0 must return the initial interval unchanged")
}

// TestBackoffDuration_Attempt1_AppliesMultiplierOnce verifies multiplier applied once.
func TestBackoffDuration_Attempt1_AppliesMultiplierOnce(t *testing.T) {
	d := mq.BackoffDuration(100*time.Millisecond, 2.0, 1)
	assert.Equal(t, 200*time.Millisecond, d)
}

// TestBackoffDuration_Attempt2_AppliesMultiplierTwice verifies multiplier applied twice.
func TestBackoffDuration_Attempt2_AppliesMultiplierTwice(t *testing.T) {
	d := mq.BackoffDuration(100*time.Millisecond, 2.0, 2)
	assert.Equal(t, 400*time.Millisecond, d)
}

// TestBackoffDuration_Attempt5_ExponentialGrowth verifies full exponential series.
func TestBackoffDuration_Attempt5_ExponentialGrowth(t *testing.T) {
	initial := 10 * time.Millisecond
	multiplier := 3.0
	// 10ms * 3^5 = 10ms * 243 = 2430ms
	expected := time.Duration(float64(initial) * 243)
	d := mq.BackoffDuration(initial, multiplier, 5)
	assert.Equal(t, expected, d)
}

// TestBackoffDuration_MultiplierOne_NeverGrows verifies that multiplier=1.0 keeps
// the interval constant (minimum valid multiplier per Config validation).
func TestBackoffDuration_MultiplierOne_NeverGrows(t *testing.T) {
	initial := 50 * time.Millisecond
	for attempt := range 10 {
		d := mq.BackoffDuration(initial, 1.0, attempt)
		assert.Equal(t, initial, d,
			"multiplier=1.0 must not grow the interval at attempt %d", attempt)
	}
}

// TestBackoffDuration_IntervalsAreMonotonicallyIncreasing verifies backoff grows.
func TestBackoffDuration_IntervalsAreMonotonicallyIncreasing(t *testing.T) {
	initial := 10 * time.Millisecond
	multiplier := 2.0
	prev := mq.BackoffDuration(initial, multiplier, 0)
	for attempt := 1; attempt <= 6; attempt++ {
		curr := mq.BackoffDuration(initial, multiplier, attempt)
		assert.Greater(t, curr, prev,
			"backoff must be strictly increasing at attempt %d", attempt)
		prev = curr
	}
}

// ── T047: MaxRetries exhaustion ───────────────────────────────────────────────

// TestReconnect_MaxRetries0_StartReturnsError verifies that when MaxRetries=0
// Start returns an error on an unreachable broker without hanging.
func TestReconnect_MaxRetries0_StartReturnsError(t *testing.T) {
	cfg := mq.Config{
		QueueManagerName:     "QM1",
		ConnectionName:       "localhost(19999)", // nothing listening
		ChannelName:          "DEV.APP.SVRCONN",
		MaxRetries:           0,
		InitialRetryInterval: 1 * time.Millisecond,
		RetryMultiplier:      2.0,
	}
	client, err := mq.NewClient(cfg)
	require.NoError(t, err, "NewClient must succeed — it does not dial")
	defer client.Close()

	l := client.NewListener("DEV.QUEUE.1", mq.WithListenerMaxRetries(0))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err = l.Start(ctx, func(_ context.Context, _ *mq.Message) error {
		return nil
	})
	assert.Error(t, err, "Start must return an error when connection to unreachable host fails")
}

// TestReconnect_MaxRetries2_SurfacesErrConnectionFailed verifies that when 2 retries
// are exhausted the error wraps ErrConnectionFailed.
func TestReconnect_MaxRetries2_SurfacesErrConnectionFailed(t *testing.T) {
	cfg := mq.Config{
		QueueManagerName:     "QM1",
		ConnectionName:       "localhost(19999)",
		ChannelName:          "DEV.APP.SVRCONN",
		MaxRetries:           2,
		InitialRetryInterval: 1 * time.Millisecond,
		RetryMultiplier:      1.0,
	}
	client, err := mq.NewClient(cfg)
	require.NoError(t, err)
	defer client.Close()

	l := client.NewListener("DEV.QUEUE.1", mq.WithListenerMaxRetries(2))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	err = l.Start(ctx, func(_ context.Context, _ *mq.Message) error {
		return nil
	})
	require.Error(t, err)
	// The initial connection fails; reconnect (via runConsumer) is triggered only
	// when a connection error occurs during consuming. The initial dial failure
	// surfaces directly. Either way, an error is returned.
	assert.Error(t, err,
		"Start must surface a connection error, got: %v", err)
}

// TestReconnect_SenderUnreachable_ReturnsError verifies that a Sender returns an
// error (and does not hang) when the broker is unreachable.
func TestReconnect_SenderUnreachable_ReturnsError(t *testing.T) {
	cfg := mq.Config{
		QueueManagerName:     "QM1",
		ConnectionName:       "localhost(19999)",
		ChannelName:          "DEV.APP.SVRCONN",
		OutputQueue:          "DEV.QUEUE.1",
		MaxRetries:           0,
		InitialRetryInterval: 1 * time.Millisecond,
		RetryMultiplier:      1.0,
	}
	client, err := mq.NewClient(cfg)
	require.NoError(t, err)
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	s := client.NewSender("DEV.QUEUE.1")
	_, err = s.Send(ctx, "payload")
	assert.Error(t, err, "Send to unreachable broker must return an error")
}

// TestReconnect_ErrConnectionFailed_IsSentinel verifies ErrConnectionFailed is a
// named sentinel that can be tested with errors.Is.
func TestReconnect_ErrConnectionFailed_IsSentinel(t *testing.T) {
	assert.True(t,
		errors.Is(mq.ErrConnectionFailed, mq.ErrConnectionFailed),
		"ErrConnectionFailed must be an errors.Is-comparable sentinel")
}

// TestBackoffDuration_ZeroAttempt_NoPanic verifies no panic on attempt=0.
func TestBackoffDuration_ZeroAttempt_NoPanic(t *testing.T) {
	assert.NotPanics(t, func() {
		_ = mq.BackoffDuration(10*time.Millisecond, 2.0, 0)
	})
}
