---
sidebar_position: 5
title: Message Sender
---

# Message Sender

`Sender` publishes messages to IBM MQ queues using a managed connection pool.

## Create a Sender

```go
sender := client.NewSender("DEV.QUEUE.1")
```

If the destination queue is empty, `Config.OutputQueue` is used.

## Send methods

### Send

```go
msgID, err := sender.Send(ctx, "hello world")
```

### SendTo

```go
msgID, err := sender.SendTo(ctx, "OTHER.QUEUE", "hello")
```

### SendWithCreator

Full control over the outgoing message:

```go
msgID, err := sender.SendWithCreator(ctx, "DEV.QUEUE.1", func(msg *mq.Message) error {
    msg.Body = []byte(`{"event":"order_created"}`)
    msg.CorrelationID = "0102030405060708090a0b0c0d0e0f101112131415161718" // hex-encoded, up to 24 bytes
    msg.TTL = 30 * time.Second
    msg.UserProperties["x-source"] = "payment-service"
    return nil
})
```

## Sender options

| Option | Description |
|--------|-------------|
| `WithSenderConcurrency(n int)` | Size of the connection pool (overrides `Config.OutputConcurrency`) |

```go
sender := client.NewSender("DEV.QUEUE.1",
    mq.WithSenderConcurrency(4),
)
```

## Connection pool

The pool is lazy: connections are established on the first `Send` call. If all pool slots are in use, `Send` blocks until one becomes available or the context is cancelled.

The initial connection is retried according to `MaxRetries` and capped backoff (`MaxRetryInterval`), bounded by the caller's context. On a connection error, the library automatically attempts to reconnect and retries the send once. If reconnection fails, `ErrConnectionFailed` is returned. Reconnection retries can produce **duplicates** if a PUT was accepted by the queue manager but its acknowledgement was lost; this is an at-least-once retry path.

`Send` failures are returned to the caller and are also reflected by `Client.Health()`: a sender whose last connection attempt failed reports `Alive=false` even when no connection remains in the pool.

## Return value

All send methods return the hex-encoded `MQMD.MsgId` assigned by the queue manager. This ID can be used for message tracking or correlation.

## Lifecycle

The Sender is automatically closed when `Client.Close()` is called. `Close` rejects new sends, waits for in-flight sends to finish, then drains and closes the pool. Calling `Send` after `Client.Close` returns `ErrClientClosed` instead of blocking or opening a new connection.
