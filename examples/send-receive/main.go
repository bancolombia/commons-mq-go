// Package main demonstrates how to use commons-mq-go to send and receive
// messages with IBM MQ.
//
// It:
//  1. Creates a client from environment-based configuration.
//  2. Starts a listener that prints every received message.
//  3. Sends a configurable number of messages to the same queue.
//  4. Waits for all messages to be received, then shuts down cleanly.
//
// Required environment variables:
//
//	MQ_QUEUE_MANAGER  - Queue manager name        (e.g. "QM1")
//	MQ_HOST           - Hostname                  (e.g. "localhost")
//	MQ_PORT           - Port                      (e.g. "1414")
//	MQ_CHANNEL        - Server-connection channel (e.g. "DEV.APP.SVRCONN")
//	MQ_QUEUE          - Queue name                (e.g. "DEV.QUEUE.1")
//
// Optional environment variables:
//
//	MQ_USERNAME       - Application user ID
//	MQ_PASSWORD       - Application password
//	MQ_MSG_COUNT      - Number of messages to send (default: 5)
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bancolombia/commons-mq-go/mq"
)

func main() {
	cfg := configFromEnv()
	msgCount := intEnv("MQ_MSG_COUNT", 5)

	// ── 1. Create client ─────────────────────────────────────────────────────
	// NewClient validates the config and applies defaults. It does NOT open any
	// MQ connections yet — those are established lazily on first use.
	client, err := mq.NewClient(cfg)
	if err != nil {
		log.Fatalf("failed to create MQ client: %v", err)
	}
	defer client.Close()

	log.Printf("client created  queue-manager=%s  host=%s  queue=%s",
		cfg.QueueManagerName, cfg.ConnectionName, cfg.InputQueue)

	// ── 2. Start listener ────────────────────────────────────────────────────
	// The listener receives messages from the queue and calls the handler for
	// each one. Start() blocks until the context is cancelled or a fatal error
	// occurs, so we run it in its own goroutine.
	var received atomic.Int32
	var allReceived sync.WaitGroup
	allReceived.Add(msgCount)

	listener := client.NewListener(cfg.InputQueue)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	listenerDone := make(chan error, 1)
	go func() {
		listenerDone <- listener.Start(ctx, func(_ context.Context, msg *mq.Message) error {
			n := received.Add(1)
			log.Printf("[recv] #%-2d  id=%.16s…  body=%s", n, msg.ID, string(msg.Body))
			allReceived.Done()
			return nil
		})
	}()

	// Give the listener a moment to establish its connection before sending.
	time.Sleep(500 * time.Millisecond)

	// ── 3. Send messages ─────────────────────────────────────────────────────
	// NewSender targets the same queue so we can observe the round-trip in a
	// single process. In production the sender and listener are typically in
	// separate services.
	sender := client.NewSender(cfg.OutputQueue)

	for i := range msgCount {
		payload := fmt.Sprintf("hello from commons-mq-go, message %d of %d", i+1, msgCount)
		msgID, err := sender.Send(context.Background(), payload)
		if err != nil {
			log.Printf("[send] error sending message %d: %v", i+1, err)
			continue
		}
		log.Printf("[send] #%-2d  id=%.16s…  body=%s", i+1, msgID, payload)
	}

	// ── 4. Wait for all messages to be received, then shut down ──────────────
	waitCh := make(chan struct{})
	go func() {
		allReceived.Wait()
		close(waitCh)
	}()

	select {
	case <-waitCh:
		log.Printf("all %d messages received — shutting down", msgCount)
	case <-time.After(30 * time.Second):
		log.Printf("timed out waiting for messages — received %d of %d", received.Load(), msgCount)
	}

	cancel() // stop the listener

	if err := <-listenerDone; err != nil {
		log.Printf("listener exited with error: %v", err)
	}

	// ── 5. Health check ───────────────────────────────────────────────────────
	status := client.Health()
	log.Printf("final health  healthy=%v", status.Healthy)
	for _, d := range status.Details {
		log.Printf("  connection  queue-manager=%s  alive=%v  reason=%s",
			d.QueueManager, d.Alive, d.Reason)
	}
}

// configFromEnv builds a Config from environment variables.
func configFromEnv() mq.Config {
	queue := requireEnv("MQ_QUEUE")
	return mq.Config{
		QueueManagerName: requireEnv("MQ_QUEUE_MANAGER"),
		ConnectionName:   fmt.Sprintf("%s(%s)", requireEnv("MQ_HOST"), requireEnv("MQ_PORT")),
		ChannelName:      requireEnv("MQ_CHANNEL"),
		Username:         os.Getenv("MQ_USERNAME"),
		Password:         os.Getenv("MQ_PASSWORD"),
		// Use the same queue for both input and output to demonstrate the round-trip.
		InputQueue:  queue,
		OutputQueue: queue,
	}
}

func requireEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		log.Fatalf("required environment variable %s is not set", key)
	}
	return v
}

func intEnv(key string, defaultVal int) int {
	v := os.Getenv(key)
	if v == "" {
		return defaultVal
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 {
		log.Fatalf("environment variable %s must be a positive integer, got %q", key, v)
	}
	return n
}
