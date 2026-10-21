package mq

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	ibmmq "github.com/ibm-messaging/mq-golang/v5/ibmmq"
)

// requestReplyViaFixedQueue implements the FixedQueueSelector request-reply flow:
//
//  1. Generate a unique 24-byte correlation ID (or use the one supplied by the
//     MessageCreator).
//  2. Build the outgoing message and let the MessageCreator customize it.
//  3. Open the shared reply queue (MQOO_INPUT_SHARED) and the request queue.
//  4. PUT the request with MQMD.CorrelId and MQMD.ReplyToQ set.
//  5. Block on GET from the reply queue with MQMO_MATCH_CORREL_ID for at most
//     timeout; return ErrTimeout when no matching reply arrives.
//
// Every MQI call runs on the caller's goroutine. The Go binding establishes
// connections with MQCNO_HANDLE_SHARE_NO_BLOCK, so making concurrent calls on a
// single connection (for example a blocking GET alongside an MQOPEN) fails with
// MQRC_CALL_IN_PROGRESS. Each RequestReply call owns its connection, so keeping
// the flow synchronous keeps every connection handle single-threaded.
//
// Multiple concurrent callers can safely share the same fixed reply queue because
// IBM MQ routes each GET to the message whose CorrelId matches the filter.
func requestReplyViaFixedQueue(
	conn *connection,
	requestQueue, replyQueue string,
	creator MessageCreator,
	timeout time.Duration,
) (*Message, error) {
	qm := conn.QueueManager()

	// ── 1. Generate unique correlation ID ─────────────────────────────────────
	var correlBytes [24]byte
	if _, err := rand.Read(correlBytes[:]); err != nil {
		return nil, fmt.Errorf("commons-mq-go: failed to generate correlation ID: %w", err)
	}

	// ── 2. Build outgoing message and invoke the creator ──────────────────────
	// The creator runs before any MQI call so a custom CorrelationID is used
	// consistently by both the request MQMD and the reply GET filter.
	msg := &Message{
		CorrelationID:  hex.EncodeToString(correlBytes[:]),
		ReplyToQueue:   replyQueue,
		UserProperties: make(map[string]string),
	}
	if creator != nil {
		if err := creator(msg); err != nil {
			return nil, fmt.Errorf("commons-mq-go: MessageCreator returned error: %w", err)
		}
	}

	// A creator-supplied correlation ID becomes the routing key.
	if msg.CorrelationID != "" {
		decoded, decErr := hex.DecodeString(msg.CorrelationID)
		if decErr != nil || len(decoded) > len(correlBytes) {
			return nil, fmt.Errorf(
				"commons-mq-go: invalid correlation ID %q: must be an even-length hex string of at most %d characters",
				msg.CorrelationID, len(correlBytes)*2)
		}
		correlBytes = [24]byte{}
		copy(correlBytes[:], decoded)
	}

	// ── 3. Open the shared reply queue ────────────────────────────────────────
	replyOD := ibmmq.NewMQOD()
	replyOD.ObjectName = replyQueue
	replyQObj, err := qm.Open(replyOD,
		ibmmq.MQOO_INPUT_SHARED|ibmmq.MQOO_FAIL_IF_QUIESCING)
	if err != nil {
		return nil, fmt.Errorf("commons-mq-go: failed to open fixed reply queue %q: %w",
			replyQueue, sanitizeError(err))
	}
	defer replyQObj.Close(0)

	// ── 4. Open request queue and PUT ─────────────────────────────────────────
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
	if conn.config.SetQueueManager {
		mqmd.ReplyToQMgr = conn.config.QueueManagerName
	}

	if msg.TTL > 0 {
		mqmd.Expiry = int32(msg.TTL / (100 * time.Millisecond))
	}

	if err := reqQObj.Put(mqmd, mqpmo, msg.Body); err != nil {
		return nil, fmt.Errorf("commons-mq-go: failed to put request to queue %q: %w",
			requestQueue, sanitizeError(err))
	}

	// ── 5. GET the correlated reply ───────────────────────────────────────────
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
	data, _, err := replyQObj.GetSlice(getMD, getGMO, buf)
	if err != nil {
		if IsEmptyQueueError(err) {
			return nil, ErrTimeout
		}
		return nil, fmt.Errorf("commons-mq-go: failed to receive fixed-queue reply: %w",
			sanitizeError(err))
	}

	return messageFromMQMD(getMD, data), nil
}
