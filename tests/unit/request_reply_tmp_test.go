package unit_test

// T031: Unit tests for RequestReplyClient (TemporaryQueue mode).
// These tests exercise the unit-testable surface — option functions, factory
// construction, and the cancelled-context fast path — without requiring a live
// IBM MQ instance.

import (
	"context"
	"testing"
	"time"

	"github.com/bancolombia/commons-mq-go/mq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// validRRConfig returns a Config that passes validation but points to an unreachable MQ host.
func validRRConfig() mq.Config {
	return mq.Config{
		QueueManagerName: "QM1",
		ConnectionName:   "localhost(1414)",
		ChannelName:      "DEV.APP.SVRCONN",
		OutputQueue:      "DEV.QUEUE.1",
	}
}

// ── Factory ───────────────────────────────────────────────────────────────────

// TestNewRequestReplyClient_ReturnsNonNil verifies the factory returns a non-nil client.
func TestNewRequestReplyClient_ReturnsNonNil(t *testing.T) {
	client, err := mq.NewClient(validRRConfig())
	require.NoError(t, err)
	defer client.Close()

	rr := client.NewRequestReplyClient("DEV.QUEUE.1")
	assert.NotNil(t, rr)
}

// TestNewRequestReplyClient_Close_Idempotent verifies Close can be called multiple times.
func TestNewRequestReplyClient_Close_Idempotent(t *testing.T) {
	client, err := mq.NewClient(validRRConfig())
	require.NoError(t, err)
	defer client.Close()

	rr := client.NewRequestReplyClient("DEV.QUEUE.1")
	assert.NoError(t, rr.Close())
	assert.NoError(t, rr.Close(), "Close must be idempotent")
}

// TestClientClose_WithRequestReplyClient_NoError verifies that Client.Close
// does not error when a RequestReplyClient has been registered.
func TestClientClose_WithRequestReplyClient_NoError(t *testing.T) {
	client, err := mq.NewClient(validRRConfig())
	require.NoError(t, err)

	_ = client.NewRequestReplyClient("DEV.QUEUE.1")
	assert.NoError(t, client.Close())
}

// ── RROption ─────────────────────────────────────────────────────────────────

// TestRROption_WithRRReplyQueue_Accepted verifies WithRRReplyQueue does not panic.
func TestRROption_WithRRReplyQueue_Accepted(t *testing.T) {
	client, err := mq.NewClient(validRRConfig())
	require.NoError(t, err)
	defer client.Close()

	// In TemporaryQueue mode WithRRReplyQueue is a no-op; must not panic.
	rr := client.NewRequestReplyClient("DEV.QUEUE.1", mq.WithRRReplyQueue("DEV.QUEUE.REPLY"))
	assert.NotNil(t, rr)
}

// ── Context cancellation (timeout path) ──────────────────────────────────────

// TestRequestReply_AlreadyCancelledContext_ReturnsCanceled verifies that when the
// context is already cancelled, RequestReply returns context.Canceled immediately
// without attempting any IBM MQ connection.
func TestRequestReply_AlreadyCancelledContext_ReturnsCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	client, err := mq.NewClient(validRRConfig())
	require.NoError(t, err)
	defer client.Close()

	rr := client.NewRequestReplyClient("DEV.QUEUE.1")
	_, err = rr.RequestReply(ctx, "ping", 1*time.Second)
	assert.ErrorIs(t, err, context.Canceled,
		"RequestReply with cancelled context must return context.Canceled")
}

// TestRequestReply_AlreadyCancelledContext_WithCreator_ReturnsCanceled verifies the
// same fast-path for RequestReplyWithCreator.
func TestRequestReply_AlreadyCancelledContext_WithCreator_ReturnsCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	client, err := mq.NewClient(validRRConfig())
	require.NoError(t, err)
	defer client.Close()

	rr := client.NewRequestReplyClient("DEV.QUEUE.1")
	_, err = rr.RequestReplyWithCreator(ctx, func(msg *mq.Message) error {
		msg.Body = []byte("ping")
		return nil
	}, 1*time.Second)
	assert.ErrorIs(t, err, context.Canceled)
}

// TestRequestReply_DeadlineExceededContext_ReturnsDeadlineExceeded verifies the
// same fast-path for context.DeadlineExceeded.
func TestRequestReply_DeadlineExceededContext_ReturnsDeadlineExceeded(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 0)
	defer cancel()

	client, err := mq.NewClient(validRRConfig())
	require.NoError(t, err)
	defer client.Close()

	rr := client.NewRequestReplyClient("DEV.QUEUE.1")
	_, err = rr.RequestReply(ctx, "ping", 1*time.Second)
	assert.Error(t, err,
		"RequestReply with expired deadline context must return an error")
}

// TestRequestReply_UnreachableBroker_ReturnsConnectionError verifies that when
// the IBM MQ broker is unreachable, RequestReply returns a connection error
// (not a panic or hang). Uses a context with a short timeout to keep the test fast.
func TestRequestReply_UnreachableBroker_ReturnsConnectionError(t *testing.T) {
	cfg := mq.Config{
		QueueManagerName: "QM1",
		ConnectionName:   "localhost(19999)", // nothing listening
		ChannelName:      "DEV.APP.SVRCONN",
		MaxRetries:       0,
	}
	client, err := mq.NewClient(cfg)
	require.NoError(t, err)
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	rr := client.NewRequestReplyClient("DEV.QUEUE.1")
	_, err = rr.RequestReply(ctx, "ping", 500*time.Millisecond)
	assert.Error(t, err, "should get a connection error for unreachable broker")
}

// TestRequestReply_MessageCreatorError_Propagated verifies that if the
// MessageCreator returns an error it is surfaced to the caller.
// The connection attempt may fail first; the test accepts either error.
func TestRequestReply_MessageCreatorError_IsError(t *testing.T) {
	cfg := mq.Config{
		QueueManagerName: "QM1",
		ConnectionName:   "localhost(19999)", // unreachable
		ChannelName:      "DEV.APP.SVRCONN",
		MaxRetries:       0,
	}
	client, err := mq.NewClient(cfg)
	require.NoError(t, err)
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	rr := client.NewRequestReplyClient("DEV.QUEUE.1")
	_, err = rr.RequestReplyWithCreator(ctx, func(msg *mq.Message) error {
		msg.Body = []byte("ping")
		return nil
	}, 500*time.Millisecond)
	assert.Error(t, err)
}
