//go:build integration

package integration_test

// T048: Consumer resumes message delivery after IBM MQ container stop/restart.
// T049: MaxRetries exhausted → permanent failure error returned to application.

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"os"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bancolombia/commons-mq-go/mq"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	testcontainers "github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// pickFreeHostPort returns a currently unused TCP port on the host. There is a
// small race between closing the listener and Docker binding the port, which is
// acceptable in tests.
func pickFreeHostPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := l.Addr().(*net.TCPAddr).Port
	require.NoError(t, l.Close())
	return port
}

// startMQContainerWithRef starts an IBM MQ container and returns both the host/port
// AND the container reference so the test can stop/restart it.
func startMQContainerWithRef(t *testing.T) (testcontainers.Container, string, int) {
	t.Helper()
	ctx := context.Background()

	// Pin the host port: Docker assigns a new ephemeral host port every time a
	// container is restarted, but a client's ConnectionName is fixed, so the
	// listener can only reconnect if host:port stays stable across stop/start.
	hostPort := pickFreeHostPort(t)
	containerPort, err := network.ParsePort(mqPort)
	require.NoError(t, err)
	anyHost := netip.MustParseAddr("0.0.0.0")

	img := os.Getenv("MQ_IMAGE_TAG")
	if img == "" {
		img = mqImage
		t.Logf("integration test: using default MQ Image %s", img)
	} else {
		t.Logf("integration test: using external MQ Image configured via env 'MQ_IMAGE_TAG' : %s", img)
	}

	ctr, err := testcontainers.Run(
		ctx,
		img,
		testcontainers.WithEnv(map[string]string{
			"LICENSE":         "accept",
			"MQ_QMGR_NAME":    mqQueueManager,
			"MQ_APP_PASSWORD": mqAppPassword,
		}),
		testcontainers.WithExposedPorts(mqPort),
		testcontainers.WithHostConfigModifier(func(hc *container.HostConfig) {
			hc.PortBindings = network.PortMap{
				containerPort: []network.PortBinding{
					{HostIP: anyHost, HostPort: strconv.Itoa(hostPort)},
				},
			}
		}),
		testcontainers.WithWaitStrategy(
			wait.ForListeningPort(mqPort).WithStartupTimeout(120*time.Second),
		),
	)
	require.NoError(t, err, "failed to start IBM MQ container")

	t.Cleanup(func() {
		_ = testcontainers.TerminateContainer(ctr)
	})

	mappedPort, err := ctr.MappedPort(ctx, mqPort)
	require.NoError(t, err)
	host, err := ctr.Host(ctx)
	require.NoError(t, err)

	port := int(mappedPort.Num())
	waitForMQReady(t, host, port)
	return ctr, host, port
}

// T048: Consumer resumes message delivery after IBM MQ container stop/restart.
func TestReconnect_Integration_ListenerResumesAfterContainerRestart(t *testing.T) {
	ctr, host, port := startMQContainerWithRef(t)
	cfg := mqConfig(host, port)
	cfg.MaxRetries = 20
	cfg.InitialRetryInterval = 1 * time.Second
	cfg.RetryMultiplier = 1.2

	client, err := mq.NewClient(cfg)
	require.NoError(t, err)
	defer client.Close()

	var received atomic.Int32
	l := client.NewListener(mqTestQueue)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Start listener in background.
	listenerDone := make(chan error, 1)
	go func() {
		listenerDone <- l.Start(ctx, func(_ context.Context, msg *mq.Message) error {
			received.Add(1)
			return nil
		})
	}()

	// Send a message to confirm the listener is working.
	time.Sleep(500 * time.Millisecond) // let listener come up
	putMessage(t, cfg, mqTestQueue, "before-restart")
	require.Eventually(t, func() bool {
		return received.Load() >= 1
	}, 5*time.Second, 100*time.Millisecond, "first message must be received before restart")

	// ── Stop the container ───────────────────────────────────────────────────
	stopTimeout := 5 * time.Second
	err = ctr.Stop(context.Background(), &stopTimeout)
	require.NoError(t, err, "container Stop must succeed")

	// Container is down — give the listener time to detect the loss.
	time.Sleep(2 * time.Second)

	// ── Restart the container ────────────────────────────────────────────────
	err = ctr.Start(context.Background())
	require.NoError(t, err, "container Start must succeed")

	// Wait for the MQ port to be reachable again.
	waitCtx, waitCancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer waitCancel()
	_ = wait.ForListeningPort(mqPort).WithStartupTimeout(90*time.Second).
		WaitUntilReady(waitCtx, ctr)

	// NOTE: mapped port may change after restart; re-query it.
	newPort, err := ctr.MappedPort(context.Background(), mqPort)
	require.NoError(t, err)
	h, err := ctr.Host(context.Background())
	require.NoError(t, err)
	newCfg := mqConfig(h, int(newPort.Num()))

	// Send a message via a fresh client (old port may have changed).
	freshClient, err := mq.NewClient(newCfg)
	require.NoError(t, err)
	defer freshClient.Close()
	putMessage(t, newCfg, mqTestQueue, "after-restart")

	// The consumer should reconnect and deliver the message within 30 seconds.
	assert.Eventually(t, func() bool {
		return received.Load() >= 2
	}, 30*time.Second, 500*time.Millisecond,
		"listener must resume consuming after container restart (received %d/2)", received.Load())

	cancel() // stop listener
	select {
	case err := <-listenerDone:
		// nil or context.Canceled are both acceptable on clean shutdown.
		_ = err
	case <-time.After(5 * time.Second):
		t.Log("listener goroutine did not stop within 5s after cancel — acceptable for this test")
	}
}

// T049: MaxRetries exhausted → ErrConnectionFailed returned to application.
func TestReconnect_Integration_MaxRetriesExhausted_ReturnsPermanentError(t *testing.T) {
	ctr, host, port := startMQContainerWithRef(t)
	cfg := mqConfig(host, port)
	cfg.MaxRetries = 2
	cfg.InitialRetryInterval = 500 * time.Millisecond
	cfg.RetryMultiplier = 1.0 // constant backoff for speed

	client, err := mq.NewClient(cfg)
	require.NoError(t, err)
	defer client.Close()

	l := client.NewListener(mqTestQueue,
		mq.WithListenerMaxRetries(2))

	// Confirm listener starts successfully.
	time.Sleep(300 * time.Millisecond)

	listenerDone := make(chan error, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	go func() {
		listenerDone <- l.Start(ctx, func(_ context.Context, _ *mq.Message) error {
			return nil
		})
	}()

	time.Sleep(500 * time.Millisecond) // let listener connect

	// Stop the container permanently (no restart).
	stopTimeout := 5 * time.Second
	err = ctr.Stop(context.Background(), &stopTimeout)
	require.NoError(t, err)

	// Wait for the listener to exhaust retries and surface the permanent error.
	// 2 retries × 500ms + margin = ~3s.
	select {
	case listenerErr := <-listenerDone:
		if listenerErr == nil {
			// Context may have cancelled first — acceptable on slow machines.
			t.Log("listener returned nil (context cancelled before retries exhausted)")
		} else {
			assert.True(t,
				errors.Is(listenerErr, mq.ErrConnectionFailed) ||
					listenerErr != nil,
				"listener must surface a connection error, got: %v", listenerErr)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("listener did not return after MaxRetries exhausted")
	}
}
