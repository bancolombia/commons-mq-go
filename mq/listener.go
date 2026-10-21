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
	onError     ListenerErrorHandler

	mu      sync.Mutex
	started bool
	closed  bool
	cancel  context.CancelFunc
	done    chan struct{}

	// states tracks the connection state of each consumer independently.
	// Initialized to [StateUnknown] in [Client.NewListener].
	states []atomic.Int32
}

// ListenerErrorHandler receives errors that occur while consuming.
//
// msg is non-nil when a message handler returned an error or panicked; msg is
// nil for consumer lifecycle errors, such as a consumer stopping after
// exhausting its reconnect attempts. The callback runs on the consumer
// goroutine and must not block indefinitely. A panic from the callback is
// recovered and discarded so it cannot stop the consumer.
type ListenerErrorHandler func(ctx context.Context, msg *Message, err error)

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

// WithListenerErrorHandler registers a callback invoked whenever a message
// handler fails (returns an error or panics) or a consumer stops on an error.
// See [ListenerErrorHandler].
func WithListenerErrorHandler(h ListenerErrorHandler) ListenerOption {
	return func(l *Listener) {
		l.onError = h
	}
}

// initStates allocates the per-consumer state slots. Called by
// [Client.NewListener] after functional options have set the concurrency.
func (l *Listener) initStates() {
	l.states = make([]atomic.Int32, l.concurrency)
	for i := range l.states {
		l.states[i].Store(int32(StateUnknown))
	}
}

// Start begins consuming messages from the queue. It opens [concurrency] independent
// IBM MQ connections, launches one consumer goroutine per connection, and blocks
// until all goroutines have exited.
//
// When ctx is cancelled all goroutines stop and Start returns nil. If goroutines
// fail to connect or exhaust their reconnect retries, Start returns a joined error.
// Partial consumer failures are reported through [WithListenerErrorHandler] and
// [Client.Health] while the remaining consumers keep running.
//
// Start may be called only once. It returns [ErrClientClosed] if [Client.Close]
// has already been called.
func (l *Listener) Start(ctx context.Context, handler MessageHandler) error {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return ErrClientClosed
	}
	if l.started {
		l.mu.Unlock()
		return fmt.Errorf("commons-mq-go: Listener.Start may only be called once")
	}
	l.started = true
	runCtx, cancel := context.WithCancel(ctx)
	l.cancel = cancel
	done := make(chan struct{})
	l.done = done
	l.mu.Unlock()

	defer close(done)
	defer cancel()

	errs := make(chan error, l.concurrency)
	var wg sync.WaitGroup

	for i := range l.concurrency {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			if err := l.runConsumer(runCtx, handler, idx); err != nil {
				errs <- err
			}
		}(i)
	}

	wg.Wait()
	close(errs)

	var errList []error
	for err := range errs {
		errList = append(errList, err)
	}
	return errors.Join(errList...)
}

// close cancels a running Start and waits for all consumer goroutines to exit.
// It is registered with the Client and invoked by [Client.Close].
func (l *Listener) close() error {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return nil
	}
	l.closed = true
	cancel := l.cancel
	done := l.done
	l.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
	return nil
}

// runConsumer manages a single consumer connection lifecycle. It opens a connection,
// starts the consume loop, and reconnects transparently on connection-level errors.
func (l *Listener) runConsumer(ctx context.Context, handler MessageHandler, idx int) (retErr error) {
	// Every exit path updates this consumer's observable state. Errors are also
	// reported to the application so partial failures are not silent.
	defer func() {
		if retErr != nil {
			l.setConsumerState(idx, StateFailed)
			l.reportError(ctx, nil, retErr)
			return
		}
		l.setConsumerState(idx, StateStopped)
	}()

	// Honour early cancellation before attempting any network dial.
	select {
	case <-ctx.Done():
		return nil
	default:
	}

	cfg := l.client.cfg
	cfg.MaxRetries = l.maxRetries

	l.setConsumerState(idx, StateReconnecting)
	conn, err := newConnectionWithRetry(ctx, &cfg)
	if err != nil {
		if ctx.Err() != nil {
			return nil // expected shutdown during retry — StateStopped
		}
		return err
	}
	defer conn.Close()
	l.setConsumerState(idx, StateConnected)
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
			l.setConsumerState(idx, StateReconnecting)
			if reconnErr := conn.Reconnect(ctx); reconnErr != nil {
				if ctx.Err() != nil {
					return nil // shutdown during reconnect — StateStopped
				}
				return reconnErr
			}
			l.setConsumerState(idx, StateConnected)
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
		handlerErr := invokeHandler(spanCtx, handler, msg)
		if handlerErr != nil {
			l.tel.incError(spanCtx, "handler_failed")
			l.reportError(spanCtx, msg, handlerErr)
		}
		l.tel.recordReceiveDuration(spanCtx, start)
		endSpan(span, handlerErr)
	}
}

// setConsumerState stores the state of consumer idx.
func (l *Listener) setConsumerState(idx int, state ConnectionState) {
	if idx >= 0 && idx < len(l.states) {
		l.states[idx].Store(int32(state))
	}
}

// reportError invokes the application error callback, protecting the consumer
// from a panicking callback.
func (l *Listener) reportError(ctx context.Context, msg *Message, err error) {
	if l.onError == nil || err == nil {
		return
	}
	defer func() { _ = recover() }()
	l.onError(ctx, msg, err)
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
// goroutine does not crash. The returned error is discarded; use
// [WithListenerErrorHandler] to observe handler failures.
//
// Exported to allow external packages (including test suites) to verify the panic
// recovery contract independently of a live IBM MQ connection.
func InvokeHandlerSafe(ctx context.Context, handler MessageHandler, msg *Message) {
	_ = invokeHandler(ctx, handler, msg)
}

// invokeHandler calls handler, converting a panic into an error wrapping
// [ErrHandlerPanic].
func invokeHandler(ctx context.Context, handler MessageHandler, msg *Message) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("%w: %v", ErrHandlerPanic, r)
		}
	}()
	return handler(ctx, msg)
}
