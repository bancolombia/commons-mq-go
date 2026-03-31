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
| `InputConcurrency` | `int` | `1` | Parallel consumer connections |
| `OutputConcurrency` | `int` | `1` | Parallel producer connections |
| `ProducerTTL` | `time.Duration` | `0` (unlimited) | Message time-to-live |
| `MaxRetries` | `int` | `10` | Reconnect attempts before permanent failure |
| `InitialRetryInterval` | `time.Duration` | `2s` | Delay before first reconnect |
| `RetryMultiplier` | `float64` | `2.0` | Exponential backoff multiplier (≥ 1.0) |
| `SetQueueManager` | `bool` | `false` | Set resolved queue manager on reply-to queues |
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

The backoff formula is: `initial × multiplier^attempt`

With defaults (`initial=2s`, `multiplier=2.0`):

| Attempt | Delay |
|---------|-------|
| 0 | 2 s |
| 1 | 4 s |
| 2 | 8 s |
| 3 | 16 s |
| … | … |
| 9 | ~512 s |

After `MaxRetries` exhausted, `ErrConnectionFailed` is returned.

## Error types

| Sentinel | Returned when |
|----------|--------------|
| `mq.ErrInvalidConfig` | Required field missing or constraint violated |
| `mq.ErrTimeout` | `RequestReply` exceeded timeout |
| `mq.ErrConnectionFailed` | `MaxRetries` exhausted |
