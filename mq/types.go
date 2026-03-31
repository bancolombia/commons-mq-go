package mq

import (
	"context"
	"time"
)

// ConnectionState represents the lifecycle state of an IBM MQ connection.
type ConnectionState int

const (
	// StateConnected means the connection is established and operational.
	StateConnected ConnectionState = iota

	// StateReconnecting means the connection was lost and a reconnect is in progress.
	StateReconnecting

	// StateFailed means reconnection attempts were exhausted; the connection is
	// permanently down and the Client should be discarded.
	StateFailed
)

// RequestReplyMode determines the mechanism used for request-reply correlation.
type RequestReplyMode int

const (
	// TemporaryQueue opens a unique dynamic reply queue per RequestReplyClient instance.
	// The temp queue is automatically deleted when the connection is closed.
	TemporaryQueue RequestReplyMode = iota

	// FixedQueueSelector uses a shared, pre-existing reply queue. Responses are
	// routed to the correct caller using MQMD.CorrelId and MQMO_MATCH_CORREL_ID.
	FixedQueueSelector
)

// MessageHandler is invoked once per received message.
//
// The context carries the OTel span extracted from the message's propagation headers
// (when Telemetry is configured), allowing callers to create child spans.
//
// Returning a non-nil error marks the processing as failed. The library logs the error
// and continues consuming subsequent messages (at-most-once delivery).
type MessageHandler func(ctx context.Context, msg *Message) error

// MessageCreator is called immediately before a message is sent, allowing the caller
// to modify headers, set a custom correlation ID, or add user properties.
// Any mutation to msg is applied to the outgoing IBM MQ MQMD and body.
type MessageCreator func(msg *Message) error

// Message abstracts an IBM MQ message, wrapping the MQMD descriptor and raw body bytes.
type Message struct {
	// ID is the hex-encoded MQMD.MsgId assigned by the queue manager on Put.
	ID string

	// CorrelationID is the hex-encoded MQMD.CorrelId.
	CorrelationID string

	// ReplyToQueue is the MQMD.ReplyToQ field, used in request-reply patterns.
	ReplyToQueue string

	// ReplyToQueueManager is the MQMD.ReplyToQMgr field.
	ReplyToQueueManager string

	// Body is the raw message payload.
	Body []byte

	// UserProperties holds arbitrary string key-value pairs attached to the message.
	// The library uses this map to carry W3C TraceContext headers ("traceparent",
	// "tracestate") when OTel propagation is enabled.
	UserProperties map[string]string

	// TTL overrides Config.ProducerTTL for this specific message. 0 means use the
	// Config default.
	TTL time.Duration
}

// QueueOptions applies optional IBM MQ property customizations before consumers start.
// Set only the fields you need; zero values leave the queue properties unchanged.
type QueueOptions struct {
	// TargetClient sets the WMQ_TARGET_CLIENT property (non-Commons MQ client mode).
	TargetClient bool

	// MQMDReadEnabled enables MQMD read via WMQ_MQMD_READ_ENABLED.
	MQMDReadEnabled bool

	// MQMDWriteEnabled enables MQMD write via WMQ_MQMD_WRITE_ENABLED.
	MQMDWriteEnabled bool

	// PutAsyncAllowed enables asynchronous put via WMQ_PUT_ASYNC_ALLOWED_ENABLED.
	PutAsyncAllowed bool

	// ReadAheadAllowed enables read-ahead prefetch via WMQ_READ_AHEAD_ALLOWED_ENABLED.
	ReadAheadAllowed bool
}

// HealthStatus reports the aggregate live/dead state of all library-managed IBM MQ connections.
type HealthStatus struct {
	// Healthy is true only when every active connection is alive.
	Healthy bool

	// Details contains per-connection health information.
	Details []ConnectionStatus
}

// ConnectionStatus reports the health of a single IBM MQ connection.
type ConnectionStatus struct {
	// QueueManager is the queue manager name for this connection.
	QueueManager string

	// Alive is true when the connection is currently active and operational.
	Alive bool

	// Reason is a human-readable description of why the connection is not alive.
	// Empty when Alive is true.
	Reason string
}
