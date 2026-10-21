//go:build integration

package integration_test

// Resilience tests: terminal consumer/sender failures must never be silent.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/bancolombia/commons-mq-go/mq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestResilience_ListenerTerminalError_HealthAndCallback verifies that a
// consumer which dies on a non-connection error reports StateFailed through
// Health() and invokes the application error callback with a nil message.
func TestResilience_ListenerTerminalError_HealthAndCallback(t *testing.T) {
	host, port := startMQContainer(t)
	cfg := mqConfig(host, port)

	client, err := mq.NewClient(cfg)
	require.NoError(t, err)
	defer client.Close()

	errCh := make(chan error, 1)
	l := client.NewListener("DOES.NOT.EXIST.QUEUE",
		mq.WithListenerErrorHandler(func(_ context.Context, msg *mq.Message, err error) {
			if msg == nil {
				select {
				case errCh <- err:
				default:
				}
			}
		}))

	startErr := l.Start(context.Background(), func(_ context.Context, _ *mq.Message) error { return nil })
	require.Error(t, startErr)

	select {
	case cbErr := <-errCh:
		assert.Error(t, cbErr, "consumer terminal error must reach the callback")
	case <-time.After(2 * time.Second):
		t.Fatal("consumer terminal error was not reported to the callback")
	}

	h := client.Health()
	assert.False(t, h.Healthy, "a dead consumer must not report healthy")
	require.NotEmpty(t, h.Details)
	assert.False(t, h.Details[0].Alive)
	assert.Contains(t, h.Details[0].Reason, "failed")
}

// TestResilience_ListenerStopped_HealthNotGreen verifies that a listener which
// stopped consuming no longer reports healthy.
func TestResilience_ListenerStopped_HealthNotGreen(t *testing.T) {
	host, port := startMQContainer(t)
	cfg := mqConfig(host, port)

	client, err := mq.NewClient(cfg)
	require.NoError(t, err)
	defer client.Close()

	ctx, cancel := context.WithCancel(context.Background())
	l := client.NewListener(mqTestQueue)
	done := make(chan error, 1)
	go func() {
		done <- l.Start(ctx, func(_ context.Context, _ *mq.Message) error { return nil })
	}()

	require.Eventually(t, func() bool { return client.Health().Healthy },
		10*time.Second, 100*time.Millisecond, "listener should become healthy after connecting")

	cancel()
	select {
	case err := <-done:
		assert.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("listener did not stop after context cancellation")
	}

	h := client.Health()
	assert.False(t, h.Healthy, "a stopped listener must not report healthy")
	require.NotEmpty(t, h.Details)
	assert.False(t, h.Details[0].Alive)
	assert.Contains(t, h.Details[0].Reason, "stopped")
}

// TestResilience_ClientClose_StopsRunningListener verifies that Client.Close
// cancels a running listener and waits for Start to return.
func TestResilience_ClientClose_StopsRunningListener(t *testing.T) {
	host, port := startMQContainer(t)
	cfg := mqConfig(host, port)

	client, err := mq.NewClient(cfg)
	require.NoError(t, err)

	l := client.NewListener(mqTestQueue)
	done := make(chan error, 1)
	go func() {
		done <- l.Start(context.Background(), func(_ context.Context, _ *mq.Message) error { return nil })
	}()

	require.Eventually(t, func() bool { return client.Health().Healthy },
		10*time.Second, 100*time.Millisecond, "listener should become healthy after connecting")

	start := time.Now()
	require.NoError(t, client.Close())
	assert.Less(t, time.Since(start), 5*time.Second, "Client.Close must stop the listener promptly")

	select {
	case err := <-done:
		assert.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("Listener.Start did not return after Client.Close")
	}

	h := client.Health()
	assert.False(t, h.Healthy, "a closed client must not report healthy")
}

// TestResilience_ClientClose_WaitsForInFlightHandler verifies that Client.Close
// does not return while a message handler is still running.
func TestResilience_ClientClose_WaitsForInFlightHandler(t *testing.T) {
	host, port := startMQContainer(t)
	cfg := mqConfig(host, port)

	client, err := mq.NewClient(cfg)
	require.NoError(t, err)

	handlerStarted := make(chan struct{}, 1)
	release := make(chan struct{})
	l := client.NewListener(mqTestQueue)
	go func() {
		_ = l.Start(context.Background(), func(_ context.Context, _ *mq.Message) error {
			handlerStarted <- struct{}{}
			<-release
			return nil
		})
	}()

	putMessage(t, cfg, mqTestQueue, "blocking")

	select {
	case <-handlerStarted:
	case <-time.After(10 * time.Second):
		t.Fatal("handler did not start")
	}

	closeDone := make(chan error, 1)
	go func() { closeDone <- client.Close() }()

	select {
	case <-closeDone:
		t.Fatal("Client.Close returned while a handler was still in flight")
	case <-time.After(300 * time.Millisecond):
	}

	close(release)

	select {
	case err := <-closeDone:
		assert.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("Client.Close did not return after the handler completed")
	}
}

