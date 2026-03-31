package otel

import (
	"context"

	"go.opentelemetry.io/otel/propagation"
)

// InjectToMap writes the W3C TraceContext headers from ctx into carrier using p.
// The resulting map entries ("traceparent", "tracestate") can be stored in
// [mq.Message].UserProperties so downstream consumers can extract the span context.
//
// A nil propagator is a no-op.
func InjectToMap(
	ctx context.Context,
	p propagation.TextMapPropagator,
	carrier map[string]string,
) {
	if p == nil || carrier == nil {
		return
	}
	p.Inject(ctx, propagation.MapCarrier(carrier))
}

// ExtractFromMap reads W3C TraceContext headers ("traceparent", "tracestate") from
// carrier and returns a context whose remote span context is set to the extracted
// trace context. The returned context should be used as the parent for any new
// consumer spans.
//
// A nil propagator returns ctx unchanged.
func ExtractFromMap(
	ctx context.Context,
	p propagation.TextMapPropagator,
	carrier map[string]string,
) context.Context {
	if p == nil {
		return ctx
	}
	return p.Extract(ctx, propagation.MapCarrier(carrier))
}
