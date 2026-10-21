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

`Start` blocks until the context is cancelled or until every consumer has exited (for example after exhausting its reconnect retries):

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
| `WithListenerMaxRetries(n int)` | Initial-connect and reconnect attempts (overrides `Config.MaxRetries`) |
| `WithListenerErrorHandler(h ListenerErrorHandler)` | Callback for handler and consumer lifecycle errors |

```go
listener := client.NewListener("DEV.QUEUE.1",
    mq.WithListenerConcurrency(5),
    mq.WithListenerMaxRetries(20),
    mq.WithListenerErrorHandler(func(ctx context.Context, msg *mq.Message, err error) {
        if msg == nil {
            log.Printf("consumer stopped: %v", err)
            return
        }
        log.Printf("handler failed for message %s: %v", msg.ID, err)
    }),
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
- A non-nil error or panic is reported to `WithListenerErrorHandler`, recorded on the consumer span, and counted in `messaging.errors.total` (`error.type=handler_failed`). A panic is converted to an error wrapping `mq.ErrHandlerPanic`. The consumer continues to the next message.
- Delivery is **at-most-once**: the message is removed from the queue before the handler runs and is not requeued on failure. If message loss is unacceptable, persist or forward failed messages from the error callback.
- `msg` is `nil` in the callback when a consumer stops on an error, which is how partial failures are surfaced when `InputConcurrency > 1`.

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

If the connection drops, the listener automatically reconnects using the configured backoff policy. Reconnection attempts are bounded by the context, and each delay is capped by `Config.MaxRetryInterval` (default 30s). The initial connection is retried with the same policy. Once reconnected, it reopens the queue and resumes consuming without any caller intervention.

Each consumer reports its own state through `Client.Health()`: one consumer dying no longer masks the health of the others (`StateUnknown` until started, `Connected`, `Reconnecting`, `Failed`, `Stopped`).

## Shutdown

Cancel the context to stop all consumer goroutines:

```go
cancel() // triggers graceful shutdown
```

`Client.Close()` also cancels every listener created from that client and waits for `Start` to return. `Start` returns `nil` on clean shutdown, and `ErrClientClosed` if `Client.Close` was already called. `Start` may be called only once per listener.
