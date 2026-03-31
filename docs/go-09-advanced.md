---
sidebar_position: 9
title: Advanced Settings
---

# Advanced Settings

## Multiple queue manager connections

Each `Client` manages one queue manager connection target. To connect to multiple queue managers, create multiple clients:

```go
clientA, _ := mq.NewClient(mq.Config{QueueManagerName: "QM1", /* ... */})
clientB, _ := mq.NewClient(mq.Config{QueueManagerName: "QM2", /* ... */})
defer clientA.Close()
defer clientB.Close()
```

## Per-component overrides

Options set on individual components override `Config` defaults:

```go
// Listener with 10 concurrent consumers and custom retry count.
listener := client.NewListener("HIGH.VOLUME.QUEUE",
    mq.WithListenerConcurrency(10),
    mq.WithListenerMaxRetries(30),
)

// Sender with its own pool size.
sender := client.NewSender("BATCH.OUT",
    mq.WithSenderConcurrency(8),
)
```

## Custom correlation IDs

```go
sender.SendWithCreator(ctx, "QUEUE", func(msg *mq.Message) error {
    msg.CorrelationID = hex.EncodeToString(myCorrelIDBytes[:])
    return nil
})
```

The correlation ID must be a hex-encoded value of ≤ 24 bytes.

## Message TTL

```go
sender.SendWithCreator(ctx, "QUEUE", func(msg *mq.Message) error {
    msg.Body = payload
    msg.TTL = 10 * time.Minute // overrides Config.ProducerTTL
    return nil
})
```

## TLS mutual authentication

```go
cfg := mq.Config{
    // ...
    TLS: &mq.TLSConfig{
        KeyRepository:    "/etc/ssl/mq/keystore",
        CertificateLabel: "client-cert",
        CipherSpec:       "TLS_RSA_WITH_AES_256_CBC_SHA256",
        PeerName:         "CN=QM1,O=Bancolombia",
        FIPSRequired:     true,
    },
}
```

## Graceful shutdown

```go
ctx, cancel := context.WithCancel(context.Background())

go func() {
    sigCh := make(chan os.Signal, 1)
    signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
    <-sigCh
    cancel()          // stop all listeners
    client.Close()    // drain sender pools
}()

listener.Start(ctx, handler) // blocks until cancel()
```

## Docker multi-stage build

When building in CI or shipping a container image, use a multi-stage Dockerfile so the IBM MQ C client libraries are available during the build stage and only the shared libraries are bundled into the final image:

```dockerfile
# ---- build stage ----
FROM golang:1.22 AS builder

# Provide the IBM MQ redistributable client (copy from a base image or ADD the tarball)
COPY --from=ibm-mq-client /opt/mqm /opt/mqm

ENV CGO_ENABLED=1 \
    CGO_CFLAGS="-I/opt/mqm/inc" \
    CGO_LDFLAGS="-L/opt/mqm/lib64 -Wl,-rpath,/opt/mqm/lib64"

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go build -o /bin/app ./cmd/app

# ---- runtime stage ----
FROM debian:bookworm-slim

# Only the shared libraries are needed at runtime
COPY --from=builder /opt/mqm/lib64 /opt/mqm/lib64
COPY --from=builder /bin/app       /bin/app

ENV LD_LIBRARY_PATH=/opt/mqm/lib64
ENTRYPOINT ["/bin/app"]
```

If you install the MQ client to a non-standard location (e.g. `/usr/local/mqm`), override the CGO flags accordingly:

```bash
export MQ_HOME=/usr/local/mqm
export CGO_CFLAGS="-I${MQ_HOME}/inc"
export CGO_LDFLAGS="-L${MQ_HOME}/lib64 -Wl,-rpath,${MQ_HOME}/lib64"  # Linux
# export CGO_LDFLAGS="-L${MQ_HOME}/lib -Wl,-rpath,${MQ_HOME}/lib"     # macOS
CGO_ENABLED=1 go build ./...
```

## CGO troubleshooting

| Symptom | Likely cause | Fix |
|---|---|---|
| `cgo: C compiler not found` | No C toolchain available | `apt install build-essential` (Linux) or `xcode-select --install` (macOS) |
| `fatal error: cmqc.h: No such file or directory` | MQ headers missing or path mismatch | Verify `/opt/mqm/inc/cmqc.h` exists; set `CGO_CFLAGS=-I<path>` |
| `error while loading shared libraries: libmqm_r.so` | MQ libs not on the runtime library path | `export LD_LIBRARY_PATH=/opt/mqm/lib64` |
| `dyld: Library not loaded: libmqm_r.dylib` | macOS runtime path missing | `export DYLD_LIBRARY_PATH=/opt/mqm/lib` |
| Build succeeds but binary panics at startup | Wrong library version linked | Ensure the `.so`/`.dylib` version matches the one used at build time |
| `CGO_ENABLED=0` | CGO explicitly disabled in env or Makefile | Unset the override or `export CGO_ENABLED=1` |

## Backoff formula

```
delay(attempt) = initialRetryInterval × retryMultiplier ^ attempt
```

To configure aggressive retry for low-latency recovery:

```go
cfg.InitialRetryInterval = 500 * time.Millisecond
cfg.RetryMultiplier      = 1.5
cfg.MaxRetries           = 20
```

## Error handling patterns

```go
_, err := sender.Send(ctx, payload)
switch {
case errors.Is(err, mq.ErrConnectionFailed):
    // permanent failure — circuit open, alert ops
case err != nil:
    // transient or queue-level error — retry at app level
}
```
