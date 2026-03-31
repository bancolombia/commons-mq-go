---
sidebar_position: 7
title: Health Check
---

# Health Check

`Client.Health()` reports the liveness of all IBM MQ connections managed by the library. Use it to integrate with HTTP liveness/readiness probes.

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
| `Details` | `[]ConnectionStatus` | Per-connection breakdown |

### ConnectionStatus

| Field | Type | Description |
|-------|------|-------------|
| `QueueManager` | `string` | Queue manager name |
| `Alive` | `bool` | `true` if connection is established and operational |
| `Reason` | `string` | Human-readable failure reason (empty when `Alive=true`) |

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

- **Idle components** (pool empty, never started): reported as healthy — the library is not unhealthy just because it hasn't been used yet.
- **Active connections**: state is read from the connection's last known `ConnectionState` flag (`Connected`, `Reconnecting`, `Failed`).
- **All connections in use**: senders with all connections checked out are treated as alive (they are actively working).
- **Closed senders**: reported as `Alive=false` with reason `"sender is closed"`.

## ConnectionState values

| State | Healthy | Description |
|-------|---------|-------------|
| `StateConnected` | ✅ | Connection is live |
| `StateReconnecting` | ❌ | Reconnect in progress after failure |
| `StateFailed` | ❌ | All reconnect retries exhausted |
