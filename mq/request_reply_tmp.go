package mq

import (
	"context"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/bancolombia/commons-mq-go/internal/correlation"
	ibmmq "github.com/ibm-messaging/mq-golang/v5/ibmmq"
)

// RequestReplyClient performs synchronous-style request-reply over IBM MQ.
//
// Two modes are supported:
//
//   - [TemporaryQueue] (created by [Client.NewRequestReplyClient]): each call
//     opens a unique dynamic reply queue. The queue is auto-deleted by the queue
//     manager when the connection is closed.
//
//   - [FixedQueueSelector] (created by [Client.NewRequestReplyClientFixed]):
//     responses are received from a shared, pre-existing reply queue and routed
//     to the correct caller by MQMD.CorrelId using MQMO_MATCH_CORREL_ID.
//
// Both modes are safe for concurrent use from multiple goroutines.
type RequestReplyClient struct {
	client       *Client
	requestQueue string // queue to which requests are sent
	replyQueue   string // fixed reply queue (FixedQueueSelector mode only)
	mode         RequestReplyMode
	registry     *correlation.Registry[*Message] // non-nil only in FixedQueueSelector mode
	tel          *mqTelemetry
	closed       bool
}

// RROption is a functional option for configuring a [RequestReplyClient].
type RROption func(*RequestReplyClient)

// WithRRReplyQueue overrides the fixed reply queue name for [FixedQueueSelector] mode.
// Has no effect in [TemporaryQueue] mode.
func WithRRReplyQueue(queue string) RROption {
	return func(r *RequestReplyClient) {
		if r.mode == FixedQueueSelector {
			r.replyQueue = queue
		}
	}
}

// RequestReply sends payload as a request to the configured request queue and
// waits for a correlated response via a dynamically-created temporary reply queue.
//
// RequestReply sends payload as a request and waits for a correlated response.
//
// In [TemporaryQueue] mode a dynamic reply queue is created per call.
// In [FixedQueueSelector] mode a shared fixed reply queue is used with
// per-caller MQMD.CorrelId filtering.
//
// The call blocks until a response arrives or timeout elapses. On timeout it
// returns [ErrTimeout]. Context cancellation propagates as [context.Canceled] or
// [context.DeadlineExceeded].
func (r *RequestReplyClient) RequestReply(ctx context.Context, payload string, timeout time.Duration) (*Message, error) {
	return r.RequestReplyWithCreator(ctx, func(msg *Message) error {
		msg.Body = []byte(payload)
		return nil
	}, timeout)
}

// RequestReplyWithCreator sends a fully customized request (via [MessageCreator])
// and waits for a correlated response. The creator is called with a pre-populated
// [Message]; any mutations (body, correlation ID, TTL, user properties) are applied
// to the outgoing MQMD. Returns [ErrTimeout] if no response arrives within timeout.
func (r *RequestReplyClient) RequestReplyWithCreator(ctx context.Context, creator MessageCreator, timeout time.Duration) (*Message, error) {
	// Honour early cancellation.
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	// ── OTel: client span wrapping the full round-trip ────────────────────────
	spanCtx, span := r.tel.startClientSpan(ctx, r.requestQueue)
	defer func() { span.End() }()

	// Wrap creator to inject OTel context into the outgoing request.
	wrappedCreator := func(msg *Message) error {
		if creator != nil {
			if err := creator(msg); err != nil {
				return err
			}
		}
		r.tel.injectToMessage(spanCtx, msg)
		return nil
	}

	conn, err := newConnection(&r.client.cfg)
	if err != nil {
		span.RecordError(err)
		return nil, err
	}
	defer conn.Close()

	var reply *Message
	if r.mode == FixedQueueSelector {
		reply, err = requestReplyViaFixedQueue(conn, r.requestQueue, r.replyQueue, wrappedCreator, r.registry, timeout)
	} else {
		reply, err = requestReplyViaTempQueue(conn, r.requestQueue, wrappedCreator, timeout)
	}
	if err != nil {
		span.RecordError(err)
	}
	return reply, err
}

// Close releases any resources held by this RequestReplyClient.
// It is safe to call Close multiple times.
func (r *RequestReplyClient) Close() error {
	r.closed = true
	return nil
}

