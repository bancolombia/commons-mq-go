---
sidebar_position: 8
title: OpenTelemetry
---

# OpenTelemetry

`commons-mq-go` ships optional OpenTelemetry instrumentation for traces, metrics, and W3C TraceContext propagation.

Import the `otel` sub-package to avoid pulling OTel SDK symbols into applications that do not use it:

```go
import mqotel "github.com/bancolombia/commons-mq-go/otel"
```

## Wire up providers

```go
import (
    mqotel "github.com/bancolombia/commons-mq-go/otel"
    "go.opentelemetry.io/otel/propagation"
    sdktrace "go.opentelemetry.io/otel/sdk/trace"
    sdkmetric "go.opentelemetry.io/otel/sdk/metric"
)

tp := sdktrace.NewTracerProvider(/* your exporter */)
mp := sdkmetric.NewMeterProvider(/* your reader */)

cfg := mq.Config{
    // ...
    Telemetry: mqotel.NewTelemetryConfig(
        tp,
        mp,
        propagation.TraceContext{}, // W3C TraceContext propagation
    ),
}
```

When `Telemetry` is `nil` (the default), all instrumentation is no-op — zero overhead, zero SDK symbols compiled.

## Traces

| Operation | SpanKind | `messaging.operation.type` |
|-----------|----------|---------------------------|
| `Sender.Send` | `Producer` | `send` |
| `Listener` handler dispatch | `Consumer` | `receive` |
| `RequestReplyClient.RequestReply` | `Client` | `receive` |

### Mandatory span attributes

| Attribute | Value |
|-----------|-------|
| `messaging.system` | `ibmmq` |
| `messaging.destination.name` | Queue name |
| `messaging.operation.type` | Per table above |
| `messaging.message.id` | Hex-encoded `MQMD.MsgId` |

## Metrics

| Metric | Instrument | Unit | Description |
|--------|-----------|------|-------------|
| `messaging.publish.duration` | Histogram | ms | Put call latency |
| `messaging.receive.duration` | Histogram | ms | Handler dispatch latency |
| `messaging.connection.active` | Gauge | connections | Live MQ connections |
| `messaging.errors.total` | Counter | errors | Errors labeled by `error.type` |

## Context propagation

When a sender has `Telemetry` configured, it injects W3C `traceparent`/`tracestate` headers into the outgoing message's `UserProperties` map. The listener extracts them and attaches the remote span context as the parent of the consumer span — enabling end-to-end distributed traces across services that communicate via IBM MQ.

The headers are invisible to consumers that do not check `UserProperties`, so backward compatibility is preserved.

## Noop helpers

```go
// Useful for testing or explicitly disabling OTel without nil checks.
mqotel.NoopTracerProvider()
mqotel.NoopMeterProvider()
mqotel.NoopPropagator()
```

## Standalone span helpers

```go
ctx, span := mqotel.StartProducerSpan(ctx, tracer, "MY.QUEUE", msgID)
defer mqotel.EndSpan(span, &err)
```
