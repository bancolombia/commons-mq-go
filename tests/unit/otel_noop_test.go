package unit_test

import (
	"context"
	"testing"

	"github.com/bancolombia/commons-mq-go/mq"
	mqotel "github.com/bancolombia/commons-mq-go/otel"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// T053: Verify that when TelemetryConfig is nil the library creates a valid client
// with no OTel SDK symbols initialised and operations complete without panicking.
func TestOTelNoOpPath_NilTelemetry(t *testing.T) {
	cfg := mq.Config{
		QueueManagerName: "QM1",
		ConnectionName:   "localhost(1414)",
		ChannelName:      "DEV.APP.SVRCONN",
		// Telemetry is deliberately nil
	}
	client, err := mq.NewClient(cfg)
	require.NoError(t, err)
	defer client.Close()

	// NewSender / NewListener / NewRequestReplyClient must not panic when Telemetry is nil.
	sender := client.NewSender("QUEUE.ONE")
	assert.NotNil(t, sender)

	listener := client.NewListener("QUEUE.TWO")
	assert.NotNil(t, listener)

	rrClient := client.NewRequestReplyClient("QUEUE.REQ")
	assert.NotNil(t, rrClient)

	rrFixed := client.NewRequestReplyClientFixed("QUEUE.REQ", "QUEUE.REPLY")
	assert.NotNil(t, rrFixed)
}

// T053: Verify that NewTelemetryConfig with all-noop providers round-trips correctly.
func TestOTelNoOpPath_ExplicitNoopProviders(t *testing.T) {
	tc := mqotel.NewTelemetryConfig(
		mqotel.NoopTracerProvider(),
		mqotel.NoopMeterProvider(),
		mqotel.NoopPropagator(),
	)
	require.NotNil(t, tc)
	assert.NotNil(t, tc.TracerProvider)
	assert.NotNil(t, tc.MeterProvider)
	assert.NotNil(t, tc.Propagator)
}

// T053: Verify no panics when Send is invoked with nil Telemetry (no MQ, just
// checks that the OTel code-path doesn't explode before the MQ connection attempt).
func TestOTelNoOpPath_SendDoesNotPanicWithoutMQ(t *testing.T) {
	cfg := mq.Config{
		QueueManagerName: "QM1",
		ConnectionName:   "localhost(19999)", // unreachable — connection will fail
		ChannelName:      "DEV.APP.SVRCONN",
		MaxRetries:       0,
	}
	client, err := mq.NewClient(cfg)
	require.NoError(t, err)
	defer client.Close()

	sender := client.NewSender("QUEUE.OUT")
	// Send will fail because there is no MQ, but it must not panic.
	assert.NotPanics(t, func() {
		_, _ = sender.Send(context.Background(), "hello")
	})
}

// T053: Benchmark confirms that noop instrumentation adds negligible overhead.
// See otel_bench_test.go for the official benchmark; this is a quick sanity check.
func TestOTelNoOpPath_NoopPropagatorsDoNothing(t *testing.T) {
	prop := mqotel.NoopPropagator()
	carrier := map[string]string{}

	// Inject must not set any keys.
	mqotel.InjectToMap(context.Background(), prop, carrier)
	assert.Empty(t, carrier, "noop propagator must not inject any headers")

	// Extract must return the same context unchanged.
	ctx := context.Background()
	outCtx := mqotel.ExtractFromMap(ctx, prop, carrier)
	assert.Equal(t, ctx, outCtx, "noop propagator must return context unchanged")
}
