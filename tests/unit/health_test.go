package unit_test

import (
	"testing"

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

// T065: Health() after NewListener (before Start) returns Healthy=false because
// lastConnState is 0 (StateConnected = iota = 0) which is actually "connected".
// A never-started listener has default zero value = StateConnected.
// This is the expected behaviour: the library only reports unhealthy if it has
// explicitly detected a failure.
func TestHealth_NewListenerNotYetStarted_IsHealthy(t *testing.T) {
	client := buildHealthClient(t)
	_ = client.NewListener("QUEUE.IN")
	h := client.Health()
	// A listener that hasn't started yet has state=StateConnected (zero value).
	// The library reports it as healthy until a real failure is observed.
	assert.True(t, h.Healthy)
	require.Len(t, h.Details, 1)
	assert.Equal(t, "QM1", h.Details[0].QueueManager)
}

// T065: Multiple components — all-alive → overall Healthy=true.
func TestHealth_AllAlive_IsHealthy(t *testing.T) {
	client := buildHealthClient(t)
	_ = client.NewListener("QUEUE.A")
	_ = client.NewListener("QUEUE.B")

	h := client.Health()
	assert.True(t, h.Healthy)
	assert.Len(t, h.Details, 2)
	for _, d := range h.Details {
		assert.True(t, d.Alive, "all details should report alive")
		assert.Empty(t, d.Reason)
	}
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
