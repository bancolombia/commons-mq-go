//go:build integration

package integration_test

// T066: Client.Health() returns Healthy=true while IBM MQ is reachable,
// and the Listener's connection status turns alive after Start() is called.

import (
	"context"
	"testing"
	"time"

	"github.com/bancolombia/commons-mq-go/mq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestHealth_Healthy_WhileConnected verifies that Health() returns Healthy=true
// once a Listener has established its IBM MQ connections.
func TestHealth_Healthy_WhileConnected(t *testing.T) {
	host, port := startMQContainer(t)
	cfg := mqConfig(host, port)

	client, err := mq.NewClient(cfg)
	require.NoError(t, err)
	defer client.Close()

	// Before starting any component the pool is empty — health should be fine.
	h := client.Health()
	assert.True(t, h.Healthy, "idle client should be healthy before any Start()")

	// Start a listener so a real connection is established.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	listener := client.NewListener("DEV.QUEUE.1")
	listenerDone := make(chan error, 1)
	go func() {
		listenerDone <- listener.Start(ctx, func(_ context.Context, _ *mq.Message) error {
			return nil
		})
	}()

	// Give the listener time to connect.
	time.Sleep(2 * time.Second)

	h = client.Health()
	assert.True(t, h.Healthy, "health should be true after listener connects")
	require.NotEmpty(t, h.Details)
	assert.True(t, h.Details[0].Alive)
	assert.Empty(t, h.Details[0].Reason)

	// Stop the listener.
	cancel()
	select {
	case <-listenerDone:
	case <-time.After(10 * time.Second):
		t.Fatal("listener did not stop in time")
	}
}

// TestHealth_Unhealthy_WhenConnectionNeverEstablished verifies that a Sender
// whose connection attempts fail keeps reporting unhealthy even though no
// connection remains allocated.
func TestHealth_Unhealthy_WhenConnectionNeverEstablished(t *testing.T) {
	cfg := mq.Config{
		QueueManagerName:     "QM1",
		ConnectionName:       "localhost(19999)", // unreachable
		ChannelName:          "DEV.APP.SVRCONN",
		Username:             "app",
		Password:             "passw0rd",
		MaxRetries:           1,
		InitialRetryInterval: 1 * time.Millisecond,
		RetryMultiplier:      1.0,
		MaxRetryInterval:     time.Millisecond,
	}
	client, err := mq.NewClient(cfg)
	require.NoError(t, err)
	defer client.Close()

	sender := client.NewSender("DEV.QUEUE.1")
	_, sendErr := sender.Send(context.Background(), "test")
	require.Error(t, sendErr, "expected send to fail on unreachable host")

	h := client.Health()
	assert.False(t, h.Healthy, "health must reflect the failed connection")
	require.NotEmpty(t, h.Details, "the failed sender must appear in the details")
	assert.False(t, h.Details[0].Alive)
	assert.Contains(t, h.Details[0].Reason, "failed")
}
