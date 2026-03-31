package mq

import (
	"context"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
	tracenoop "go.opentelemetry.io/otel/trace/noop"
)

const mqInstrumentationScope = "github.com/bancolombia/commons-mq-go"

// mqTelemetry holds resolved OTel instrumentation objects derived from [TelemetryConfig].
// When TelemetryConfig is nil all objects are no-ops with zero overhead.
type mqTelemetry struct {
	tracer     trace.Tracer
	propagator propagation.TextMapPropagator

	publishDuration metric.Float64Histogram
	receiveDuration metric.Float64Histogram
	connActive      metric.Int64ObservableGauge
	errorsTotal     metric.Int64Counter

	activeConns atomic.Int64 // underlying counter for the connActive observable
}

// newMQTelemetry builds an mqTelemetry from an optional TelemetryConfig.
// A nil config produces all-noop instruments with zero overhead.
func newMQTelemetry(tel *TelemetryConfig) *mqTelemetry {
	var tp trace.TracerProvider = tracenoop.NewTracerProvider()
	var mp metric.MeterProvider = metricnoop.NewMeterProvider()
	var prop propagation.TextMapPropagator = noopTextMapPropagator{}

	if tel != nil {
		if tel.TracerProvider != nil {
			tp = tel.TracerProvider
		}
		if tel.MeterProvider != nil {
			mp = tel.MeterProvider
		}
		if tel.Propagator != nil {
			prop = tel.Propagator
		}
	}

	t := &mqTelemetry{
		tracer:     tp.Tracer(mqInstrumentationScope),
		propagator: prop,
	}

	m := mp.Meter(mqInstrumentationScope)

	t.publishDuration, _ = m.Float64Histogram(
		"messaging.publish.duration",
		metric.WithDescription("Time from Put call start to Put return"),
		metric.WithUnit("ms"),
	)
	t.receiveDuration, _ = m.Float64Histogram(
		"messaging.receive.duration",
		metric.WithDescription("Time from message available to handler return"),
		metric.WithUnit("ms"),
	)
	t.connActive, _ = m.Int64ObservableGauge(
		"messaging.connection.active",
		metric.WithDescription("Count of live MQ connections"),
		metric.WithUnit("{connection}"),
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			o.Observe(t.activeConns.Load())
			return nil
		}),
	)
	t.errorsTotal, _ = m.Int64Counter(
		"messaging.errors.total",
		metric.WithDescription("Count of errors labeled by error.type"),
		metric.WithUnit("{error}"),
	)

	return t
}

// startProducerSpan creates a [trace.SpanKindProducer] span for a send operation.
// The caller must call span.End() when done.
func (t *mqTelemetry) startProducerSpan(ctx context.Context, queueName string) (context.Context, trace.Span) {
	return t.tracer.Start(ctx, "messaging send",
		trace.WithSpanKind(trace.SpanKindProducer),
		trace.WithAttributes(
			attribute.String("messaging.system", "ibmmq"),
			attribute.String("messaging.destination.name", queueName),
			attribute.String("messaging.operation.type", "send"),
		),
	)
}

// startConsumerSpan creates a [trace.SpanKindConsumer] span for a receive operation.
// The caller must call span.End() when done.
func (t *mqTelemetry) startConsumerSpan(ctx context.Context, queueName, msgID string) (context.Context, trace.Span) {
	return t.tracer.Start(ctx, "messaging receive",
		trace.WithSpanKind(trace.SpanKindConsumer),
		trace.WithAttributes(
			attribute.String("messaging.system", "ibmmq"),
			attribute.String("messaging.destination.name", queueName),
			attribute.String("messaging.operation.type", "receive"),
			attribute.String("messaging.message.id", msgID),
		),
	)
}

// startClientSpan creates a [trace.SpanKindClient] span wrapping a full request-reply
// round-trip. The caller must call span.End() when done.
func (t *mqTelemetry) startClientSpan(ctx context.Context, queueName string) (context.Context, trace.Span) {
	return t.tracer.Start(ctx, "messaging request-reply",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("messaging.system", "ibmmq"),
			attribute.String("messaging.destination.name", queueName),
			attribute.String("messaging.operation.type", "receive"),
		),
	)
}

// injectToMessage writes the current span context from ctx into msg.UserProperties
// as W3C TraceContext headers (traceparent / tracestate).
func (t *mqTelemetry) injectToMessage(ctx context.Context, msg *Message) {
	if msg.UserProperties == nil {
		msg.UserProperties = make(map[string]string)
	}
	t.propagator.Inject(ctx, propagation.MapCarrier(msg.UserProperties))
}

// extractFromMessage reads W3C TraceContext headers from msg.UserProperties and
// returns a context whose span is the extracted remote span context.
func (t *mqTelemetry) extractFromMessage(ctx context.Context, msg *Message) context.Context {
	return t.propagator.Extract(ctx, propagation.MapCarrier(msg.UserProperties))
}

// recordPublishDuration records how long a publish operation took (in ms).
func (t *mqTelemetry) recordPublishDuration(ctx context.Context, start time.Time) {
	t.publishDuration.Record(ctx, float64(time.Since(start).Microseconds())/1000.0)
}

// recordReceiveDuration records how long a receive/dispatch took (in ms).
func (t *mqTelemetry) recordReceiveDuration(ctx context.Context, start time.Time) {
	t.receiveDuration.Record(ctx, float64(time.Since(start).Microseconds())/1000.0)
}

// incConnActive increments the active connection gauge by 1.
func (t *mqTelemetry) incConnActive() {
	t.activeConns.Add(1)
}

// decConnActive decrements the active connection gauge by 1.
func (t *mqTelemetry) decConnActive() {
	t.activeConns.Add(-1)
}

// incError increments the error counter with the given error type label.
func (t *mqTelemetry) incError(ctx context.Context, errType string) {
	t.errorsTotal.Add(ctx, 1, metric.WithAttributes(
		attribute.String("error.type", errType),
	))
}

// endSpan ends the span, setting error status if err is non-nil.
func endSpan(span trace.Span, err error) {
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}
	span.End()
}

// noopTextMapPropagator is a zero-allocation propagator used when Telemetry is nil.
type noopTextMapPropagator struct{}

func (noopTextMapPropagator) Inject(_ context.Context, _ propagation.TextMapCarrier) {}

func (noopTextMapPropagator) Extract(ctx context.Context, _ propagation.TextMapCarrier) context.Context {
	return ctx
}

func (noopTextMapPropagator) Fields() []string { return nil }
