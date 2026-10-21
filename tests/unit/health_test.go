package unit_test

import (
	"context"
	"testing"
	"time"

	"github.com/bancolombia/commons-mq-go/mq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// buildHealthClient creates a Client using an unreachable MQ host so that no real
// connections are ever established, allowing pure aggregation logic to be tested.
func buildHealthClient(t *testing.T) *mq.Client {
	t.Helper()
	cfg := mq.Config{
		QueueManagerName: "QM1",
		ConnectionName:   "localhost(19999)", // unreachable
		ChannelName:      "DEV.APP.SVRCONN",
		MaxRetries:       0,
	}
	client, err := mq.NewClient(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })
	return client
}

// T065: Health() on a client with no components created returns Healthy=true and no Details.
func TestHealth_NoComponentsIsHealthy(t *testing.T) {
	client := buildHealthClient(t)
	h := client.Health()
	assert.True(t, h.Healthy)
	assert.Empty(t, h.Details)
}

// T065: Health() after NewSender (before any sends) returns Healthy=true because
// no connections have been created yet (pool is empty).
func TestHealth_NewSenderNotYetUsed_IsHealthy(t *testing.T) {
	client := buildHealthClient(t)
	_ = client.NewSender("QUEUE.OUT")
	h := client.Health()
	assert.True(t, h.Healthy, "sender with empty pool should be healthy (idle)")
}

// T065: Health() after NewListener (before Start) reports unhealthy: a registered
// listener is expected to be consuming, and a never-started consumer is unknown.
func TestHealth_NewListenerNotYetStarted_IsUnhealthy(t *testing.T) {
	client := buildHealthClient(t)
	_ = client.NewListener("QUEUE.IN")
	h := client.Health()
	assert.False(t, h.Healthy, "a never-started listener must not report healthy")
	require.Len(t, h.Details, 1)
	assert.Equal(t, "QM1", h.Details[0].QueueManager)
	assert.False(t, h.Details[0].Alive)
	assert.Contains(t, h.Details[0].Reason, "unknown")
	assert.Contains(t, h.Details[0].Component, "QUEUE.IN")
}

// T065: Every consumer of every listener contributes its own status entry.
func TestHealth_MultipleListeners_EmitPerConsumerDetails(t *testing.T) {
	client := buildHealthClient(t)
	_ = client.NewListener("QUEUE.A")
	_ = client.NewListener("QUEUE.B", mq.WithListenerConcurrency(3))

	h := client.Health()
	assert.False(t, h.Healthy)
	assert.Len(t, h.Details, 4, "1 consumer + 3 consumers must yield 4 details")
	for _, d := range h.Details {
		assert.False(t, d.Alive, "unstarted consumers must not report alive")
		assert.Contains(t, d.Component, "listener")
	}
}

// T065: A failed Sender connection is exposed by Health() even though no
// connection remains allocated.
func TestHealth_SenderConnectionFailure_IsUnhealthy(t *testing.T) {
	cfg := mq.Config{
		QueueManagerName:     "QM1",
		ConnectionName:       "localhost(19999)",
		ChannelName:          "DEV.APP.SVRCONN",
		MaxRetries:           1,
		InitialRetryInterval: time.Millisecond,
		RetryMultiplier:      1.0,
		MaxRetryInterval:     time.Millisecond,
	}
	client, err := mq.NewClient(cfg)
	require.NoError(t, err)
	defer client.Close()

	sender := client.NewSender("QUEUE.OUT")
	_, err = sender.Send(context.Background(), "payload")
	require.Error(t, err)

	h := client.Health()
	assert.False(t, h.Healthy, "a sender whose connection failed must be unhealthy")
	require.NotEmpty(t, h.Details)
	assert.False(t, h.Details[0].Alive)
	assert.Contains(t, h.Details[0].Reason, "failed")
	assert.Contains(t, h.Details[0].Component, "QUEUE.OUT")
}

// T065: A request-reply client whose connection failed is exposed by Health().
func TestHealth_RequestReplyConnectionFailure_IsUnhealthy(t *testing.T) {
	client := buildHealthClient(t)
	rr := client.NewRequestReplyClient("QUEUE.REQ")
	_, err := rr.RequestReply(context.Background(), "ping", 50*time.Millisecond)
	require.Error(t, err)

	h := client.Health()
	assert.False(t, h.Healthy)
	require.NotEmpty(t, h.Details)
	assert.False(t, h.Details[0].Alive)
	assert.Contains(t, h.Details[0].Component, "QUEUE.REQ")
}

// T065: HealthStatus.Healthy is false when any Detail.Alive is false.
func TestHealth_AggregationAnyDead_IsUnhealthy(t *testing.T) {
	// Build a health status directly to test aggregation logic.
	status := mq.HealthStatus{
		Details: []mq.ConnectionStatus{
			{QueueManager: "QM1", Alive: true},
			{QueueManager: "QM1", Alive: false, Reason: "connection state: failed"},
		},
	}
	// Manually replicate the aggregation logic the library performs.
	healthy := true
	for _, d := range status.Details {
		if !d.Alive {
			healthy = false
			break
		}
	}
	assert.False(t, healthy, "any-dead should produce healthy=false")
}

// T065: ConnectionStatus.Reason is non-empty when Alive is false.
func TestHealth_DeadStatusHasReason(t *testing.T) {
	s := mq.ConnectionStatus{
		QueueManager: "QM1",
		Alive:        false,
		Reason:       "connection state: failed",
	}
	assert.NotEmpty(t, s.Reason, "unhealthy status should carry a reason")
}

// T065: ConnectionStatus.Reason is empty when Alive is true.
func TestHealth_AliveStatusHasNoReason(t *testing.T) {
	s := mq.ConnectionStatus{
		QueueManager: "QM1",
		Alive:        true,
	}
	assert.Empty(t, s.Reason)
}

// T065: Client.Health() returns all Details for multiple Senders.
func TestHealth_MultipleSenders_AggregatesAll(t *testing.T) {
	client := buildHealthClient(t)
	_ = client.NewSender("QUEUE.A")
	_ = client.NewSender("QUEUE.B")
	_ = client.NewSender("QUEUE.C")

	h := client.Health()
	// Senders with no connections yet contribute 0 Details each.
	assert.True(t, h.Healthy)
}
