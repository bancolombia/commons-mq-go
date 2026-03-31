---
sidebar_position: 6
title: Request-Reply
---

# Request-Reply

`RequestReplyClient` performs synchronous request-reply over IBM MQ. Two patterns are supported.

## Pattern 1: Temporary queue (default)

Each call opens a unique dynamic reply queue. The queue is auto-deleted by the queue manager when the connection closes.

```go
rr := client.NewRequestReplyClient("DEV.QUEUE.REQUEST")

reply, err := rr.RequestReply(ctx, "ping", 5*time.Second)
if err != nil {
    // mq.ErrTimeout if no reply arrived within 5s
}
log.Printf("reply: %s", reply.Body)
```

**Best for**: services where each caller has its own reply queue, or when simplicity is preferred over throughput.

## Pattern 2: Fixed queue + selector

A single shared reply queue. Responses are routed to the correct waiting goroutine using `MQMD.CorrelId` and `MQMO_MATCH_CORREL_ID`.

```go
rr := client.NewRequestReplyClientFixed("DEV.QUEUE.REQUEST", "DEV.QUEUE.REPLY")

reply, err := rr.RequestReply(ctx, "ping", 5*time.Second)
```

**Best for**: high-concurrency request-reply where creating a temp queue per call is expensive.

## Custom request via MessageCreator

```go
reply, err := rr.RequestReplyWithCreator(ctx, func(msg *mq.Message) error {
    msg.Body = []byte(`{"action":"query","id":42}`)
    msg.TTL = 10 * time.Second
    return nil
}, 10*time.Second)
```

## Options

| Option | Description |
|--------|-------------|
| `WithRRReplyQueue(queue string)` | Override the fixed reply queue name (FixedQueueSelector only) |

## Error handling

| Error | Cause |
|-------|-------|
| `mq.ErrTimeout` | No reply received within the timeout |
| `mq.ErrConnectionFailed` | IBM MQ connection lost and retries exhausted |
| `context.Canceled` | Context cancelled by caller |

## Concurrency

Both clients are safe for concurrent use from multiple goroutines. The `FixedQueueSelector` mode uses per-call correlation IDs to avoid interference between concurrent callers on the same reply queue.

## How the echo server pattern works

A typical setup has a listener that reads requests and sends replies:

```go
listener := client.NewListener("DEV.QUEUE.REQUEST")
_ = listener.Start(ctx, func(ctx context.Context, req *mq.Message) error {
    if req.ReplyToQueue == "" {
        return nil
    }
    replySender := client.NewSender(req.ReplyToQueue)
    _, _ = replySender.SendWithCreator(ctx, req.ReplyToQueue, func(reply *mq.Message) error {
        reply.Body = req.Body         // echo body
        reply.CorrelationID = req.ID  // correlate to request MsgId
        return nil
    })
    return nil
})
```