// TestResilience_HandlerError_ReportedAndConsumerContinues verifies that a
// handler error reaches the callback while the consumer keeps consuming.
func TestResilience_HandlerError_ReportedAndConsumerContinues(t *testing.T) {
	host, port := startMQContainer(t)
	cfg := mqConfig(host, port)

	client, err := mq.NewClient(cfg)
	require.NoError(t, err)
	defer client.Close()

	putMessage(t, cfg, mqTestQueue, "bad")
	putMessage(t, cfg, mqTestQueue, "good")

	handlerErrs := make(chan error, 2)
	processed := make(chan string, 2)

	l := client.NewListener(mqTestQueue,
		mq.WithListenerErrorHandler(func(_ context.Context, msg *mq.Message, err error) {
			if msg != nil {
				select {
				case handlerErrs <- err:
				default:
				}
			}
		}))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- l.Start(ctx, func(_ context.Context, msg *mq.Message) error {
			body := string(msg.Body)
			processed <- body
			if body == "bad" {
				return assert.AnError
			}
			return nil
		})
	}()

	got := make([]string, 0, 2)
	for len(got) < 2 {
		select {
		case body := <-processed:
			got = append(got, body)
		case <-time.After(10 * time.Second):
			t.Fatalf("only %d/2 messages processed", len(got))
		}
	}
	assert.ElementsMatch(t, []string{"bad", "good"}, got,
		"consumer must continue after a handler error")

	select {
	case cbErr := <-handlerErrs:
		assert.ErrorIs(t, cbErr, assert.AnError)
	case <-time.After(2 * time.Second):
		t.Fatal("handler error was not reported to the callback")
	}

	assert.True(t, client.Health().Healthy, "consumer is still connected and must be healthy")

	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("listener did not stop")
	}
}

// TestResilience_HandlerPanic_ReportedAndConsumerContinues verifies that a
// panicking handler is converted to ErrHandlerPanic and does not stop the consumer.
func TestResilience_HandlerPanic_ReportedAndConsumerContinues(t *testing.T) {
	host, port := startMQContainer(t)
	cfg := mqConfig(host, port)

	client, err := mq.NewClient(cfg)
	require.NoError(t, err)
	defer client.Close()

	putMessage(t, cfg, mqTestQueue, "boom")
	putMessage(t, cfg, mqTestQueue, "survivor")

	handlerErrs := make(chan error, 2)
	processed := make(chan string, 2)

	l := client.NewListener(mqTestQueue,
		mq.WithListenerErrorHandler(func(_ context.Context, msg *mq.Message, err error) {
			if msg != nil {
				select {
				case handlerErrs <- err:
				default:
				}
			}
		}))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- l.Start(ctx, func(_ context.Context, msg *mq.Message) error {
			body := string(msg.Body)
			processed <- body
			if body == "boom" {
				panic("deliberate handler panic")
			}
			return nil
		})
	}()

	got := make([]string, 0, 2)
	for len(got) < 2 {
		select {
		case body := <-processed:
			got = append(got, body)
		case <-time.After(10 * time.Second):
			t.Fatalf("only %d/2 messages processed", len(got))
		}
	}
	assert.ElementsMatch(t, []string{"boom", "survivor"}, got)

	select {
	case cbErr := <-handlerErrs:
		assert.ErrorIs(t, cbErr, mq.ErrHandlerPanic)
	case <-time.After(2 * time.Second):
		t.Fatal("handler panic was not reported to the callback")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("listener did not stop")
	}
}

// TestResilience_SenderPermanentFailure_HealthNotGreen verifies that a sender
// whose connection was permanently lost reports unhealthy, even though no
// connection remains in the pool.
func TestResilience_SenderPermanentFailure_HealthNotGreen(t *testing.T) {
	ctr, host, port := startMQContainerWithRef(t)
	cfg := mqConfig(host, port)
	cfg.MaxRetries = 1
	cfg.InitialRetryInterval = 10 * time.Millisecond
	cfg.RetryMultiplier = 1.0
	cfg.MaxRetryInterval = 10 * time.Millisecond

	client, err := mq.NewClient(cfg)
	require.NoError(t, err)
	defer client.Close()

	sender := client.NewSender(mqTestQueue)
	_, err = sender.Send(context.Background(), "before")
	require.NoError(t, err)
	require.True(t, client.Health().Healthy, "sender should be healthy after a successful send")

	stopTimeout := 5 * time.Second
	require.NoError(t, ctr.Stop(context.Background(), &stopTimeout))

	_, err = sender.Send(context.Background(), "after")
	require.Error(t, err)
	assert.True(t, errors.Is(err, mq.ErrConnectionFailed),
		"send after broker loss must surface ErrConnectionFailed, got: %v", err)

	h := client.Health()
	assert.False(t, h.Healthy, "a permanently failed sender must not report healthy")
	require.NotEmpty(t, h.Details)
	assert.False(t, h.Details[0].Alive)
	assert.Contains(t, h.Details[0].Reason, "failed")
}
