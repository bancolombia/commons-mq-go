package mq

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	ibmmq "github.com/ibm-messaging/mq-golang/v5/ibmmq"
	"go.opentelemetry.io/otel/attribute"
)

// Sender publishes messages to IBM MQ queues using a managed connection pool.
// Create via [Client.NewSender].
type Sender struct {
	client   *Client
	queue    string           // default destination queue
	pool     chan *connection // buffered channel acting as connection pool
	mu       sync.Mutex
	created  int  // number of connections created so far
	maxConns int  // pool capacity = concurrency
	closed   bool // set once by close()
	tel      *mqTelemetry
}

// SenderOption is a functional option for configuring a [Sender].
type SenderOption func(*Sender)

// WithSenderConcurrency overrides Config.OutputConcurrency for this Sender.
// n must be ≥ 1; values < 1 are silently ignored.
func WithSenderConcurrency(n int) SenderOption {
	return func(s *Sender) {
		if n >= 1 {
			s.maxConns = n
			s.pool = make(chan *connection, n)
		}
	}
}

// Send sends payload to the default output queue and returns the assigned
// message ID (hex-encoded MQMD.MsgId) or an error.
func (s *Sender) Send(ctx context.Context, payload string) (string, error) {
	return s.SendWithCreator(ctx, "", func(msg *Message) error {
		msg.Body = []byte(payload)
		return nil
	})
}

// SendTo sends payload to the named destination queue and returns the assigned
// message ID or an error.
func (s *Sender) SendTo(ctx context.Context, destination, payload string) (string, error) {
	return s.SendWithCreator(ctx, destination, func(msg *Message) error {
		msg.Body = []byte(payload)
		return nil
	})
}

// SendWithCreator sends a message constructed by the provided [MessageCreator].
// If destination is empty, the Sender's default output queue is used.
// Returns the assigned message ID (hex-encoded MQMD.MsgId) or an error.
// Returns an error if no destination queue can be resolved.
func (s *Sender) SendWithCreator(ctx context.Context, destination string, creator MessageCreator) (string, error) {
	if destination == "" {
		destination = s.queue
	}
	if destination == "" {
		return "", fmt.Errorf("commons-mq-go: no destination queue specified and Config.OutputQueue is empty")
	}

	// ── OTel: producer span + inject context ──────────────────────────────────
	spanCtx, span := s.tel.startProducerSpan(ctx, destination)
	start := time.Now()

	wrappedCreator := func(msg *Message) error {
		if creator != nil {
			if err := creator(msg); err != nil {
				return err
			}
		}
		// Inject span context into outgoing message user properties.
		s.tel.injectToMessage(spanCtx, msg)
		return nil
	}

	conn, err := s.acquireConn(ctx)
	if err != nil {
		s.tel.incError(ctx, "connection_failed")
		endSpan(span, err)
		return "", err
	}

	msgID, putErr := s.put(conn, destination, wrappedCreator)

	if putErr != nil && IsConnectionError(putErr) {
		// Attempt transparent reconnect then retry the send once.
		if reconnErr := conn.Reconnect(); reconnErr != nil {
			// Reconnect exhausted retries — discard the connection permanently.
			_ = conn.Close()
			s.mu.Lock()
			s.created--
			s.mu.Unlock()
			s.tel.decConnActive()
			s.tel.incError(spanCtx, "connection_failed")
			endSpan(span, reconnErr)
			return "", reconnErr
		}
		// Reconnected — retry the put on the refreshed connection.
		msgID, putErr = s.put(conn, destination, wrappedCreator)
		if putErr != nil {
			_ = conn.Close()
			s.mu.Lock()
			s.created--
			s.mu.Unlock()
			s.tel.decConnActive()
			s.tel.incError(spanCtx, "put_failed")
			endSpan(span, putErr)
			return "", putErr
		}
	}

	s.tel.recordPublishDuration(spanCtx, start)
	if putErr != nil {
		s.tel.incError(spanCtx, "put_failed")
	} else if msgID != "" {
		span.SetAttributes(attribute.String("messaging.message.id", msgID))
	}
	endSpan(span, putErr)

	s.releaseConn(conn)
	return msgID, putErr
}

