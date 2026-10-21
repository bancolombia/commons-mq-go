---
sidebar_position: 3
title: Configuration
---

# Configuration Reference

The `mq.Config` struct configures all aspects of the IBM MQ client.

## Required fields

| Field | Type | Description |
|-------|------|-------------|
| `QueueManagerName` | `string` | IBM MQ queue manager name (e.g. `"QM1"`) |
| `ConnectionName` | `string` | Host and port in `"hostname(port)"` format |
| `ChannelName` | `string` | Server-connection channel name |

## Optional fields

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `Username` | `string` | `""` | Application user ID |
| `Password` | `string` | `""` | Application password. **Never logged.** |
| `InputQueue` | `string` | `""` | Default queue name for consumers |
| `OutputQueue` | `string` | `""` | Default queue name for producers |
| `ModelQueue` | `string` | `"SYSTEM.DEFAULT.MODEL.QUEUE"` | Model queue used to create temporary reply queues. The app user needs `+get` authority on it (the IBM MQ developer image uses `DEV.APP.MODEL.QUEUE`) |
| `InputConcurrency` | `int` | `1` | Parallel consumer connections |
| `OutputConcurrency` | `int` | `1` | Parallel producer connections |
| `ProducerTTL` | `time.Duration` | `0` (unlimited) | Message time-to-live |
| `MaxRetries` | `int` | `10` | Initial-connect and reconnect attempts before permanent failure. `WithListenerMaxRetries(0)` overrides this per listener to fail fast |
| `InitialRetryInterval` | `time.Duration` | `2s` | Delay before the first retry |
| `RetryMultiplier` | `float64` | `2.0` | Exponential backoff multiplier (≥ 1.0) |
| `MaxRetryInterval` | `time.Duration` | `30s` | Upper bound for each exponential retry delay (before jitter), preventing long recovery lags after an outage |
| `SetQueueManager` | `bool` | `false` | Populate `MQMD.ReplyToQMgr` with `QueueManagerName` when a reply-to queue is set |
| `TLS` | `*TLSConfig` | `nil` | TLS/SSL settings |
| `Telemetry` | `*TelemetryConfig` | `nil` | OpenTelemetry providers |

## TLS configuration

```go
cfg := mq.Config{
    // ... required fields ...
    TLS: &mq.TLSConfig{
        KeyRepository:    "/path/to/keystore",  // without extension
        CertificateLabel: "my-cert",
        CipherSpec:       "TLS_RSA_WITH_AES_256_CBC_SHA256",
        PeerName:         "CN=server,O=MyOrg",
        FIPSRequired:     false,
    },
}
```

> **Credential safety**: `Password`, `TLS.KeyRepository`, and `TLS.CertificateLabel` are never included in log output or error strings.

## Reconnect policy

The backoff formula is: `min(initial × multiplier^attempt, maxRetryInterval)`

Retries are context-aware: cancellation during a wait stops the operation promptly. With defaults (`initial=2s`, `multiplier=2.0`, `maxRetryInterval=30s`):

| Attempt | Delay |
|---------|-------|
| 0 | 2 s |
| 1 | 4 s |
| 2 | 8 s |
| 3 | 16 s |
| 4 and later | 30 s (capped by `MaxRetryInterval`) |

A small random jitter (up to 10%) is added to each delay. After `MaxRetries` is exhausted, `ErrConnectionFailed` is returned.

## Error types

| Sentinel | Returned when |
|----------|--------------|
| `mq.ErrInvalidConfig` | Required field missing or constraint violated |
| `mq.ErrTimeout` | `RequestReply` exceeded timeout |
| `mq.ErrConnectionFailed` | Initial connect or reconnect retries exhausted |
| `mq.ErrClientClosed` | Operation attempted on a closed Sender, RequestReplyClient, or Listener |
| `mq.ErrHandlerPanic` | Wrapped by a handler error caused by a panic (usable with `errors.Is`) |
