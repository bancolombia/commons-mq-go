---
sidebar_position: 1
title: Overview
---

# Commons MQ Go

`commons-mq-go` is the Go equivalent of the Java `commons-jms` library. It provides a high-level IBM MQ client for Go applications, covering:

- **Multi-connection queue listeners** — concurrent consumers using goroutines
- **Message senders** — pooled connections with lazy initialisation
- **Request-reply** — temporary queue and fixed-queue-selector patterns
- **Automatic reconnection** — exponential backoff on connection failures
- **Health checks** — liveness probes for operations/readiness endpoints
- **OpenTelemetry** — opt-in traces, metrics, and W3C TraceContext propagation

The library requires **no JVM**, no Spring, and no Commons MQ runtime. It is distributed as a standard Go module and can be used in any Go 1.22+ application.

## Module

```
github.com/bancolombia/commons-mq-go
```

## Package structure

| Package | Description |
|---------|-------------|
| `mq` | Core IBM MQ operations — `Client`, `Listener`, `Sender`, `RequestReplyClient`, `Health` |
| `otel` | Optional OpenTelemetry helpers — `NewTelemetryConfig`, span/metrics utilities |

## Prerequisites

- Go 1.22+
- IBM MQ C client libraries installed on the host:
  - Linux: `/opt/mqm/lib64`
  - macOS: `/opt/mqm/lib`
- CGO enabled (`CGO_ENABLED=1`)

## Quick install

```bash
go get github.com/bancolombia/commons-mq-go
```

## Feature parity with the Java library

| Feature | Java `commons-jms` | Go `commons-mq-go` |
|---------|---------------------|----------------------|
| Queue listener | ✅ | ✅ |
| Message sender | ✅ | ✅ |
| Request-reply (temp queue) | ✅ | ✅ |
| Request-reply (fixed queue + selector) | ✅ | ✅ |
| Automatic reconnect | ✅ | ✅ |
| Health check | ✅ | ✅ |
| OpenTelemetry | ✅ | ✅ |
| TLS/SSL | ✅ | ✅ |
