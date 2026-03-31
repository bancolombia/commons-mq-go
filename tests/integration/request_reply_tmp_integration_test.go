//go:build integration

package integration_test

// T032: RequestReply() returns correlated response from echo server within timeout.
// T033: N concurrent RequestReply() callers each receive only their own correlated response.
// T034: ErrTimeout returned when no response arrives within the configured duration.

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

// echoServer starts a Listener on requestQueue that reads each message and
// sends a response to msg.ReplyToQueue containing the original body prefixed
// with "echo:". It runs until ctx is cancelled.
func echoServer(ctx context.Context, t *testing.T, cfg mq.Config, requestQueue string) {
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
			sender := echoClient.NewSender(msg.ReplyToQueue)
			reply := fmt.Sprintf("echo:%s", string(msg.Body))

			// Carry CorrelId from the request into the response so callers can
			// match with MQMO_MATCH_CORREL_ID if needed.
			_, _ = sender.SendWithCreator(context.Background(), msg.ReplyToQueue,
				func(resp *mq.Message) error {
					resp.Body = []byte(reply)
					resp.CorrelationID = msg.ID // correlate by MsgId of the request
					return nil
				})
			return nil
		})
	}()
	// Give the listener goroutines time to start consuming.
	time.Sleep(300 * time.Millisecond)
}

// T032: RequestReply returns correlated response from the echo server within timeout.
func TestRequestReply_Integration_EchoServer_RespondsWithinTimeout(t *testing.T) {
	host, port := startMQContainer(t)
	cfg := mqConfig(host, port)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	echoServer(ctx, t, cfg, mqTestQueue)

	client, err := mq.NewClient(cfg)
	require.NoError(t, err)
	defer client.Close()

	rr := client.NewRequestReplyClient(mqTestQueue)
	resp, err := rr.RequestReply(context.Background(), "hello", 10*time.Second)
	require.NoError(t, err, "RequestReply must not return an error")
	require.NotNil(t, resp)
	assert.Equal(t, "echo:hello", string(resp.Body),
		"response body must contain the echo-prefixed request payload")
	assert.NotEmpty(t, resp.ID, "response must have a message ID")
}

// T033: N concurrent RequestReply() callers each receive only their own response.
func TestRequestReply_Integration_ConcurrentCallers_GetOwnResponse(t *testing.T) {
	const concurrency = 5

	host, port := startMQContainer(t)
	cfg := mqConfig(host, port)
	cfg.InputConcurrency = concurrency + 1

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	echoServer(ctx, t, cfg, mqTestQueue2)

	client, err := mq.NewClient(cfg)
	require.NoError(t, err)
	defer client.Close()

	var wg sync.WaitGroup
	var failures atomic.Int32

	for i := range concurrency {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			payload := fmt.Sprintf("request-%d", idx)

			rr := client.NewRequestReplyClient(mqTestQueue2)
			resp, reqErr := rr.RequestReply(context.Background(), payload, 15*time.Second)
			if reqErr != nil {
				t.Logf("caller %d: RequestReply error: %v", idx, reqErr)
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
		"all concurrent callers must receive exactly their own response")
}

// T034: ErrTimeout returned when no responder is present within the configured timeout.
func TestRequestReply_Integration_NoResponder_ReturnsErrTimeout(t *testing.T) {
	host, port := startMQContainer(t)
	cfg := mqConfig(host, port)

	client, err := mq.NewClient(cfg)
	require.NoError(t, err)
	defer client.Close()

	// No echo server started — the reply queue will stay empty.
	rr := client.NewRequestReplyClient(mqTestQueue3)
	_, err = rr.RequestReply(context.Background(), "ping", 1500*time.Millisecond)
	assert.ErrorIs(t, err, mq.ErrTimeout,
		"RequestReply with no responder must return ErrTimeout")
}
