# send-receive example

Demonstrates the core send/receive workflow with **commons-mq-go**:

1. Creates an MQ client from environment variables.
2. Starts a `Listener` that prints every received message.
3. Uses a `Sender` to publish N messages to the same queue.
4. Waits for all messages to arrive, then shuts down cleanly.

## Prerequisites

A running IBM MQ queue manager with a server-connection channel and a local queue.
The [IBM MQ Developer image](https://hub.docker.com/r/ibmcom/mq) is the easiest way to get one:

```sh
docker run --rm \
  -e LICENSE=accept \
  -e MQ_QMGR_NAME=QM1 \
  -p 1414:1414 \
  -p 9443:9443 \
  icr.io/ibm-messaging/mq:latest
```

## Run

```sh
export MQ_QUEUE_MANAGER=QM1
export MQ_HOST=localhost
export MQ_PORT=1414
export MQ_CHANNEL=DEV.APP.SVRCONN
export MQ_QUEUE=DEV.QUEUE.1
export MQ_USERNAME=app          # optional
export MQ_PASSWORD=passw0rd     # optional
export MQ_MSG_COUNT=5           # optional, default 5

go run ./examples/send-receive/
```

## Expected output

```
client created  queue-manager=QM1  host=localhost(1414)  queue=DEV.QUEUE.1
[send] #1   id=414d5120514d312020202020…  body=hello from commons-mq-go, message 1 of 5
[recv] #1   id=414d5120514d312020202020…  body=hello from commons-mq-go, message 1 of 5
...
all 5 messages received — shutting down
final health  healthy=true
```
