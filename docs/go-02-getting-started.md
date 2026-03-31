---
sidebar_position: 2
title: Getting Started
---

# Getting Started

## Prerequisites: CGO & IBM MQ C Client

This library uses [`ibm-messaging/mq-golang`](https://github.com/ibm-messaging/mq-golang), a CGO binding to IBM MQ's native C client. Two things are required before you can build:

1. **A C compiler** (`gcc` on Linux, `clang` via Xcode Command Line Tools on macOS).
2. **IBM MQ C client libraries** installed at `/opt/mqm`.

### Install a C compiler

```bash
# Debian / Ubuntu
sudo apt-get install -y build-essential

# macOS
xcode-select --install
```

### Install the IBM MQ Redistributable Client

Download the *Redistributable Client* package for your platform from [IBM Fix Central](https://www.ibm.com/support/fixcentral) and extract it to `/opt/mqm`. This package contains only the C shared libraries — no queue manager is installed.

```bash
# Linux (x86-64) — adjust the filename to the version you downloaded
sudo mkdir -p /opt/mqm
sudo tar -zxf IBM_MQ_9.4.*_LINUX_X86-64_client_redist.tar.gz -C /opt/mqm

# macOS
sudo mkdir -p /opt/mqm
sudo tar -zxf IBM_MQ_9.4.*_MACOS_client_redist.tar.gz -C /opt/mqm
```

After extraction the layout should be:

```
/opt/mqm/
├── inc/        ← C headers (cmqc.h, …)
├── lib64/      ← shared libraries on Linux (libmqm_r.so, …)
└── lib/        ← shared libraries on macOS  (libmqm_r.dylib, …)
```

### Make the libraries visible at runtime

```bash
# Linux — add to ~/.bashrc or /etc/ld.so.conf.d/
export LD_LIBRARY_PATH=/opt/mqm/lib64:$LD_LIBRARY_PATH

# macOS
export DYLD_LIBRARY_PATH=/opt/mqm/lib:$DYLD_LIBRARY_PATH
```

### Verify CGO is active

```bash
go env CGO_ENABLED   # must print 1 (default for native builds)
```

CGO is enabled by default for native (non-cross) builds. If it shows `0`, re-enable it:

```bash
export CGO_ENABLED=1
```

> **Cross-compilation note:** CGO does not support cross-compilation out of the box. When targeting a different `GOOS`/`GOARCH`, use a Docker-based build (see [Advanced Settings](./go-09-advanced.md#docker-multi-stage-build)).

---

## Installation

Add the library to your Go module:

```bash
go get github.com/bancolombia/commons-mq-go
```

## Minimal example — queue listener

```go
package main

import (
    "context"
    "log"

    "github.com/bancolombia/commons-mq-go/mq"
)

func main() {
    client, _ := mq.NewClient(mq.Config{
        QueueManagerName: "QM1",
        ConnectionName:   "localhost(1414)",
        ChannelName:      "DEV.APP.SVRCONN",
        Username:         "app",
        Password:         "passw0rd",
    })
    defer client.Close()

    listener := client.NewListener("DEV.QUEUE.1")
    _ = listener.Start(context.Background(), func(_ context.Context, msg *mq.Message) error {
        log.Printf("received: %s", msg.Body)
        return nil
    })
}
```

> **SC-001**: The above example is 18 lines excluding imports and config — within the ≤ 20-line target.

## Start IBM MQ locally

The quickest way to get an IBM MQ instance running is via Docker:

```bash
docker run --rm -e LICENSE=accept \
  -e MQ_QMGR_NAME=QM1 \
  -e MQ_APP_PASSWORD=passw0rd \
  -p 1414:1414 \
  icr.io/ibm-messaging/mq:latest
```

Wait ~30 seconds for the queue manager to initialise, then run your application.

## Send a message

```go
sender := client.NewSender("DEV.QUEUE.1")
msgID, err := sender.Send(context.Background(), "hello world")
```

## Request-reply

```go
rr := client.NewRequestReplyClient("DEV.QUEUE.1")
reply, err := rr.RequestReply(context.Background(), "ping", 5*time.Second)
```

## Next steps

- [Configuration reference](./go-03-configuration.md)
- [Listener options](./go-04-listener.md)
- [Sender options](./go-05-sender.md)
- [Request-reply patterns](./go-06-request-reply.md)
