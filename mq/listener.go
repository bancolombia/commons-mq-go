package mq

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	ibmmq "github.com/ibm-messaging/mq-golang/v5/ibmmq"
)

// Listener consumes messages from a single IBM MQ queue using N concurrent connections.
// Create via [Client.NewListener]; start consuming with [Listener.Start].
type Listener struct {
	client      *Client
	queue       string
	concurrency int
	maxRetries  int
	queueOpts   QueueOptions
	tel         *mqTelemetry

	// lastConnState tracks the most recent connection state observed by runConsumer.
	// Stored as int32 so it can be read atomically (0=Connected, 1=Reconnecting, 2=Failed).
	lastConnState atomic.Int32
}

// ListenerOption is a functional option for configuring a [Listener].
type ListenerOption func(*Listener)

// WithListenerConcurrency overrides Config.InputConcurrency for this Listener.
// n must be ≥ 1; values < 1 are silently ignored.
func WithListenerConcurrency(n int) ListenerOption {
	return func(l *Listener) {
		if n >= 1 {
			l.concurrency = n
		}
	}
}

// WithListenerQueueOptions applies IBM MQ queue property customizations before
// consumers open the queue.
func WithListenerQueueOptions(opts QueueOptions) ListenerOption {
	return func(l *Listener) {
		l.queueOpts = opts
	}
}

// WithListenerMaxRetries overrides Config.MaxRetries for this Listener.
// n must be ≥ 0; values < 0 are silently ignored.
func WithListenerMaxRetries(n int) ListenerOption {
	return func(l *Listener) {
		if n >= 0 {
			l.maxRetries = n
		}
	}
}

// Start begins consuming messages from the queue. It opens [concurrency] independent
// IBM MQ connections, launches one consumer goroutine per connection, and blocks
// until all goroutines have exited.
//
// When ctx is cancelled all goroutines stop and Start returns nil. If goroutines
// fail to connect or exhaust their reconnect retries, Start returns a joined error.
//
// Start returns an error only if startup fails; runtime handler errors are logged
// and do not stop the consumer.
func (l *Listener) Start(ctx context.Context, handler MessageHandler) error {
	errs := make(chan error, l.concurrency)
	var wg sync.WaitGroup

	for range l.concurrency {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := l.runConsumer(ctx, handler); err != nil {
				errs <- err
			}
		}()
	}

	wg.Wait()
	close(errs)

	var errList []error
	for err := range errs {
		errList = append(errList, err)
	}
	return errors.Join(errList...)
}

// runConsumer manages a single consumer connection lifecycle. It opens a connection,
// starts the consume loop, and reconnects transparently on connection-level errors.
func (l *Listener) runConsumer(ctx context.Context, handler MessageHandler) error {
	// Honour early cancellation before attempting any network dial.
	select {
	case <-ctx.Done():
		return nil
	default:
	}

	cfg := l.client.cfg
	cfg.MaxRetries = l.maxRetries

	conn, err := newConnection(&cfg)
	if err != nil {
		l.lastConnState.Store(int32(StateFailed))
		return err
	}
	defer conn.Close()
	l.lastConnState.Store(int32(StateConnected))
	l.tel.incConnActive()
	defer l.tel.decConnActive()

	for {
		if ctx.Err() != nil {
			return nil
		}

		loopErr := l.consumeLoop(ctx, conn, handler)
		if loopErr == nil || ctx.Err() != nil {
			return nil
		}

		if IsConnectionError(loopErr) {
			l.lastConnState.Store(int32(StateReconnecting))
			if reconnErr := conn.Reconnect(); reconnErr != nil {
				l.lastConnState.Store(int32(StateFailed))
				return reconnErr
			}
			l.lastConnState.Store(int32(StateConnected))
			// Reconnected — reopen queue and resume.
			continue
		}
		return loopErr
	}
}

// consumeLoop opens the queue on the provided connection and runs a blocking
// GetSlice loop until ctx is cancelled or an error occurs.
func (l *Listener) consumeLoop(ctx context.Context, conn *connection, handler MessageHandler) error {
	mqod := ibmmq.NewMQOD()
	mqod.ObjectName = l.queue
	openOptions := int32(ibmmq.MQOO_INPUT_AS_Q_DEF | ibmmq.MQOO_FAIL_IF_QUIESCING)
	openOptions = applyQueueOpenOptions(l.queueOpts, openOptions)

	qObj, err := conn.QueueManager().Open(mqod, openOptions)
	if err != nil {
		return fmt.Errorf("commons-mq-go: failed to open queue %q: %w",
			l.queue, sanitizeError(err))
	}
	defer qObj.Close(0)

	buf := make([]byte, 4*1024*1024) // 4 MB receive buffer; GetSlice returns a sub-slice

	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		mqmd := ibmmq.NewMQMD()
		mqgmo := ibmmq.NewMQGMO()
		mqgmo.Options = ibmmq.MQGMO_WAIT | ibmmq.MQGMO_NO_SYNCPOINT
		mqgmo.MatchOptions = 0    // unconditional get
		mqgmo.WaitInterval = 1000 // 1-second poll interval in milliseconds

		data, _, err := qObj.GetSlice(mqmd, mqgmo, buf)
		if err != nil {
			if IsEmptyQueueError(err) {
				continue
			}
			return err
		}

		msg := messageFromMQMD(mqmd, data)

		// Extract trace context propagated from the sender into the message.
		msgCtx := l.tel.extractFromMessage(ctx, msg)

		// Create a consumer span as a child of the sender's span (if any).
		spanCtx, span := l.tel.startConsumerSpan(msgCtx, l.queue, msg.ID)
		start := time.Now()
		InvokeHandlerSafe(spanCtx, handler, msg)
		l.tel.recordReceiveDuration(spanCtx, start)
		span.End()
	}
}

// messageFromMQMD converts an IBM MQ MQMD and raw body bytes into a [Message].
// The body slice is copied so callers retain ownership of the receive buffer.
func messageFromMQMD(mqmd *ibmmq.MQMD, body []byte) *Message {
	bodyClone := make([]byte, len(body))
	copy(bodyClone, body)
	return &Message{
		ID:                  hex.EncodeToString(mqmd.MsgId),
		CorrelationID:       hex.EncodeToString(mqmd.CorrelId),
		ReplyToQueue:        mqmd.ReplyToQ,
		ReplyToQueueManager: mqmd.ReplyToQMgr,
		Body:                bodyClone,
		UserProperties:      make(map[string]string),
	}
}

// InvokeHandlerSafe calls handler and recovers from any panic so the consumer
// goroutine does not crash. A recovered panic is silently discarded; the goroutine
// continues to the next message.
//
// Exported to allow external packages (including test suites) to verify the panic
// recovery contract independently of a live IBM MQ connection.
func InvokeHandlerSafe(ctx context.Context, handler MessageHandler, msg *Message) {
	defer func() {
		_ = recover()
	}()
	_ = handler(ctx, msg)
}
