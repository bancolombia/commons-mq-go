// Package otel provides OpenTelemetry instrumentation helpers for the mq package.
//
// It is a separate import path so applications that do not use OpenTelemetry
// do not compile any OTel SDK code. Only the lightweight API packages
// (go.opentelemetry.io/otel/trace, metric, propagation) are referenced here.
package otel

import (
	"context"

	omq "github.com/bancolombia/commons-mq-go/mq"
	"go.opentelemetry.io/otel/metric"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
	tracenoop "go.opentelemetry.io/otel/trace/noop"
)

// NewTelemetryConfig builds a [mq.TelemetryConfig] from the provided OTel providers.
// If propagator is nil, a no-op propagator is used (no trace context is propagated).
//
// Pass the noop helpers ([NoopTracerProvider], [NoopMeterProvider], [NoopPropagator])
// to explicitly disable instrumentation without relying on nil checks, which is
// useful for testing.
func NewTelemetryConfig(
	tp trace.TracerProvider,
	mp metric.MeterProvider,
	propagator propagation.TextMapPropagator,
) *omq.TelemetryConfig {
	if tp == nil {
		tp = NoopTracerProvider()
	}
	if mp == nil {
		mp = NoopMeterProvider()
	}
	if propagator == nil {
		propagator = NoopPropagator()
	}
	return &omq.TelemetryConfig{
		TracerProvider: tp,
		MeterProvider:  mp,
		Propagator:     propagator,
	}
}

// NoopTracerProvider returns an OTel no-op tracer provider backed by the
// go.opentelemetry.io/otel/trace/noop package. No spans are recorded.
func NoopTracerProvider() trace.TracerProvider {
	return tracenoop.NewTracerProvider()
}

// NoopMeterProvider returns an OTel no-op meter provider backed by the
// go.opentelemetry.io/otel/metric/noop package. No metrics are recorded.
func NoopMeterProvider() metric.MeterProvider {
	return metricnoop.NewMeterProvider()
}

// NoopPropagator returns a [propagation.TextMapPropagator] that performs no
// inject or extract operations. Use this when trace context propagation is
// explicitly disabled.
func NoopPropagator() propagation.TextMapPropagator {
	return noopPropagator{}
}

// noopPropagator implements propagation.TextMapPropagator with no-op operations.
type noopPropagator struct{}

func (noopPropagator) Inject(_ context.Context, _ propagation.TextMapCarrier) {}

func (noopPropagator) Extract(ctx context.Context, _ propagation.TextMapCarrier) context.Context {
	return ctx
}

func (noopPropagator) Fields() []string { return nil }
