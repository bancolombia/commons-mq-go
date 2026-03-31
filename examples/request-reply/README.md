# request-reply example

Demonstrates the synchronous **request-reply** pattern with **commons-mq-go**.

The program runs both roles in a single process:

| Role | What it does |
|------|--------------|
| **Replier** (Listener) | Reads requests from `MQ_REQUEST_QUEUE`, upper-cases the payload, and sends the echo response back to the `ReplyToQueue` embedded in each request. |
| **Requester** (RequestReplyClient) | Sends N requests and blocks until each correlated response arrives (or a timeout fires). |

## Two modes

| Mode | Environment | How it works |
|------|-------------|--------------|
| `TemporaryQueue` *(default)* | `MQ_RR_MODE` unset or `temp` | Each call opens a unique dynamic reply queue; auto-deleted on close. |
| `FixedQueueSelector` | `MQ_RR_MODE=fixed` | All callers share one pre-existing reply queue; IBM MQ routes each reply using `MQMD.CorrelId`. |

## Prerequisites

A running IBM MQ queue manager. The [IBM MQ Developer image](https://hub.docker.com/r/ibmcom/mq) is the easiest option:

```sh
docker run --rm \
  -e LICENSE=accept \
  -e MQ_QMGR_NAME=QM1 \
  -p 1414:1414 \
  -p 9443:9443 \
  icr.io/ibm-messaging/mq:latest
```

For `FixedQueueSelector` mode you also need a second queue (e.g. `DEV.QUEUE.2`) configured on the queue manager.

## Run — TemporaryQueue mode

```sh
export MQ_QUEUE_MANAGER=QM1
export MQ_HOST=localhost
export MQ_PORT=1414
export MQ_CHANNEL=DEV.APP.SVRCONN
export MQ_REQUEST_QUEUE=DEV.QUEUE.1
export MQ_USERNAME=app        # optional
export MQ_PASSWORD=passw0rd   # optional
export MQ_MSG_COUNT=3         # optional, default 3

go run ./examples/request-reply/
```

## Run — FixedQueueSelector mode

```sh
export MQ_QUEUE_MANAGER=QM1
export MQ_HOST=localhost
export MQ_PORT=1414
export MQ_CHANNEL=DEV.APP.SVRCONN
export MQ_REQUEST_QUEUE=DEV.QUEUE.1
export MQ_REPLY_QUEUE=DEV.QUEUE.2
export MQ_RR_MODE=fixed
export MQ_USERNAME=app        # optional
export MQ_PASSWORD=passw0rd   # optional

go run ./examples/request-reply/
```

## Expected output

```
client created  queue-manager=QM1  host=localhost(1414)  request-queue=DEV.QUEUE.1  mode=TemporaryQueue
using TemporaryQueue mode
[requester] #1   sending  payload="request 1 of 3 — hello from commons-mq-go"
[replier]   #1   correlId=0000000000000000…  replied="ECHO: REQUEST 1 OF 3 — HELLO FROM COMMONS-MQ-GO"
[requester] #1   got reply  correlId=0000000000000000…  body="ECHO: REQUEST 1 OF 3 — HELLO FROM COMMONS-MQ-GO"
[requester] #2   sending  payload="request 2 of 3 — hello from commons-mq-go"
[replier]   #2   correlId=0000000000000000…  replied="ECHO: REQUEST 2 OF 3 — HELLO FROM COMMONS-MQ-GO"
[requester] #2   got reply  correlId=0000000000000000…  body="ECHO: REQUEST 2 OF 3 — HELLO FROM COMMONS-MQ-GO"
...
all 3 round-trips done — shutting down
final health  healthy=true
```
