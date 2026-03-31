// Package main demonstrates the request-reply pattern with commons-mq-go.
//
// Two modes are shown in a single process:
//
//  1. A "replier" Listener that reads requests from MQ_REQUEST_QUEUE,
//     processes each one, and sends the response back to msg.ReplyToQueue.
//  2. A "requester" that sends N requests via a RequestReplyClient and
//     waits synchronously for each correlated response.
//
// Both TemporaryQueue (default) and FixedQueueSelector modes are supported
// via the MQ_RR_MODE environment variable.
//
// Required environment variables:
//
//	MQ_QUEUE_MANAGER   - Queue manager name           (e.g. "QM1")
//	MQ_HOST            - Hostname                     (e.g. "localhost")
//	MQ_PORT            - Port                         (e.g. "1414")
//	MQ_CHANNEL         - Server-connection channel    (e.g. "DEV.APP.SVRCONN")
//	MQ_REQUEST_QUEUE   - Queue where requests land    (e.g. "DEV.QUEUE.1")
//
// Optional environment variables:
//
//	MQ_REPLY_QUEUE     - Fixed reply queue (required when MQ_RR_MODE=fixed)
//	MQ_USERNAME        - Application user ID
//	MQ_PASSWORD        - Application password
//	MQ_MSG_COUNT       - Number of request-reply round-trips (default: 3)
//	MQ_RR_MODE         - "temp" (default) or "fixed"
//	MQ_TIMEOUT_SECS    - Per-request timeout in seconds (default: 10)
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/bancolombia/commons-mq-go/mq"
)

func main() {
	cfg := configFromEnv()
	requestQueue := requireEnv("MQ_REQUEST_QUEUE")
	replyQueue := os.Getenv("MQ_REPLY_QUEUE")
	rrMode := strings.ToLower(os.Getenv("MQ_RR_MODE"))
	msgCount := intEnv("MQ_MSG_COUNT", 3)
	timeoutSecs := intEnv("MQ_TIMEOUT_SECS", 10)
	timeout := time.Duration(timeoutSecs) * time.Second

	if rrMode == "fixed" && replyQueue == "" {
		log.Fatal("MQ_REPLY_QUEUE must be set when MQ_RR_MODE=fixed")
	}

	// ── Create client ─────────────────────────────────────────────────────────
	client, err := mq.NewClient(cfg)
	if err != nil {
		log.Fatalf("failed to create MQ client: %v", err)
	}
	defer client.Close()

	log.Printf("client created  queue-manager=%s  host=%s  request-queue=%s  mode=%s",
		cfg.QueueManagerName, cfg.ConnectionName, requestQueue, rrModeLabel(rrMode))

	// ── Start replier (Listener) ───────────────────────────────────────────────
	// The replier receives requests from MQ_REQUEST_QUEUE. For each request it
	// echoes the payload back (upper-cased) to msg.ReplyToQueue via a Sender.
	replierSender := client.NewSender("")
	replierListener := client.NewListener(requestQueue)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var repliedCount atomic.Int32
	listenerDone := make(chan error, 1)
	go func() {
		listenerDone <- replierListener.Start(ctx, func(_ context.Context, msg *mq.Message) error {
			replyTo := msg.ReplyToQueue
			if replyTo == "" {
				log.Printf("[replier] no ReplyToQueue set on request — skipping")
				return nil
			}
			body := string(msg.Body)
			response := fmt.Sprintf("ECHO: %s", strings.ToUpper(body))
			_, sendErr := replierSender.SendWithCreator(ctx, replyTo, func(reply *mq.Message) error {
				reply.Body = []byte(response)
				reply.CorrelationID = msg.CorrelationID
				return nil
			})
			if sendErr != nil {
				return fmt.Errorf("replier: failed to send response: %w", sendErr)
			}
			n := repliedCount.Add(1)
			log.Printf("[replier] #%-2d  correlId=%.16s…  replied=%q", n, msg.CorrelationID, response)
			return nil
		})
	}()

	// Give the replier a moment to connect before the requester sends.
	time.Sleep(600 * time.Millisecond)

	// ── Create RequestReplyClient ─────────────────────────────────────────────
	var rrClient *mq.RequestReplyClient
	if rrMode == "fixed" {
		rrClient = client.NewRequestReplyClientFixed(requestQueue, replyQueue)
		log.Printf("using FixedQueueSelector mode  reply-queue=%s", replyQueue)
	} else {
		rrClient = client.NewRequestReplyClient(requestQueue)
		log.Printf("using TemporaryQueue mode")
	}
	defer rrClient.Close()

	// ── Send requests and wait for replies ────────────────────────────────────
	for i := range msgCount {
		payload := fmt.Sprintf("request %d of %d — hello from commons-mq-go", i+1, msgCount)

		log.Printf("[requester] #%-2d  sending  payload=%q", i+1, payload)

		reply, rrErr := rrClient.RequestReply(context.Background(), payload, timeout)
		if rrErr != nil {
			log.Printf("[requester] #%-2d  ERROR: %v", i+1, rrErr)
			continue
		}
		log.Printf("[requester] #%-2d  got reply  correlId=%.16s…  body=%q",
			i+1, reply.CorrelationID, string(reply.Body))
	}

	log.Printf("all %d round-trips done — shutting down", msgCount)
	cancel()

	if err := <-listenerDone; err != nil {
		log.Printf("replier exited with error: %v", err)
	}

	// ── Health check ──────────────────────────────────────────────────────────
	status := client.Health()
	log.Printf("final health  healthy=%v", status.Healthy)
}

func rrModeLabel(mode string) string {
	if mode == "fixed" {
		return "FixedQueueSelector"
	}
	return "TemporaryQueue"
}

func configFromEnv() mq.Config {
	return mq.Config{
		QueueManagerName: requireEnv("MQ_QUEUE_MANAGER"),
		ConnectionName:   fmt.Sprintf("%s(%s)", requireEnv("MQ_HOST"), requireEnv("MQ_PORT")),
		ChannelName:      requireEnv("MQ_CHANNEL"),
		Username:         os.Getenv("MQ_USERNAME"),
		Password:         os.Getenv("MQ_PASSWORD"),
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
