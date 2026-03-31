#
export MQ_QUEUE_MANAGER=QM1
export MQ_HOST=localhost
export MQ_PORT=32783
export MQ_CHANNEL=DEV.APP.SVRCONN
export MQ_REQUEST_QUEUE=DEV.QUEUE.1
export MQ_USERNAME=app           # optional
export MQ_PASSWORD=passw0rd      # optional
export MQ_MSG_COUNT=3            # optional, default 3
export MQ_TIMEOUT_SECS=10        # optional, default 10
export MQ_RR_MODE=fixed          # comment for FixedQueueSelector mode
export MQ_REPLY_QUEUE=DEV.QUEUE.2  # required when MQ_RR_MODE=fixed

go run main.go