// requestReplyViaTempQueue implements the core TemporaryQueue request-reply flow:
//  1. Open a model/dynamic queue to get a unique temporary reply queue.
//  2. Build the outgoing MQMD with ReplyToQ = resolved temp queue name.
//  3. Call the MessageCreator to populate / customize the message.
//  4. PUT the request to requestQueue.
//  5. Block on GET from the temp queue with a WaitInterval = timeout.
//  6. Return the response message or ErrTimeout.
func requestReplyViaTempQueue(
	conn *connection,
	requestQueue string,
	creator MessageCreator,
	timeout time.Duration,
) (*Message, error) {
	qm := conn.QueueManager()

	// ── 1. Open dynamic (temporary) reply queue ───────────────────────────────
	// DynamicQName = "AMQ.*" tells IBM MQ to allocate a unique queue with the
	// "AMQ." prefix. The resolved name is written back into MQOD.ResolvedQName.
	replyOD := ibmmq.NewMQOD()
	replyOD.ObjectName = "SYSTEM.DEFAULT.MODEL.QUEUE"
	replyOD.DynamicQName = "AMQ.*"

	// MQOO_INPUT_EXCLUSIVE on the temp queue ensures only this process can receive
	// from it; MQOO_FAIL_IF_QUIESCING avoids waiting during queue-manager shutdown.
	replyQObj, err := qm.Open(replyOD,
		ibmmq.MQOO_INPUT_EXCLUSIVE|ibmmq.MQOO_FAIL_IF_QUIESCING)
	if err != nil {
		return nil, fmt.Errorf("commons-mq-go: failed to open temporary reply queue: %w",
			sanitizeError(err))
	}
	defer replyQObj.Close(0)

	// IBM MQ writes the allocated dynamic queue name back into ObjectName after MQOPEN.
	replyQueueName := replyOD.ObjectName

	// ── 2. Build outgoing message ─────────────────────────────────────────────
	msg := &Message{
		ReplyToQueue:   replyQueueName,
		UserProperties: make(map[string]string),
	}
	if creator != nil {
		if err := creator(msg); err != nil {
			return nil, fmt.Errorf("commons-mq-go: MessageCreator returned error: %w", err)
		}
	}

	// ── 3. Open request queue and PUT ────────────────────────────────────────
	reqOD := ibmmq.NewMQOD()
	reqOD.ObjectName = requestQueue
	reqQObj, err := qm.Open(reqOD, ibmmq.MQOO_OUTPUT|ibmmq.MQOO_FAIL_IF_QUIESCING)
	if err != nil {
		return nil, fmt.Errorf("commons-mq-go: failed to open request queue %q: %w",
			requestQueue, sanitizeError(err))
	}
	defer reqQObj.Close(0)

	mqmd := ibmmq.NewMQMD()
	mqpmo := ibmmq.NewMQPMO()
	mqpmo.Options = ibmmq.MQPMO_NEW_MSG_ID | ibmmq.MQPMO_NO_SYNCPOINT

	mqmd.ReplyToQ = replyQueueName

	// TTL from per-message value.
	if msg.TTL > 0 {
		mqmd.Expiry = int32(msg.TTL / (100 * time.Millisecond))
	}

	// CorrelationID from MessageCreator.
	if msg.CorrelationID != "" {
		if decoded, decErr := hex.DecodeString(msg.CorrelationID); decErr == nil && len(decoded) <= 24 {
			copy(mqmd.CorrelId, decoded)
		}
	}

	if err := reqQObj.Put(mqmd, mqpmo, msg.Body); err != nil {
		return nil, fmt.Errorf("commons-mq-go: failed to put request message to queue %q: %w",
			requestQueue, sanitizeError(err))
	}

	// ── 4. GET response from temp reply queue ─────────────────────────────────
	// WaitInterval is in milliseconds; cap at 24-day IBM MQ maximum (MQWI_UNLIMITED
	// would block indefinitely, so we use the caller-specified timeout converted to ms).
	waitMS := int32(timeout.Milliseconds())
	if waitMS <= 0 {
		waitMS = 1
	}

	getMD := ibmmq.NewMQMD()
	getGMO := ibmmq.NewMQGMO()
	getGMO.Options = ibmmq.MQGMO_WAIT | ibmmq.MQGMO_NO_SYNCPOINT
	getGMO.WaitInterval = waitMS

	buf := make([]byte, 4*1024*1024)
	data, _, err := replyQObj.GetSlice(getMD, getGMO, buf)
	if err != nil {
		if IsEmptyQueueError(err) {
			return nil, ErrTimeout
		}
		return nil, fmt.Errorf("commons-mq-go: failed to receive reply message: %w",
			sanitizeError(err))
	}

	body := make([]byte, len(data))
	copy(body, data)
	return &Message{
		ID:                  hex.EncodeToString(getMD.MsgId),
		CorrelationID:       hex.EncodeToString(getMD.CorrelId),
		ReplyToQueue:        getMD.ReplyToQ,
		ReplyToQueueManager: getMD.ReplyToQMgr,
		Body:                body,
	}, nil
}
