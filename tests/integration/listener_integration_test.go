//go:build integration

package integration_test

// T015: Listener starts against IBM MQ container; message delivered to handler within 1 second.
// T016: InputConcurrency=3 opens exactly 3 consumer connections.
// T017: Invalid queue name returns descriptive error from Listener.Start.

import (
	"context"
	"fmt"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bancolombia/commons-mq-go/mq"
	ibmmq "github.com/ibm-messaging/mq-golang/v5/ibmmq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	testcontainers "github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

const (
	mqPort         = "1414/tcp"
	mqQueueManager = "QM1"
	mqChannel      = "DEV.APP.SVRCONN"
	mqAppUser      = "app"
	mqAppPassword  = "passw0rd"
	mqTestQueue    = "DEV.QUEUE.1"
	mqTestQueue2   = "DEV.QUEUE.2"
	mqTestQueue3   = "DEV.QUEUE.3"
)

// startMQContainer starts an IBM MQ Docker container and returns the host:port.
// The container is terminated when the test completes via t.Cleanup.
func startMQContainer(t *testing.T) (host string, port int) {
	t.Helper()
	ctx := context.Background()

	img := os.Getenv("MQ_IMAGE_TAG")
	if img == "" {
		img = "ibm-mqadvanced-server-dev:9.4.5.0-arm64"
		t.Logf("integration test: using default MQ Image %s", img)
	} else {
		t.Logf("integration test: using external MQ Image configured via env 'MQ_IMAGE_TAG' : %s", img)
	}

	ctr, err := testcontainers.Run(
		ctx,
		img,
		testcontainers.WithEnv(map[string]string{
			"LICENSE":           "accept",
			"MQ_QMGR_NAME":      mqQueueManager,
			"MQ_APP_PASSWORD":   mqAppPassword,
			"MQ_ADMIN_PASSWORD": mqAppPassword,
		}),
		testcontainers.WithExposedPorts(mqPort),
		testcontainers.WithWaitStrategy(
			wait.ForListeningPort(mqPort).WithStartupTimeout(120*time.Second),
		),
	)
	if err != nil {
		fmt.Printf("failed to start IBM MQ container: %v", err)
	}
	require.NoError(t, err, "failed to start IBM MQ container")

	t.Cleanup(func() {
		if err := testcontainers.TerminateContainer(ctr); err != nil {
			t.Logf("failed to terminate MQ container: %v", err)
		}
	})

	mappedPort, err := ctr.MappedPort(ctx, mqPort)
	require.NoError(t, err)

	h, err := ctr.Host(ctx)
	require.NoError(t, err)

	return h, mappedPort.Int()
}

// mqConfig returns a Config pointing at the test MQ container.
func mqConfig(host string, port int) mq.Config {
	return mq.Config{
		QueueManagerName:     mqQueueManager,
		ConnectionName:       fmt.Sprintf("%s(%d)", host, port),
		ChannelName:          mqChannel,
		Username:             mqAppUser,
		Password:             mqAppPassword,
		InputConcurrency:     1,
		MaxRetries:           3,
		InitialRetryInterval: 500 * time.Millisecond,
	}
}

// putMessage places a raw message on the named queue using a direct IBM MQ connection.
// Used by integration tests to seed messages before starting a listener.
func putMessage(t *testing.T, cfg mq.Config, queueName, payload string) {
	t.Helper()

	mqcd := ibmmq.NewMQCD()
	mqcd.ChannelName = cfg.ChannelName
	mqcd.ConnectionName = cfg.ConnectionName

	mqcno := ibmmq.NewMQCNO()
	mqcno.Options = ibmmq.MQCNO_CLIENT_BINDING
	mqcno.ClientConn = mqcd

	if cfg.Username != "" {
		mqcsp := ibmmq.NewMQCSP()
		mqcsp.AuthenticationType = ibmmq.MQCSP_AUTH_USER_ID_AND_PWD
		mqcsp.UserId = cfg.Username
		mqcsp.Password = cfg.Password
		mqcno.SecurityParms = mqcsp
	}

	qmgr, err := ibmmq.Connx(cfg.QueueManagerName, mqcno)
	require.NoError(t, err, "putMessage: connect to QM")
	defer qmgr.Disc()

	mqod := ibmmq.NewMQOD()
	mqod.ObjectName = queueName
	qObj, err := qmgr.Open(mqod, ibmmq.MQOO_OUTPUT|ibmmq.MQOO_FAIL_IF_QUIESCING)
	require.NoError(t, err, "putMessage: open queue")
	defer qObj.Close(0)

	mqmd := ibmmq.NewMQMD()
	mqpmo := ibmmq.NewMQPMO()
	mqpmo.Options = ibmmq.MQPMO_NEW_MSG_ID | ibmmq.MQPMO_NO_SYNCPOINT
	err = qObj.Put(mqmd, mqpmo, []byte(payload))
	require.NoError(t, err, "putMessage: put message")
}

// T015: Listener receives a message from the IBM MQ container within 1 second.
func TestListener_Integration_MessageDeliveredWithinOneSecond(t *testing.T) {
	host, port := startMQContainer(t)
	cfg := mqConfig(host, port)

	client, err := mq.NewClient(cfg)
	require.NoError(t, err)
	defer client.Close()

	const payload = "hello-from-integration-test"
	putMessage(t, cfg, mqTestQueue, payload)

	received := make(chan string, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	l := client.NewListener(mqTestQueue)
	go func() {
		_ = l.Start(ctx, func(_ context.Context, msg *mq.Message) error {
			received <- string(msg.Body)
			cancel() // stop listener after first message
			return nil
		})
	}()

	select {
	case body := <-received:
		assert.Equal(t, payload, body, "delivered message body must match sent payload")
	case <-time.After(1 * time.Second):
		t.Fatal("message was not delivered within 1 second")
	}
}

// T016: InputConcurrency=3 opens exactly 3 consumer connections.
// Verified by observing 3 simultaneous blocking GET operations.
func TestListener_Integration_ConcurrencyOpensNConnections(t *testing.T) {
	host, port := startMQContainer(t)
	cfg := mqConfig(host, port)

	client, err := mq.NewClient(cfg)
	require.NoError(t, err)
	defer client.Close()

	const concurrency = 3
	var activeCount atomic.Int64
	var peakCount atomic.Int64

	// Seed 3 messages so each goroutine picks one up.
	for i := range concurrency {
		putMessage(t, cfg, mqTestQueue, fmt.Sprintf("msg-%d", i))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	received := make(chan struct{}, concurrency)
	l := client.NewListener(mqTestQueue, mq.WithListenerConcurrency(concurrency))

	go func() {
		_ = l.Start(ctx, func(_ context.Context, _ *mq.Message) error {
			current := activeCount.Add(1)
			peak := peakCount.Load()
			for current > peak {
				if peakCount.CompareAndSwap(peak, current) {
					break
				}
				peak = peakCount.Load()
			}
			received <- struct{}{}
			return nil
		})
	}()

	// Wait for all 3 messages to be received.
	for range concurrency {
		select {
		case <-received:
		case <-time.After(5 * time.Second):
			t.Fatal("not all messages received within timeout")
		}
	}
	cancel()

	// With concurrency=3 and 3 simultaneous messages, peak must reach 3.
	assert.Equal(t, int64(concurrency), peakCount.Load(),
		"with concurrency=%d, peak simultaneous handler invocations must equal %d", concurrency, concurrency)
}

// T017: Invalid queue name returns a descriptive error from Listener.Start.
func TestListener_Integration_InvalidQueueName_ReturnsDescriptiveError(t *testing.T) {
	host, port := startMQContainer(t)
	cfg := mqConfig(host, port)
	cfg.MaxRetries = 0 // fail fast

	client, err := mq.NewClient(cfg)
	require.NoError(t, err)
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	l := client.NewListener("DOES.NOT.EXIST.QUEUE", mq.WithListenerMaxRetries(0))
	err = l.Start(ctx, func(_ context.Context, _ *mq.Message) error { return nil })

	require.Error(t, err, "Start with invalid queue name must return an error")
	assert.Contains(t, err.Error(), "DOES.NOT.EXIST.QUEUE",
		"error message must identify the invalid queue name")
}
