---
sidebar_position: 4
title: Queue Listener
---

# Queue Listener

`Listener` consumes messages from an IBM MQ queue using N concurrent goroutine-per-connection consumers.

## Create a Listener

```go
listener := client.NewListener("DEV.QUEUE.1")
```

## Start consuming

`Start` blocks until the context is cancelled:

```go
ctx, cancel := context.WithCancel(context.Background())
defer cancel()

err := listener.Start(ctx, func(ctx context.Context, msg *mq.Message) error {
    log.Printf("id=%s body=%s", msg.ID, msg.Body)
    return nil
})
```

## Listener options

| Option | Description |
|--------|-------------|
| `WithListenerConcurrency(n int)` | Number of parallel consumer connections (overrides `Config.InputConcurrency`) |
| `WithListenerQueueOptions(opts QueueOptions)` | IBM MQ queue property customisations |
| `WithListenerMaxRetries(n int)` | Reconnect retries (overrides `Config.MaxRetries`) |

```go
listener := client.NewListener("DEV.QUEUE.1",
    mq.WithListenerConcurrency(5),
    mq.WithListenerMaxRetries(20),
)
```

## Queue options

`QueueOptions` maps to IBM MQ queue properties applied before opening the queue:

```go
opts := mq.QueueOptions{
    ReadAheadAllowed: true,
    MQMDReadEnabled:  true,
}
listener := client.NewListener("DEV.QUEUE.1",
    mq.WithListenerQueueOptions(opts),
)
```

## MessageHandler

```go
type MessageHandler func(ctx context.Context, msg *mq.Message) error
```

- The `ctx` carries the OTel span extracted from the message's propagation headers (when `Telemetry` is configured).
- A non-nil error is logged; the consumer continues to the next message.
- Panics inside the handler are recovered — the consumer goroutine will not crash.

## Message fields

| Field | Description |
|-------|-------------|
| `ID` | Hex-encoded `MQMD.MsgId` |
| `CorrelationID` | Hex-encoded `MQMD.CorrelId` |
| `ReplyToQueue` | `MQMD.ReplyToQ` |
| `ReplyToQueueManager` | `MQMD.ReplyToQMgr` |
| `Body` | Raw message payload |
| `UserProperties` | String key-value pairs (includes OTel headers when propagation is enabled) |
| `TTL` | Per-message time-to-live |

## Reconnect behaviour

If the connection drops, the listener automatically reconnects using the configured backoff policy. Once reconnected, it reopens the queue and resumes consuming without any caller intervention.

## Shutdown

Cancel the context to stop all consumer goroutines:

```go
cancel() // triggers graceful shutdown
```

`Start` returns `nil` on clean shutdown.
