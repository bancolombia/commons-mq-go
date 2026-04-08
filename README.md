# commons-mq-go (alpha)

Go equivalent of the [`commons-jms`](https://github.com/bancolombia/commons-jms) Java library — a high-level IBM MQ client for Go applications.

**DISCLAIMER**: this is a non-production-quality alpha release

## Features

- Multi-connection queue listeners (`Listener`) with configurable concurrency
- Message sender (`Sender`) with producer pool and TTL support
- Request-reply via temporary queue (`RequestReplyClient`)
- Request-reply via fixed queue with correlation ID selector (`RequestReplyClientFixed`)
- Automatic reconnection with exponential backoff
- Health check (`Client.Health()`) for liveness/readiness probes
- Zero-overhead OpenTelemetry tracing and metrics (opt-in via `TelemetryConfig`)

## Requirements

- Go 1.22+
- CGO enabled (`CGO_ENABLED=1`, which is the default for native builds)
- IBM MQ C client libraries installed at `/opt/mqm` (see [CGO & IBM MQ C Client](#cgo--ibm-mq-c-client) below)

## CGO & IBM MQ C Client

This library wraps [`ibm-messaging/mq-golang`](https://github.com/ibm-messaging/mq-golang), which is a CGO binding to IBM MQ's C client. CGO is Go's foreign function interface to C and is enabled automatically for native builds — **no extra flag is needed unless you have explicitly disabled it**.

### Install the IBM MQ C client

The IBM MQ Redistributable Client is a lightweight, royalty-free package that provides only the C client libraries (no queue manager). Download the appropriate package from [IBM Fix Central](https://www.ibm.com/support/fixcentral) and extract it to `/opt/mqm`.

**Linux (x86-64)**

```bash
# Example using the MQ 9.4 redistributable client tarball
tar -zxf IBM_MQ_9.4.*_LINUX_X86-64_client_redist.tar.gz -C /opt/mqm
export LD_LIBRARY_PATH=/opt/mqm/lib64:$LD_LIBRARY_PATH
```

**macOS (ARM / Intel)**

```bash
# Example using the MQ 9.4 macOS package
tar -zxf IBM_MQ_9.4.*_MACOS_client_redist.tar.gz -C /opt/mqm
export DYLD_LIBRARY_PATH=/opt/mqm/lib:$DYLD_LIBRARY_PATH
```

The `ibm-messaging/mq-golang` package hard-codes its CGO directives to look at `/opt/mqm/inc` (headers) and `/opt/mqm/lib64` or `/opt/mqm/lib` (shared libraries), so **no custom `CGO_CFLAGS` or `CGO_LDFLAGS` are required** when the client is installed at the default path.

If you install the client to a non-standard location, override the CGO flags at build time:

```bash
export MQ_HOME=/usr/local/mqm
export CGO_CFLAGS="-I${MQ_HOME}/inc"
export CGO_LDFLAGS="-L${MQ_HOME}/lib64 -Wl,-rpath,${MQ_HOME}/lib64"  # Linux
# export CGO_LDFLAGS="-L${MQ_HOME}/lib -Wl,-rpath,${MQ_HOME}/lib"     # macOS
CGO_ENABLED=1 go build ./...
```

### Verify CGO is active

```bash
go env CGO_ENABLED   # should print 1
```

If this prints `0`, re-enable CGO explicitly:

```bash
export CGO_ENABLED=1
```

> **Note:** CGO is implicitly disabled for cross-compilation targets (e.g. `GOOS=linux GOARCH=arm64` on a macOS/amd64 host). In that case you must provide a cross-compiling C toolchain and set `CC` accordingly, or use a Docker-based build as described below.

### Docker multi-stage build

For containerised builds that ship the IBM MQ libraries alongside the binary:

```dockerfile
# ---- build stage ----
FROM golang:1.22 AS builder

# Copy IBM MQ redistributable client (pre-extracted)
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
COPY --from=builder /bin/app /bin/app

ENV LD_LIBRARY_PATH=/opt/mqm/lib64
ENTRYPOINT ["/bin/app"]
```

### Troubleshooting

| Symptom | Likely cause | Fix |
|---|---|---|
| `cgo: C compiler not found` | No C toolchain installed | Install `gcc` / `clang` (`apt install build-essential` on Debian/Ubuntu, `xcode-select --install` on macOS) |
| `fatal error: cmqc.h: No such file or directory` | MQ headers missing or wrong path | Check `/opt/mqm/inc/cmqc.h` exists; set `CGO_CFLAGS=-I<path>` |
| `error while loading shared libraries: libmqm_r.so` | MQ shared libs not on the runtime library path | Set `LD_LIBRARY_PATH=/opt/mqm/lib64` before running |
| `dyld: Library not loaded: libmqm_r.dylib` | macOS equivalent of the above | Set `DYLD_LIBRARY_PATH=/opt/mqm/lib` |
| `CGO_ENABLED=0` set in environment or Makefile | CGO explicitly disabled | Remove the override or export `CGO_ENABLED=1` |

## Getting Started

### 1. Start IBM MQ locally

```bash
docker run -e LICENSE=accept -e MQ_QMGR_NAME=QM1 \
  -p 1414:1414 -p 9443:9443 -d --name ibmmq \
  -e MQ_APP_PASSWORD=passw0rd \
  icr.io/ibm-messaging/mq:9.4.3.1-r1
```

### 2. Add the dependency

```bash
go get github.com/bancolombia/commons-mq-go@v0.1.0
```

### 3. Consume messages from a queue

```go
package main

import (
    "context"
    "log"

    "github.com/bancolombia/commons-mq-go/mq"
)

func main() {
    client, err := mq.NewClient(mq.Config{
        QueueManagerName: "QM1",
        ConnectionName:   "localhost(1414)",
        ChannelName:      "DEV.APP.SVRCONN",
        Username:         "app",
        Password:         "passw0rd",
        InputQueue:       "DEV.QUEUE.1",
        InputConcurrency: 3,
    })
    if err != nil {
        log.Fatal(err)
    }
    defer client.Close()

    listener := client.NewListener("DEV.QUEUE.1")
    if err := listener.Start(context.Background(), func(ctx context.Context, msg *mq.Message) error {
        log.Printf("received: %s", msg.Body)
        return nil
    }); err != nil {
        log.Fatal(err)
    }
}
```

### 4. Send a message

```go
sender := client.NewSender("DEV.QUEUE.1")
msgID, err := sender.Send(context.Background(), "hello world")
if err != nil {
    log.Fatal(err)
}
log.Printf("sent message ID: %s", msgID)
```

### 5. Request-Reply (temporary queue)

```go
rr := client.NewRequestReplyClient("DEV.QUEUE.REQUEST")
response, err := rr.RequestReply(context.Background(), "ping", 10*time.Second)
if err != nil {
    log.Fatal(err)
}
log.Printf("reply: %s", response.Body)
```

### 6. Add OpenTelemetry tracing (optional)

```go
import "github.com/bancolombia/commons-mq-go/otel"

cfg.Telemetry = otel.NewTelemetryConfig(
    myTracerProvider, // trace.TracerProvider from your OTel SDK setup
    myMeterProvider,  // metric.MeterProvider from your OTel SDK setup
    nil,              // nil = use global propagator
)
```

When `Telemetry` is nil (default), no OTel code runs — zero overhead.

### 7. Health check

```go
status := client.Health()
if !status.Healthy {
    for _, c := range status.Details {
        log.Printf("connection %s: %s", c.QueueManager, c.Reason)
    }
}
```

## Configuration

See [Configuration reference](https://github.com/bancolombia/commons-mq-go/blob/main/docs/go-03-configuration.md) for all `Config` fields and defaults.

## Documentation

Full documentation: <https://github.com/bancolombia/commons-mq-go/tree/main/docs>

## License

This project is licensed under the MIT License — see [LICENSE](./LICENSE) for details.
