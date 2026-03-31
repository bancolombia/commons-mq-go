package otel

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// Instruments holds the four OTel metric instruments used by the library.
// Create via [NewInstruments].
type Instruments struct {
	// PublishDuration records the time from Put call start to Put return (ms).
	PublishDuration metric.Float64Histogram

	// ReceiveDuration records the time from message available to handler return (ms).
	ReceiveDuration metric.Float64Histogram

	// ConnectionActive is an observable gauge reporting the count of live
	// MQ connections. Wire the callback to an atomic counter incremented on
	// connect and decremented on close.
	ConnectionActive metric.Int64ObservableGauge

	// ErrorsTotal counts errors labeled by "error.type".
	ErrorsTotal metric.Int64Counter
}

// NewInstruments creates and registers all four OTel metric instruments from mp.
// The activeConnFn callback is invoked at each metrics scrape to report the
// current number of live MQ connections; pass nil to always report 0.
//
// Returns an error only if the meter provider rejects instrument registration
// (which does not happen with standard SDK or noop providers).
func NewInstruments(
	mp metric.MeterProvider,
	activeConnFn func() int64,
) (*Instruments, error) {
	m := mp.Meter("github.com/bancolombia/commons-mq-go")

	publishDur, err := m.Float64Histogram(
		"messaging.publish.duration",
		metric.WithDescription("Time from Put call start to Put return"),
		metric.WithUnit("ms"),
	)
	if err != nil {
		return nil, err
	}

	receiveDur, err := m.Float64Histogram(
		"messaging.receive.duration",
		metric.WithDescription("Time from message available to handler return"),
		metric.WithUnit("ms"),
	)
	if err != nil {
		return nil, err
	}

	if activeConnFn == nil {
		activeConnFn = func() int64 { return 0 }
	}
	connFn := activeConnFn // capture for closure

	connActive, err := m.Int64ObservableGauge(
		"messaging.connection.active",
		metric.WithDescription("Count of live MQ connections"),
		metric.WithUnit("{connection}"),
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			o.Observe(connFn())
			return nil
		}),
	)
	if err != nil {
		return nil, err
	}

	errorsTotal, err := m.Int64Counter(
		"messaging.errors.total",
		metric.WithDescription("Count of errors labeled by error.type"),
		metric.WithUnit("{error}"),
	)
	if err != nil {
		return nil, err
	}

	return &Instruments{
		PublishDuration:  publishDur,
		ReceiveDuration:  receiveDur,
		ConnectionActive: connActive,
		ErrorsTotal:      errorsTotal,
	}, nil
}

// RecordError adds 1 to ErrorsTotal with the given errType label.
// errType should be a stable identifier such as "connection_failed" or "timeout".
func (ins *Instruments) RecordError(ctx context.Context, errType string) {
	ins.ErrorsTotal.Add(ctx, 1, metric.WithAttributes(
		attribute.String("error.type", errType),
	))
}
