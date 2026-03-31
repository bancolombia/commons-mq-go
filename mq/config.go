package mq

import (
	"errors"
	"fmt"
	"time"

	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

// Sentinel errors returned by Config validation and library operations.
var (
	// ErrInvalidConfig is returned when Config validation fails.
	ErrInvalidConfig = errors.New("commons-mq-go: invalid configuration")

	// ErrTimeout is returned when a RequestReply call exceeds its timeout.
	ErrTimeout = errors.New("commons-mq-go: request-reply timeout")

	// ErrConnectionFailed is returned when MaxRetries are exhausted.
	ErrConnectionFailed = errors.New("commons-mq-go: connection permanently failed")
)

// Config holds all settings for connecting to IBM MQ and configuring
// consumers, producers, and operational policies.
//
// Required fields: QueueManagerName, ConnectionName, ChannelName.
// All other fields have usable zero-value defaults (see [Config.applyDefaults]).
type Config struct {
	// QueueManagerName is the IBM MQ queue manager name (e.g. "QM1"). Required.
	QueueManagerName string

	// ConnectionName is the host and port in "hostname(port)" format. Required.
	ConnectionName string

	// ChannelName is the MQ server-connection channel (e.g. "DEV.APP.SVRCONN"). Required.
	ChannelName string

	// Username is the application user ID. Optional.
	Username string

	// Password is the application password. Optional. Never logged or echoed in errors.
	Password string

	// TLS holds optional TLS/SSL settings. nil means plaintext connection.
	TLS *TLSConfig

	// InputQueue is the default queue name for consumers.
	InputQueue string

	// OutputQueue is the default queue name for producers.
	OutputQueue string

	// InputConcurrency is the number of parallel consumer connections. Defaults to 1.
	InputConcurrency int

	// OutputConcurrency is the number of parallel producer connections. Defaults to 1.
	OutputConcurrency int

	// ProducerTTL is the message time-to-live. 0 means indefinite (no expiry).
	ProducerTTL time.Duration

	// MaxRetries is the maximum number of reconnect attempts before permanent failure.
	// Defaults to 10.
	MaxRetries int

	// InitialRetryInterval is the delay before the first reconnect attempt.
	// Defaults to 2 seconds.
	InitialRetryInterval time.Duration

	// RetryMultiplier is the exponential backoff multiplier for reconnect intervals.
	// Must be ≥ 1.0. Defaults to 2.0.
	RetryMultiplier float64

	// SetQueueManager, when true, sets the resolved queue manager on reply-to queues.
	// Useful in multi-queue-manager topologies.
	SetQueueManager bool

	// Telemetry carries optional OpenTelemetry providers for tracing and metrics.
	// When nil all instrumentation is no-op (zero overhead).
	Telemetry *TelemetryConfig
}

// TLSConfig holds TLS/SSL settings for the IBM MQ connection.
// It maps to the MQ MQSCO structure. Never include credential paths in error messages.
type TLSConfig struct {
	// KeyRepository is the path to the TLS key store directory (without extension).
	// Never logged or echoed in errors.
	KeyRepository string

	// CertificateLabel is the label of the certificate to use from the key store.
	// Never logged or echoed in errors.
	CertificateLabel string

	// CipherSpec is the TLS cipher specification (e.g. "TLS_RSA_WITH_AES_256_CBC_SHA256").
	CipherSpec string

	// PeerName is the expected Distinguished Name of the server certificate.
	PeerName string

	// FIPSRequired, when true, requires FIPS 140-2 compliant algorithms.
	FIPSRequired bool
}

// TelemetryConfig carries optional OpenTelemetry providers.
// When nil all instrumentation is no-op.
// Use the otel.NewTelemetryConfig helper to construct this with safe nil-handling.
type TelemetryConfig struct {
	// TracerProvider is the OTel tracer provider (go.opentelemetry.io/otel/trace).
	TracerProvider trace.TracerProvider

	// MeterProvider is the OTel meter provider (go.opentelemetry.io/otel/metric).
	MeterProvider metric.MeterProvider

	// Propagator is the W3C TraceContext propagator. When nil,
	// otel.GetTextMapPropagator() is used at instrumentation time.
	Propagator propagation.TextMapPropagator
}

// applyDefaults fills in zero-value fields with sensible defaults.
// Called by [NewClient] before validation.
func (c *Config) applyDefaults() {
	if c.InputConcurrency <= 0 {
		c.InputConcurrency = 1
	}
	if c.OutputConcurrency <= 0 {
		c.OutputConcurrency = 1
	}
	if c.MaxRetries <= 0 {
		c.MaxRetries = 10
	}
	if c.InitialRetryInterval <= 0 {
		c.InitialRetryInterval = 2 * time.Second
	}
	if c.RetryMultiplier < 1.0 {
		c.RetryMultiplier = 2.0
	}
}

// Validate checks that all required fields are set and all constrained fields
// are within their valid ranges. Returns an error wrapping [ErrInvalidConfig].
// The error message never contains the values of Password, TLS.KeyRepository,
// or TLS.CertificateLabel.
func (c *Config) Validate() error {
	var errs []string

	if c.QueueManagerName == "" {
		errs = append(errs, "QueueManagerName is required")
	}
	if c.ConnectionName == "" {
		errs = append(errs, "ConnectionName is required")
	}
	if c.ChannelName == "" {
		errs = append(errs, "ChannelName is required")
	}
	if c.InputConcurrency < 1 {
		errs = append(errs, "InputConcurrency must be ≥ 1")
	}
	if c.OutputConcurrency < 1 {
		errs = append(errs, "OutputConcurrency must be ≥ 1")
	}
	if c.MaxRetries < 0 {
		errs = append(errs, "MaxRetries must be ≥ 0")
	}
	if c.RetryMultiplier < 1.0 {
		errs = append(errs, "RetryMultiplier must be ≥ 1.0")
	}
	if c.ProducerTTL < 0 {
		errs = append(errs, "ProducerTTL must be ≥ 0")
	}

	if len(errs) == 0 {
		return nil
	}

	msg := errs[0]
	for _, e := range errs[1:] {
		msg = fmt.Sprintf("%s; %s", msg, e)
	}
	return fmt.Errorf("%w: %s", ErrInvalidConfig, msg)
}
