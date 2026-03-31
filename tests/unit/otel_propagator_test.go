package unit_test

import (
	"context"
	"testing"

	mqotel "github.com/bancolombia/commons-mq-go/otel"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

// T055: InjectToMap writes traceparent into the carrier map.
func TestPropagator_InjectToMap_WritesTraceparent(t *testing.T) {
	_, tp := newInMemoryTracer(t)
	tracer := tp.Tracer("test")
	ctx, span := tracer.Start(context.Background(), "root")
	defer span.End()

	prop := propagation.TraceContext{}
	carrier := make(map[string]string)
	mqotel.InjectToMap(ctx, prop, carrier)

	assert.Contains(t, carrier, "traceparent", "traceparent header must be injected")
}

// T055: ExtractFromMap recovers the trace context from a carrier map.
func TestPropagator_ExtractFromMap_RecoverTraceContext(t *testing.T) {
	exp := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSyncer(exp),
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
	)
	defer tp.Shutdown(context.Background())
	tracer := tp.Tracer("test")

	// Create a root span and inject its context into a carrier.
	rootCtx, rootSpan := tracer.Start(context.Background(), "root")
	rootSpanCtx := trace.SpanFromContext(rootCtx).SpanContext()
	prop := propagation.TraceContext{}
	carrier := make(map[string]string)
	mqotel.InjectToMap(rootCtx, prop, carrier)
	rootSpan.End()

	// Extract into a fresh context and start a child span.
	extractedCtx := mqotel.ExtractFromMap(context.Background(), prop, carrier)
	childCtx, childSpan := tracer.Start(extractedCtx, "child")
	assert.NotNil(t, childCtx)
	childSpan.End()

	// The child must have the root's TraceID.
	spans := exp.GetSpans()
	require.Len(t, spans, 2)
	var childStub tracetest.SpanStub
	for _, s := range spans {
		if s.Name == "child" {
			childStub = s
		}
	}
	assert.Equal(t, rootSpanCtx.TraceID(), childStub.SpanContext.TraceID(),
		"child span must carry the root trace ID")
}

// T055: InjectToMap with nil propagator must not panic.
func TestPropagator_InjectToMap_NilPropagatorNoPanic(t *testing.T) {
	assert.NotPanics(t, func() {
		mqotel.InjectToMap(context.Background(), nil, map[string]string{})
	})
}

// T055: ExtractFromMap with nil propagator must return ctx unchanged.
func TestPropagator_ExtractFromMap_NilPropagatorReturnsCtx(t *testing.T) {
	ctx := context.Background()
	got := mqotel.ExtractFromMap(ctx, nil, map[string]string{})
	assert.Equal(t, ctx, got)
}

// T055: InjectToMap with nil carrier must not panic.
func TestPropagator_InjectToMap_NilCarrierNoPanic(t *testing.T) {
	prop := propagation.TraceContext{}
	assert.NotPanics(t, func() {
		mqotel.InjectToMap(context.Background(), prop, nil)
	})
}

// T055: Round-trip inject → extract preserves all W3C TraceContext fields.
func TestPropagator_RoundTrip(t *testing.T) {
	exp := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSyncer(exp),
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
	)
	defer tp.Shutdown(context.Background())

	prop := propagation.TraceContext{}
	tracer := tp.Tracer("test")

	// Producer side: start span, inject into map.
	prodCtx, prodSpan := tracer.Start(context.Background(), "producer")
	carrier := make(map[string]string)
	mqotel.InjectToMap(prodCtx, prop, carrier)
	prodSpan.End()

	// Consumer side: extract, then start child span.
	consumerCtx := mqotel.ExtractFromMap(context.Background(), prop, carrier)
	_, consSpan := tracer.Start(consumerCtx, "consumer")
	consSpan.End()

	spans := exp.GetSpans()
	require.Len(t, spans, 2)

	traceIDs := make(map[trace.TraceID]struct{})
	for _, s := range spans {
		traceIDs[s.SpanContext.TraceID()] = struct{}{}
	}
	assert.Len(t, traceIDs, 1, "producer and consumer spans must share the same TraceID")
}
