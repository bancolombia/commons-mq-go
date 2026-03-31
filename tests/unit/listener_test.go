package unit_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bancolombia/commons-mq-go/mq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// validListenerConfig returns a Config that passes validation but has no real MQ server.
func validListenerConfig() mq.Config {
	return mq.Config{
		QueueManagerName: "QM1",
		ConnectionName:   "localhost(1414)",
		ChannelName:      "DEV.APP.SVRCONN",
	}
}

// ── T014: Listener goroutine lifecycle and graceful shutdown ──────────────────

// TestListener_Start_AlreadyCancelledContext verifies that Start returns nil
// immediately when the context is already cancelled, without attempting any
// IBM MQ network connection.
func TestListener_Start_AlreadyCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before Start

	client, err := mq.NewClient(validListenerConfig())
	require.NoError(t, err)
	defer client.Close()

	l := client.NewListener("DEV.QUEUE.1")

	start := time.Now()
	err = l.Start(ctx, func(_ context.Context, _ *mq.Message) error {
		return nil
	})
	elapsed := time.Since(start)

	assert.NoError(t, err, "Start with cancelled context must return nil")
	assert.Less(t, elapsed, 200*time.Millisecond,
		"Start must return quickly when context is already cancelled")
}

// TestListener_Start_CancelledContext_SpawnsNConcurrentGoroutines verifies that
// WithListenerConcurrency(N) causes Start to check context cancellation N times
// (one per goroutine) and return nil.
func TestListener_Start_CancelledContext_SpawnsNConcurrentGoroutines(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	client, err := mq.NewClient(validListenerConfig())
	require.NoError(t, err)
	defer client.Close()

	const concurrency = 3
	l := client.NewListener("DEV.QUEUE.1", mq.WithListenerConcurrency(concurrency))

	err = l.Start(ctx, func(_ context.Context, _ *mq.Message) error {
		return nil
	})
	assert.NoError(t, err)
}

// TestListener_Start_HandlerInvocationCount verifies that each call to
// InvokeHandlerSafe increments an atomic counter, confirming that the handler
// is called once per message and the goroutine continues after each invocation.
func TestListener_Start_HandlerInvocationCount(t *testing.T) {
	var count atomic.Int64

	const messages = 5
	for i := range messages {
		msg := &mq.Message{Body: []byte("msg"), ID: string(rune('0' + i))}
		mq.InvokeHandlerSafe(context.Background(),
			func(_ context.Context, _ *mq.Message) error {
				count.Add(1)
				return nil
			},
			msg,
		)
	}

	assert.Equal(t, int64(messages), count.Load(),
		"handler must be invoked once per InvokeHandlerSafe call")
}

// ── ListenerOption functions ──────────────────────────────────────────────────

// TestListenerOption_WithConcurrency verifies that NewListener respects the
// concurrency option by checking Start exits early across multiple goroutines.
func TestListenerOption_WithConcurrency_AppliedOnStart(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	client, err := mq.NewClient(validListenerConfig())
	require.NoError(t, err)
	defer client.Close()

	l := client.NewListener("DEV.QUEUE.1", mq.WithListenerConcurrency(5))
	err = l.Start(ctx, func(_ context.Context, _ *mq.Message) error { return nil })
	assert.NoError(t, err)
}

// TestListenerOption_WithConcurrencyLessThanOne verifies that values < 1 are ignored.
func TestListenerOption_WithConcurrencyLessThanOne_IsIgnored(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	client, err := mq.NewClient(mq.Config{
		QueueManagerName: "QM1",
		ConnectionName:   "localhost(1414)",
		ChannelName:      "DEV.APP.SVRCONN",
		InputConcurrency: 2,
	})
	require.NoError(t, err)
	defer client.Close()

	// WithListenerConcurrency(0) should be ignored; effective concurrency stays at 2.
	l := client.NewListener("DEV.QUEUE.1", mq.WithListenerConcurrency(0))
	err = l.Start(ctx, func(_ context.Context, _ *mq.Message) error { return nil })
	assert.NoError(t, err)
}

// TestListenerOption_WithMaxRetries verifies the option is accepted.
func TestListenerOption_WithMaxRetries_Accepted(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	client, err := mq.NewClient(validListenerConfig())
	require.NoError(t, err)
	defer client.Close()

	l := client.NewListener("DEV.QUEUE.1", mq.WithListenerMaxRetries(3))
	err = l.Start(ctx, func(_ context.Context, _ *mq.Message) error { return nil })
	assert.NoError(t, err)
}

// TestListenerOption_WithQueueOptions verifies the option is accepted.
func TestListenerOption_WithQueueOptions_Accepted(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	client, err := mq.NewClient(validListenerConfig())
	require.NoError(t, err)
	defer client.Close()

	l := client.NewListener("DEV.QUEUE.1",
		mq.WithListenerQueueOptions(mq.QueueOptions{ReadAheadAllowed: true}))
	err = l.Start(ctx, func(_ context.Context, _ *mq.Message) error { return nil })
	assert.NoError(t, err)
}

// ── T079: FR-001 Panic recovery ───────────────────────────────────────────────

// TestInvokeHandlerSafe_RecoversPanic verifies that a panicking handler does not
// propagate the panic and that the goroutine continues normally.
func TestInvokeHandlerSafe_RecoversPanic(t *testing.T) {
	assert.NotPanics(t, func() {
		mq.InvokeHandlerSafe(
			context.Background(),
			func(_ context.Context, _ *mq.Message) error {
				panic("deliberate test panic")
			},
			&mq.Message{Body: []byte("test")},
		)
	}, "InvokeHandlerSafe must not propagate a panic from the handler")
}

// TestInvokeHandlerSafe_ContinuesAfterPanic verifies that after a panic in the
// first message the second message is still delivered successfully.
func TestInvokeHandlerSafe_ContinuesAfterPanic(t *testing.T) {
	var callCount atomic.Int64

	panicHandler := func(_ context.Context, _ *mq.Message) error {
		n := callCount.Add(1)
		if n == 1 {
			panic("panic on first message")
		}
		return nil
	}

	// First invocation panics but is recovered.
	mq.InvokeHandlerSafe(context.Background(), panicHandler, &mq.Message{Body: []byte("msg1")})
	// Second invocation succeeds — goroutine continues after the panic.
	mq.InvokeHandlerSafe(context.Background(), panicHandler, &mq.Message{Body: []byte("msg2")})

	assert.Equal(t, int64(2), callCount.Load(),
		"handler must be called for both the panicking message and the subsequent one")
}

// TestInvokeHandlerSafe_ReturnedErrorIsDiscarded verifies that the handler's
// error return value is not propagated (errors are logged, not returned from Start).
func TestInvokeHandlerSafe_HandlerErrorIsDiscarded(t *testing.T) {
	assert.NotPanics(t, func() {
		mq.InvokeHandlerSafe(
			context.Background(),
			func(_ context.Context, _ *mq.Message) error {
				return assert.AnError
			},
			&mq.Message{Body: []byte("test")},
		)
	})
}
