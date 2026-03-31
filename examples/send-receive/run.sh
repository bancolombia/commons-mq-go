#
export MQ_QUEUE_MANAGER=QM1
export MQ_HOST=localhost
export MQ_PORT=1414
export MQ_CHANNEL=DEV.APP.SVRCONN
export MQ_QUEUE=DEV.QUEUE.1
export MQ_USERNAME=app          # optional
export MQ_PASSWORD=passw0rd     # optional
export MQ_MSG_COUNT=5           # optional, default 5

go run main.go