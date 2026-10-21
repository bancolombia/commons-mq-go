---
sidebar_position: 6
title: Request-Reply
---

# Request-Reply

`RequestReplyClient` performs synchronous request-reply over IBM MQ. Two patterns are supported.

## Pattern 1: Temporary queue (default)

Each call opens a unique dynamic reply queue. The queue is auto-deleted by the queue manager when the connection closes.

Dynamic queues are created from the model queue named by `Config.ModelQueue` (default `SYSTEM.DEFAULT.MODEL.QUEUE`). The application user must hold `+get` authority on that model queue. The IBM MQ developer image grants the `app` user authority on `DEV.APP.MODEL.QUEUE` instead, so set `ModelQueue: "DEV.APP.MODEL.QUEUE"` when using that image.

```go
rr := client.NewRequestReplyClient("DEV.QUEUE.1")

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
// DEV.QUEUE.1 and DEV.QUEUE.2 are pre-created in the IBM MQ developer image.
rr := client.NewRequestReplyClientFixed("DEV.QUEUE.1", "DEV.QUEUE.2")

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
| `mq.ErrClientClosed` | `RequestReplyClient.Close()` or `Client.Close()` was called |
| `context.Canceled` | Context cancelled by caller |

## Concurrency

Both clients are safe for concurrent use from multiple goroutines. The `FixedQueueSelector` mode uses per-call correlation IDs to avoid interference between concurrent callers on the same reply queue.

## Health and lifecycle

Each request-reply client contributes to `Client.Health()` after its first call: a connection-level failure sets `Alive=false` until a later call succeeds, while `ErrTimeout` counts as a working connection. Idle clients contribute no entry. `Close` is idempotent; subsequent calls return `mq.ErrClientClosed`. `Client.Close` closes every request-reply client created from it.

Set `Config.SetQueueManager = true` to populate `MQMD.ReplyToQMgr` with the configured queue manager name when a reply-to queue is set.

## How the echo server pattern works

A typical setup has a listener that reads requests and sends replies:

```go
listener := client.NewListener("DEV.QUEUE.1")
_ = listener.Start(ctx, func(ctx context.Context, req *mq.Message) error {
    if req.ReplyToQueue == "" {
        return nil
    }
    replySender := client.NewSender(req.ReplyToQueue)
    _, _ = replySender.SendWithCreator(ctx, req.ReplyToQueue, func(reply *mq.Message) error {
        reply.Body = req.Body         // echo body
        reply.CorrelationID = req.ID  // TemporaryQueue mode: correlate to request MsgId
        // For FixedQueueSelector mode, use `reply.CorrelationID = req.CorrelationID`
        // instead: callers filter the shared reply queue with MQMO_MATCH_CORREL_ID.
        return nil
    })
    return nil
})
```
