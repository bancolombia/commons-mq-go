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
    msg.CorrelationID = "aabbccddeeff" // hex-encoded 24 bytes
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

On a connection error, the library automatically attempts to reconnect and retries the send once. If reconnection fails, `ErrConnectionFailed` is returned.

## Return value

All send methods return the hex-encoded `MQMD.MsgId` assigned by the queue manager. This ID can be used for message tracking or correlation.

## Lifecycle

The Sender is automatically closed when `Client.Close()` is called.
