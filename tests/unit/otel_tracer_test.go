package unit_test

import (
	"context"
	"testing"

	mqotel "github.com/bancolombia/commons-mq-go/otel"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// newInMemoryTracer creates an SDK tracer backed by an in-memory exporter so that
// produced spans can be inspected in assertions.
func newInMemoryTracer(t *testing.T) (*tracetest.InMemoryExporter, *sdktrace.TracerProvider) {
	t.Helper()
	exp := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSyncer(exp),
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
	)
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	return exp, tp
}

// T054: StartProducerSpan populates all mandatory messaging semantic convention attributes.
func TestSpanAttributes_ProducerSpan(t *testing.T) {
	exp, tp := newInMemoryTracer(t)
	tracer := tp.Tracer("test")

	ctx, span := mqotel.StartProducerSpan(context.Background(), tracer, "MY.QUEUE", "aabbcc")
	assert.NotNil(t, ctx)
	span.End()

	spans := exp.GetSpans()
	require.Len(t, spans, 1)
	s := spans[0]

	assertAttr(t, s, "messaging.system", "ibmmq")
	assertAttr(t, s, "messaging.destination.name", "MY.QUEUE")
	assertAttr(t, s, "messaging.operation.type", "send")
	assertAttr(t, s, "messaging.message.id", "aabbcc")
}

// T054: StartConsumerSpan populates all mandatory messaging semantic convention attributes.
func TestSpanAttributes_ConsumerSpan(t *testing.T) {
	exp, tp := newInMemoryTracer(t)
	tracer := tp.Tracer("test")

	ctx, span := mqotel.StartConsumerSpan(context.Background(), tracer, "MY.QUEUE", "112233")
	assert.NotNil(t, ctx)
	span.End()

	spans := exp.GetSpans()
	require.Len(t, spans, 1)
	s := spans[0]

	assertAttr(t, s, "messaging.system", "ibmmq")
	assertAttr(t, s, "messaging.destination.name", "MY.QUEUE")
	assertAttr(t, s, "messaging.operation.type", "receive")
	assertAttr(t, s, "messaging.message.id", "112233")
}

// T054: StartClientSpan populates all mandatory messaging semantic convention attributes.
func TestSpanAttributes_ClientSpan(t *testing.T) {
	exp, tp := newInMemoryTracer(t)
	tracer := tp.Tracer("test")

	ctx, span := mqotel.StartClientSpan(context.Background(), tracer, "REQ.QUEUE")
	assert.NotNil(t, ctx)
	span.End()

	spans := exp.GetSpans()
	require.Len(t, spans, 1)
	s := spans[0]

	assertAttr(t, s, "messaging.system", "ibmmq")
	assertAttr(t, s, "messaging.destination.name", "REQ.QUEUE")
	assertAttr(t, s, "messaging.operation.type", "receive")
}

// T054: EndSpan records the error on the span and ends it.
func TestEndSpan_RecordsError(t *testing.T) {
	exp, tp := newInMemoryTracer(t)
	tracer := tp.Tracer("test")
	_, span := mqotel.StartProducerSpan(context.Background(), tracer, "Q", "")
	someErr := assert.AnError
	mqotel.EndSpan(span, &someErr)

	spans := exp.GetSpans()
	require.Len(t, spans, 1)
	s := spans[0]
	assert.Len(t, s.Events, 1, "expect one RecordError event")
	assert.Equal(t, "exception", string(s.Events[0].Name))
}

// T054: EndSpan with nil error must not set error status.
func TestEndSpan_NilErrorOk(t *testing.T) {
	exp, tp := newInMemoryTracer(t)
	tracer := tp.Tracer("test")
	_, span := mqotel.StartProducerSpan(context.Background(), tracer, "Q", "")
	mqotel.EndSpan(span, nil)

	spans := exp.GetSpans()
	require.Len(t, spans, 1)
	assert.Empty(t, spans[0].Events)
}

// assertAttr is a helper that finds an attribute by key and asserts its string value.
func assertAttr(t *testing.T, s tracetest.SpanStub, key, want string) {
	t.Helper()
	for _, a := range s.Attributes {
		if string(a.Key) == key {
			assert.Equal(t, want, a.Value.AsString(), "attribute %q", key)
			return
		}
	}
	t.Errorf("span missing attribute %q", key)
}
