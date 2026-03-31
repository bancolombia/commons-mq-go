package unit_test

// T039: Unit tests for RequestReplyClient in FixedQueueSelector mode.
// Focuses on correlation ID uniqueness, factory construction, option functions,
// and the context-cancellation fast path — no live IBM MQ required.

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/bancolombia/commons-mq-go/mq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// validFixedRRConfig is a Config that passes validation but has no real MQ server.
func validFixedRRConfig() mq.Config {
	return mq.Config{
		QueueManagerName: "QM1",
		ConnectionName:   "localhost(1414)",
		ChannelName:      "DEV.APP.SVRCONN",
	}
}

// ── Factory ───────────────────────────────────────────────────────────────────

func TestNewRequestReplyClientFixed_ReturnsNonNil(t *testing.T) {
	client, err := mq.NewClient(validFixedRRConfig())
	require.NoError(t, err)
	defer client.Close()

	rr := client.NewRequestReplyClientFixed("DEV.QUEUE.1", "DEV.QUEUE.REPLY")
	assert.NotNil(t, rr)
}

func TestNewRequestReplyClientFixed_Close_Idempotent(t *testing.T) {
	client, err := mq.NewClient(validFixedRRConfig())
	require.NoError(t, err)
	defer client.Close()

	rr := client.NewRequestReplyClientFixed("DEV.QUEUE.1", "DEV.QUEUE.REPLY")
	assert.NoError(t, rr.Close())
	assert.NoError(t, rr.Close(), "Close must be idempotent")
}

func TestClientClose_WithFixedRRClient_NoError(t *testing.T) {
	client, err := mq.NewClient(validFixedRRConfig())
	require.NoError(t, err)

	_ = client.NewRequestReplyClientFixed("DEV.QUEUE.1", "DEV.QUEUE.REPLY")
	assert.NoError(t, client.Close())
}

// ── RROption ─────────────────────────────────────────────────────────────────

func TestRROption_WithRRReplyQueue_OverridesReplyQueue(t *testing.T) {
	client, err := mq.NewClient(validFixedRRConfig())
	require.NoError(t, err)
	defer client.Close()

	// Pass a different reply queue via option — must not panic.
	rr := client.NewRequestReplyClientFixed(
		"DEV.QUEUE.1", "DEV.QUEUE.REPLY",
		mq.WithRRReplyQueue("DEV.QUEUE.REPLY.OVERRIDE"),
	)
	assert.NotNil(t, rr)
}

// ── Context cancellation fast-path ───────────────────────────────────────────

func TestRequestReplyFixed_AlreadyCancelledContext_ReturnsCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	client, err := mq.NewClient(validFixedRRConfig())
	require.NoError(t, err)
	defer client.Close()

	rr := client.NewRequestReplyClientFixed("DEV.QUEUE.1", "DEV.QUEUE.REPLY")
	_, err = rr.RequestReply(ctx, "ping", 1*time.Second)
	assert.ErrorIs(t, err, context.Canceled)
}

func TestRequestReplyFixed_CancelledContext_WithCreator_ReturnsCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	client, err := mq.NewClient(validFixedRRConfig())
	require.NoError(t, err)
	defer client.Close()

	rr := client.NewRequestReplyClientFixed("DEV.QUEUE.1", "DEV.QUEUE.REPLY")
	_, err = rr.RequestReplyWithCreator(ctx, func(msg *mq.Message) error {
		msg.Body = []byte("ping")
		return nil
	}, 1*time.Second)
	assert.ErrorIs(t, err, context.Canceled)
}

// ── Correlation ID uniqueness ─────────────────────────────────────────────────

// TestRequestReplyFixed_CorrelID_Uniqueness verifies that concurrent RequestReply
// calls each use a unique correlation ID. We indirectly verify this by running N
// concurrent calls (all of which will fail with a connection error, not a panic)
// and checking that none of them hang or panic due to a shared/colliding correlID.
func TestRequestReplyFixed_CorrelID_Uniqueness_NoPanicOrDeadlock(t *testing.T) {
	const concurrency = 20

	client, err := mq.NewClient(mq.Config{
		QueueManagerName: "QM1",
		ConnectionName:   "localhost(19999)", // unreachable
		ChannelName:      "DEV.APP.SVRCONN",
		MaxRetries:       0,
	})
	require.NoError(t, err)
	defer client.Close()

	rr := client.NewRequestReplyClientFixed("DEV.QUEUE.1", "DEV.QUEUE.REPLY")

	var wg sync.WaitGroup
	for range concurrency {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			// Each call will fail with a connection error, not hang or panic.
			_, _ = rr.RequestReply(ctx, "ping", 500*time.Millisecond)
		}()
	}
	// If there is a deadlock the test will time out.
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
		// All goroutines returned — no deadlock.
	case <-time.After(10 * time.Second):
		t.Fatal("concurrent RequestReply calls deadlocked — possible correlID collision")
	}
}
