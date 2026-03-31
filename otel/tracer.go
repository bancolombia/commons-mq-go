package otel

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// StartProducerSpan starts an OTel span with [trace.SpanKindProducer] for a
// message send operation, pre-populated with IBM MQ messaging semantic
// convention attributes.
//
// The caller must call span.End() when the operation completes (typically via defer).
// Use [EndSpan] to record errors and end in one call.
//
//	ctx, span := otel.StartProducerSpan(ctx, tracer, "MY.QUEUE", "")
//	defer otel.EndSpan(span, err)
func StartProducerSpan(
	ctx context.Context,
	tracer trace.Tracer,
	queueName, msgID string,
) (context.Context, trace.Span) {
	attrs := []attribute.KeyValue{
		attribute.String("messaging.system", "ibmmq"),
		attribute.String("messaging.destination.name", queueName),
		attribute.String("messaging.operation.type", "send"),
	}
	if msgID != "" {
		attrs = append(attrs, attribute.String("messaging.message.id", msgID))
	}
	return tracer.Start(ctx, "messaging send",
		trace.WithSpanKind(trace.SpanKindProducer),
		trace.WithAttributes(attrs...),
	)
}

// StartConsumerSpan starts an OTel span with [trace.SpanKindConsumer] for a
// message receive operation, pre-populated with IBM MQ messaging semantic
// convention attributes.
//
// The caller must call span.End() when the handler returns.
func StartConsumerSpan(
	ctx context.Context,
	tracer trace.Tracer,
	queueName, msgID string,
) (context.Context, trace.Span) {
	attrs := []attribute.KeyValue{
		attribute.String("messaging.system", "ibmmq"),
		attribute.String("messaging.destination.name", queueName),
		attribute.String("messaging.operation.type", "receive"),
	}
	if msgID != "" {
		attrs = append(attrs, attribute.String("messaging.message.id", msgID))
	}
	return tracer.Start(ctx, "messaging receive",
		trace.WithSpanKind(trace.SpanKindConsumer),
		trace.WithAttributes(attrs...),
	)
}

// StartClientSpan starts an OTel span with [trace.SpanKindClient] for a
// request-reply round-trip, pre-populated with IBM MQ messaging semantic
// convention attributes.
//
// The caller must call span.End() when the round-trip completes.
func StartClientSpan(
	ctx context.Context,
	tracer trace.Tracer,
	queueName string,
) (context.Context, trace.Span) {
	return tracer.Start(ctx, "messaging request-reply",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("messaging.system", "ibmmq"),
			attribute.String("messaging.destination.name", queueName),
			attribute.String("messaging.operation.type", "receive"),
		),
	)
}

// EndSpan ends span, recording err as a span error if non-nil.
// Intended for use with defer to cleanly close spans:
//
//	ctx, span := otel.StartProducerSpan(...)
//	defer func() { otel.EndSpan(span, &err) }()
func EndSpan(span trace.Span, errp *error) {
	if errp != nil && *errp != nil {
		span.RecordError(*errp)
		span.SetStatus(codes.Error, (*errp).Error())
	}
	span.End()
}
