---
sidebar_position: 7
title: Health Check
---

# Health Check

`Client.Health()` reports the last known state of every IBM MQ connection managed by the library. Use it to integrate with HTTP liveness/readiness probes.

Health is state-based: it does not perform an active network probe. A connection that broke while idle is reported as connected until the next operation discovers the failure.

Start listeners before exposing a readiness probe: a listener that has been created but not started reports `StateUnknown` and therefore `Healthy=false`.

## API

```go
status := client.Health()

if !status.Healthy {
    log.Printf("MQ unhealthy: %+v", status.Details)
}
```

### HealthStatus

| Field | Type | Description |
|-------|------|-------------|
| `Healthy` | `bool` | `true` when all active connections are alive |
| `Details` | `[]ConnectionStatus` | Per-component (per-consumer for listeners) breakdown |

### ConnectionStatus

| Field | Type | Description |
|-------|------|-------------|
| `QueueManager` | `string` | Queue manager name |
| `Alive` | `bool` | `true` if the connection is established and operational |
| `Reason` | `string` | Human-readable failure reason (empty when `Alive=true`) |
| `Component` | `string` | Source of the entry, e.g. `listener "DEV.QUEUE.1" consumer 0`, `sender "DEV.QUEUE.1"`, `request-reply "DEV.QUEUE.1"` |

## HTTP liveness probe example

```go
http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
    h := client.Health()
    if !h.Healthy {
        w.WriteHeader(http.StatusServiceUnavailable)
    }
    json.NewEncoder(w).Encode(h)
})
```

## Behaviour details

- **Listeners**: every consumer emits its own entry. A listener that was created but never started reports `StateUnknown` (not healthy), because a registered consumer is expected to be consuming. A consumer that stopped reports `StateStopped`; a consumer that failed reports `StateFailed`. Partial failures are therefore visible even when `InputConcurrency > 1`.
- **Senders**: idle senders (never used) contribute no entries. After a failed connection attempt or exhausted reconnect retries, the sender remains in `Details` with `Alive=false` and the connection error in `Reason`, even when no connection is allocated.
- **Request-reply clients**: contribute no entry until used; afterwards they report the state of the last call. A timeout counts as a working connection.
- **All connections in use**: a sender with all connections checked out reports alive only when its last observed state is not failed or reconnecting.
- **Closed components**: senders, listeners, and request-reply clients report `Alive=false` with a `closed` reason. After `Client.Close`, an additional `client` entry reports the closed state.

## ConnectionState values

| State | Healthy | Description |
|-------|---------|-------------|
| `StateConnected` | ✅ | Connection is live |
| `StateReconnecting` | ❌ | Connect/reconnect in progress after failure |
| `StateFailed` | ❌ | Connect/reconnect retries exhausted, or a consumer stopped on an error |
| `StateUnknown` | ❌ | Component registered but not started / not used yet |
| `StateStopped` | ❌ | Component deliberately stopped (context cancelled or `Client.Close`) |