// put opens the destination queue on conn, calls the MessageCreator, and invokes
// MQPUT. It returns the hex-encoded MQMD.MsgId assigned by the queue manager.
func (s *Sender) put(conn *connection, queueName string, creator MessageCreator) (string, error) {
	mqod := ibmmq.NewMQOD()
	mqod.ObjectName = queueName
	qObj, err := conn.QueueManager().Open(mqod, ibmmq.MQOO_OUTPUT|ibmmq.MQOO_FAIL_IF_QUIESCING)
	if err != nil {
		return "", fmt.Errorf("commons-mq-go: failed to open queue %q for output: %w",
			queueName, sanitizeError(err))
	}
	defer qObj.Close(0)

	// Build the outgoing Message for the creator to populate or modify.
	msg := &Message{
		UserProperties: make(map[string]string),
	}
	if creator != nil {
		if err := creator(msg); err != nil {
			return "", fmt.Errorf("commons-mq-go: MessageCreator returned error: %w", err)
		}
	}

	mqmd := ibmmq.NewMQMD()
	mqpmo := ibmmq.NewMQPMO()
	mqpmo.Options = ibmmq.MQPMO_NEW_MSG_ID | ibmmq.MQPMO_NO_SYNCPOINT

	// TTL: per-message value takes precedence; fall back to Config.ProducerTTL.
	// MQMD.Expiry is in 1/10ths of a second; MQEI_UNLIMITED (the default) means no expiry.
	ttl := msg.TTL
	if ttl == 0 {
		ttl = s.client.cfg.ProducerTTL
	}
	if ttl > 0 {
		mqmd.Expiry = int32(ttl / (100 * time.Millisecond))
	}

	// Apply CorrelationID if set by the creator (hex-encoded 24-byte value).
	if msg.CorrelationID != "" {
		if decoded, decErr := hex.DecodeString(msg.CorrelationID); decErr == nil && len(decoded) <= 24 {
			copy(mqmd.CorrelId, decoded)
		}
	}

	// Apply reply-to routing fields from the creator.
	mqmd.ReplyToQ = msg.ReplyToQueue
	mqmd.ReplyToQMgr = msg.ReplyToQueueManager

	if err := qObj.Put(mqmd, mqpmo, msg.Body); err != nil {
		return "", fmt.Errorf("commons-mq-go: failed to put message to queue %q: %w",
			queueName, sanitizeError(err))
	}

	return hex.EncodeToString(mqmd.MsgId), nil
}

// acquireConn returns an idle connection from the pool, creating a new one if the
// pool is empty and the creation limit has not been reached, or blocks until one
// is returned. Respects ctx cancellation at every wait point.
func (s *Sender) acquireConn(ctx context.Context) (*connection, error) {
	// Honour early cancellation before any allocation attempt.
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	// Non-blocking: grab an idle connection if one is available immediately.
	select {
	case conn := <-s.pool:
		return conn, nil
	default:
	}

	// Create a new connection if under the concurrency limit.
	s.mu.Lock()
	canCreate := s.created < s.maxConns
	if canCreate {
		s.created++
	}
	s.mu.Unlock()

	if canCreate {
		conn, err := newConnection(&s.client.cfg)
		if err != nil {
			s.mu.Lock()
			s.created--
			s.mu.Unlock()
			return nil, err
		}
		s.tel.incConnActive()
		return conn, nil
	}

	// All connections are in use — block until one is returned or ctx fires.
	select {
	case conn := <-s.pool:
		return conn, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// releaseConn returns a healthy connection to the pool. If the Sender has been
// closed it closes the connection instead to avoid leaks.
func (s *Sender) releaseConn(conn *connection) {
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		_ = conn.Close()
		return
	}
	s.pool <- conn
}

// close drains the connection pool and closes each connection.
// Registered with Client so it is called automatically by [Client.Close].
func (s *Sender) close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	created := s.created
	s.mu.Unlock()

	var errs []error
	// Drain connections that are currently idle in the pool.
	for range created {
		select {
		case conn := <-s.pool:
			if err := conn.Close(); err != nil {
				errs = append(errs, err)
			}
		default:
			// Connection is currently checked out and will be closed by releaseConn.
		}
	}
	return errors.Join(errs...)
}
