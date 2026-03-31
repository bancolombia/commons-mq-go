//go:build integration

package integration_test

// T040: N concurrent RequestReply() calls via fixed queue each receive only their own response.
// T041: Custom correlation ID provided via MessageCreator is preserved and used for routing.

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bancolombia/commons-mq-go/mq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mqFixedReplyQueue is the shared fixed reply queue used by FixedQueueSelector tests.
// DEV.QUEUE.2 is pre-configured in the IBM MQ Docker image.
const mqFixedReplyQueue = mqTestQueue2

// echoServerFixed starts a Listener on requestQueue that echoes each request to
// msg.ReplyToQueue, preserving the CorrelationID so fixed-queue callers can match.
func echoServerFixed(ctx context.Context, t *testing.T, cfg mq.Config, requestQueue string) {
	t.Helper()

	echoClient, err := mq.NewClient(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { echoClient.Close() })

	listener := echoClient.NewListener(requestQueue)

	go func() {
		_ = listener.Start(ctx, func(_ context.Context, msg *mq.Message) error {
			if msg.ReplyToQueue == "" {
				return nil
			}
			_, _ = echoClient.NewSender(msg.ReplyToQueue).SendWithCreator(
				context.Background(), msg.ReplyToQueue,
				func(resp *mq.Message) error {
					resp.Body = []byte(fmt.Sprintf("echo:%s", string(msg.Body)))
					// Preserve the request CorrelationID in the response so that
					// MQMO_MATCH_CORREL_ID routing to the correct waiting caller works.
					resp.CorrelationID = msg.CorrelationID
					return nil
				})
			return nil
		})
	}()
	// Allow the listener goroutines to start.
	time.Sleep(300 * time.Millisecond)
}

// T040: N concurrent RequestReply() callers each receive only their own response
// from a shared fixed reply queue.
func TestRequestReplyFixed_Integration_ConcurrentCallers_GetOwnResponse(t *testing.T) {
	const concurrency = 5

	host, port := startMQContainer(t)
	cfg := mqConfig(host, port)
	cfg.InputConcurrency = concurrency + 1

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Echo server listens on DEV.QUEUE.1 and replies to msg.ReplyToQueue.
	echoServerFixed(ctx, t, cfg, mqTestQueue)

	client, err := mq.NewClient(cfg)
	require.NoError(t, err)
	defer client.Close()

	var wg sync.WaitGroup
	var failures atomic.Int32

	for i := range concurrency {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			payload := fmt.Sprintf("fixed-request-%d", idx)

			rr := client.NewRequestReplyClientFixed(mqTestQueue, mqFixedReplyQueue)
			resp, err := rr.RequestReply(context.Background(), payload, 15*time.Second)
			if err != nil {
				t.Logf("caller %d: error: %v", idx, err)
				failures.Add(1)
				return
			}
			expected := fmt.Sprintf("echo:%s", payload)
			if string(resp.Body) != expected {
				t.Logf("caller %d: got %q, want %q", idx, string(resp.Body), expected)
				failures.Add(1)
			}
		}(i)
	}

	wg.Wait()
	assert.Equal(t, int32(0), failures.Load(),
		"all concurrent callers must receive exactly their own response via fixed queue")
}

// T041: Custom correlation ID provided via MessageCreator is preserved in the
// received response (the echo server copies the CorrelationID).
func TestRequestReplyFixed_Integration_CustomCorrelID_PreservedInResponse(t *testing.T) {
	host, port := startMQContainer(t)
	cfg := mqConfig(host, port)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	echoServerFixed(ctx, t, cfg, mqTestQueue3)

	client, err := mq.NewClient(cfg)
	require.NoError(t, err)
	defer client.Close()

	// Use a known 24-byte hex correlation ID via MessageCreator.
	const customCorrelID = "0102030405060708090a0b0c0d0e0f101112131415161718"

	rr := client.NewRequestReplyClientFixed(mqTestQueue3, mqFixedReplyQueue)
	resp, err := rr.RequestReplyWithCreator(context.Background(), func(msg *mq.Message) error {
		msg.Body = []byte("custom-correl-payload")
		msg.CorrelationID = customCorrelID
		return nil
	}, 15*time.Second)

	require.NoError(t, err, "RequestReplyWithCreator with custom correlID must not error")
	require.NotNil(t, resp)
	assert.Equal(t, "echo:custom-correl-payload", string(resp.Body))
	// The echo server copies the request CorrelationID into the response.
	assert.Equal(t, customCorrelID, resp.CorrelationID,
		"custom correlation ID must be preserved in the response")
}

// TestRequestReplyFixed_Integration_Timeout_ReturnsErrTimeout verifies ErrTimeout
// when no responder is available on the shared fixed reply queue.
func TestRequestReplyFixed_Integration_Timeout_ReturnsErrTimeout(t *testing.T) {
	host, port := startMQContainer(t)
	cfg := mqConfig(host, port)

	client, err := mq.NewClient(cfg)
	require.NoError(t, err)
	defer client.Close()

	// No echo server — the fixed reply queue stays empty for our correlID.
	rr := client.NewRequestReplyClientFixed(mqTestQueue, mqFixedReplyQueue)
	_, err = rr.RequestReply(context.Background(), "ping", 1500*time.Millisecond)
	assert.ErrorIs(t, err, mq.ErrTimeout,
		"RequestReplyFixed with no responder must return ErrTimeout")
}
