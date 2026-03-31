package mq

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/bancolombia/commons-mq-go/internal/correlation"
	ibmmq "github.com/ibm-messaging/mq-golang/v5/ibmmq"
)

// requestReplyViaFixedQueue implements the FixedQueueSelector request-reply flow:
//
//  1. Generate a unique 24-byte correlation ID.
//  2. Register the correlation ID in the registry → get a response channel.
//  3. Spawn a background goroutine that opens replyQueue (MQOO_INPUT_SHARED)
//     and blocks on GetSlice with MQMO_MATCH_CORREL_ID; on success it delivers
//     the response via the registry and exits.
//  4. PUT the request to requestQueue with MQMD.CorrelId and MQMD.ReplyToQ set.
//  5. Block on the response channel with a timeout (via context + time.After).
//  6. Deregister the correlation ID regardless of outcome.
//
// Multiple concurrent callers can safely share the same fixed reply queue because
// IBM MQ routes each GET to the message whose CorrelId matches the filter.
func requestReplyViaFixedQueue(
	conn *connection,
	requestQueue, replyQueue string,
	creator MessageCreator,
	reg *correlation.Registry[*Message],
	timeout time.Duration,
) (*Message, error) {
	qm := conn.QueueManager()

	// ── 1. Generate unique correlation ID ─────────────────────────────────────
	var correlBytes [24]byte
	if _, err := rand.Read(correlBytes[:]); err != nil {
		return nil, fmt.Errorf("commons-mq-go: failed to generate correlation ID: %w", err)
	}
	correlHex := hex.EncodeToString(correlBytes[:])

	// ── 2. Register correlation ID → response channel ─────────────────────────
	respCh := reg.Register(correlHex)
	defer reg.Deregister(correlHex)

	// ── 3. Spawn background goroutine to GET from the fixed reply queue ───────
	// The goroutine opens its own handle to the reply queue so that concurrent
	// callers do not contend on a single queue object.
	errCh := make(chan error, 1)
	go func() {
		replyOD := ibmmq.NewMQOD()
		replyOD.ObjectName = replyQueue

		replyQObj, err := qm.Open(replyOD,
			ibmmq.MQOO_INPUT_SHARED|ibmmq.MQOO_FAIL_IF_QUIESCING)
		if err != nil {
			errCh <- fmt.Errorf("commons-mq-go: failed to open fixed reply queue %q: %w",
				replyQueue, sanitizeError(err))
			return
		}
		defer replyQObj.Close(0)

		// Set up GET descriptor with correlation ID filter.
		getMD := ibmmq.NewMQMD()
		copy(getMD.CorrelId, correlBytes[:])

		getGMO := ibmmq.NewMQGMO()
		getGMO.Options = ibmmq.MQGMO_WAIT | ibmmq.MQGMO_NO_SYNCPOINT
		getGMO.MatchOptions = ibmmq.MQMO_MATCH_CORREL_ID
		waitMS := int32(timeout.Milliseconds())
		if waitMS <= 0 {
			waitMS = 1
		}
		getGMO.WaitInterval = waitMS

		buf := make([]byte, 4*1024*1024)
		data, _, getErr := replyQObj.GetSlice(getMD, getGMO, buf)
		if getErr != nil {
			if IsEmptyQueueError(getErr) {
				// Timeout — no-op; the main goroutine will surface ErrTimeout.
				return
			}
			errCh <- fmt.Errorf("commons-mq-go: failed to receive fixed-queue reply: %w",
				sanitizeError(getErr))
			return
		}

		body := make([]byte, len(data))
		copy(body, data)
		msg := &Message{
			ID:                  hex.EncodeToString(getMD.MsgId),
			CorrelationID:       hex.EncodeToString(getMD.CorrelId),
			ReplyToQueue:        getMD.ReplyToQ,
			ReplyToQueueManager: getMD.ReplyToQMgr,
			Body:                body,
		}
		reg.Deliver(correlHex, msg)
	}()

	// ── 4. Build outgoing message ─────────────────────────────────────────────
	msg := &Message{
		CorrelationID:  correlHex,
		ReplyToQueue:   replyQueue,
		UserProperties: make(map[string]string),
	}
	if creator != nil {
		if err := creator(msg); err != nil {
			return nil, fmt.Errorf("commons-mq-go: MessageCreator returned error: %w", err)
		}
		// If the creator changed the CorrelationID, update the registry mapping.
		if msg.CorrelationID != correlHex {
			reg.Deregister(correlHex)
			correlHex = msg.CorrelationID
			respCh = reg.Register(correlHex)
			defer reg.Deregister(correlHex)
			// Reparse the correlation bytes for MQMD.
			if decoded, decErr := hex.DecodeString(correlHex); decErr == nil && len(decoded) <= 24 {
				copy(correlBytes[:], decoded)
			}
		}
	}

	// ── 5. Open request queue and PUT ─────────────────────────────────────────
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

	copy(mqmd.CorrelId, correlBytes[:])
	mqmd.ReplyToQ = replyQueue

	if msg.TTL > 0 {
		mqmd.Expiry = int32(msg.TTL / (100 * time.Millisecond))
	}

	if err := reqQObj.Put(mqmd, mqpmo, msg.Body); err != nil {
		return nil, fmt.Errorf("commons-mq-go: failed to put request to queue %q: %w",
			requestQueue, sanitizeError(err))
	}

	// ── 6. Block for response (or timeout) ────────────────────────────────────
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case resp := <-respCh:
		return resp, nil
	case err := <-errCh:
		return nil, err
	case <-timer.C:
		return nil, ErrTimeout
	}
}

// newFixedRRRegistry creates the per-instance correlation registry used by
// FixedQueueSelector RequestReplyClients.
func newFixedRRRegistry() *correlation.Registry[*Message] {
	return correlation.NewRegistry[*Message]()
}
