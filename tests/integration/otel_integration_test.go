//go:build integration

package integration_test

// T056: Spans are emitted with correct attributes via in-memory span exporter for
//       send, receive, and request-reply operations.
// T057: messaging.publish.duration, messaging.receive.duration, messaging.errors.total
//       are emitted and recorded via in-memory metrics reader.

import (
	"context"
	"testing"
	"time"

	"github.com/bancolombia/commons-mq-go/mq"
	mqotel "github.com/bancolombia/commons-mq-go/otel"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// buildOTelConfig builds a TelemetryConfig backed by in-memory exporters.
// Returns the config plus the span exporter and metrics reader for assertions.
func buildOTelConfig(t *testing.T) (
	*mq.TelemetryConfig,
	*tracetest.InMemoryExporter,
	*sdkmetric.ManualReader,
) {
	t.Helper()

	spanExp := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSyncer(spanExp),
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
	)
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { _ = mp.Shutdown(context.Background()) })

	tc := mqotel.NewTelemetryConfig(tp, mp, propagation.TraceContext{})
	return tc, spanExp, reader
}

// collectMetrics gathers the current metric snapshot via the manual reader.
func collectMetrics(t *testing.T, reader *sdkmetric.ManualReader) metricdata.ResourceMetrics {
	t.Helper()
	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &rm))
	return rm
}

// findHistogram finds a histogram metric by name in the resource metrics.
func findHistogram(rm metricdata.ResourceMetrics, name string) *metricdata.Histogram[float64] {
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name == name {
				if h, ok := m.Data.(metricdata.Histogram[float64]); ok {
					return &h
				}
			}
		}
	}
	return nil
}

// findCounter finds a sum metric by name in the resource metrics.
func findCounter(rm metricdata.ResourceMetrics, name string) *metricdata.Sum[int64] {
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name == name {
				if s, ok := m.Data.(metricdata.Sum[int64]); ok {
					return &s
				}
			}
		}
	}
	return nil
}

// hasSpanWithName checks whether any span has the given name.
func hasSpanWithName(spans tracetest.SpanStubs, name string) bool {
	for _, s := range spans {
		if s.Name == name {
			return true
		}
	}
	return false
}

// hasSpanAttr checks whether the named span has a specific attribute value.
func hasSpanAttr(spans tracetest.SpanStubs, spanName, attrKey, attrVal string) bool {
	for _, s := range spans {
		if s.Name != spanName {
			continue
		}
		for _, a := range s.Attributes {
			if string(a.Key) == attrKey && a.Value.AsString() == attrVal {
				return true
			}
		}
	}
	return false
}

// ─── T056: Send span ─────────────────────────────────────────────────────────

// TestOTelIntegration_SendSpan verifies that Sender.Send produces a producer span
// with the correct messaging semantic convention attributes.
func TestOTelIntegration_SendSpan(t *testing.T) {
	host, port := startMQContainer(t)
	tc, spanExp, _ := buildOTelConfig(t)

	cfg := mqConfig(host, port)
	cfg.Telemetry = tc

	client, err := mq.NewClient(cfg)
	require.NoError(t, err)
	defer client.Close()

	sender := client.NewSender("DEV.QUEUE.1")
	msgID, err := sender.Send(context.Background(), "hello otel")
	require.NoError(t, err)
	assert.NotEmpty(t, msgID)

	spans := spanExp.GetSpans()
	assert.True(t, hasSpanWithName(spans, "messaging send"),
		"expected a 'messaging send' span, got: %v", spanNames(spans))
	assert.True(t, hasSpanAttr(spans, "messaging send", "messaging.system", "ibmmq"))
	assert.True(t, hasSpanAttr(spans, "messaging send", "messaging.destination.name", "DEV.QUEUE.1"))
	assert.True(t, hasSpanAttr(spans, "messaging send", "messaging.operation.type", "send"))
}

// ─── T056: Listener (receive) span ───────────────────────────────────────────

// TestOTelIntegration_ReceiveSpan verifies that the Listener creates a consumer span
// with correct attributes when processing a message.
func TestOTelIntegration_ReceiveSpan(t *testing.T) {
	host, port := startMQContainer(t)
	tc, spanExp, _ := buildOTelConfig(t)

	cfg := mqConfig(host, port)
	cfg.Telemetry = tc

	client, err := mq.NewClient(cfg)
	require.NoError(t, err)
	defer client.Close()

	// Seed the queue with a message.
	sender := client.NewSender("DEV.QUEUE.1")
	_, err = sender.Send(context.Background(), "for listener")
	require.NoError(t, err)

	// Start a listener that processes the message and then cancels.
	received := make(chan struct{}, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	listener := client.NewListener("DEV.QUEUE.1")
	go func() {
		_ = listener.Start(ctx, func(_ context.Context, _ *mq.Message) error {
			received <- struct{}{}
			cancel() // stop after first message
			return nil
		})
	}()

	select {
	case <-received:
	case <-ctx.Done():
		t.Fatal("listener did not receive the message within timeout")
	}

	// Allow the span to be flushed.
	time.Sleep(100 * time.Millisecond)

	spans := spanExp.GetSpans()
	assert.True(t, hasSpanWithName(spans, "messaging receive"),
		"expected a 'messaging receive' span, got: %v", spanNames(spans))
	assert.True(t, hasSpanAttr(spans, "messaging receive", "messaging.system", "ibmmq"))
	assert.True(t, hasSpanAttr(spans, "messaging receive", "messaging.operation.type", "receive"))
}

// ─── T056: Request-reply span ────────────────────────────────────────────────

// TestOTelIntegration_RequestReplySpan verifies that RequestReplyClient creates a
// SpanKindClient span wrapping the full round-trip.
func TestOTelIntegration_RequestReplySpan(t *testing.T) {
	host, port := startMQContainer(t)
	tc, spanExp, _ := buildOTelConfig(t)

	cfg := mqConfig(host, port)
	cfg.Telemetry = tc

	client, err := mq.NewClient(cfg)
	require.NoError(t, err)
	defer client.Close()

	// Start an echo listener on the request queue.
	echoCtx, echoCancel := context.WithCancel(context.Background())
	defer echoCancel()

	echoListener := client.NewListener("DEV.QUEUE.1")
	go func() {
		_ = echoListener.Start(echoCtx, func(ctx context.Context, msg *mq.Message) error {
			if msg.ReplyToQueue == "" {
				return nil
			}
			replySender := client.NewSender(msg.ReplyToQueue)
			_, _ = replySender.SendWithCreator(ctx, msg.ReplyToQueue, func(reply *mq.Message) error {
				reply.Body = msg.Body
				reply.CorrelationID = msg.ID
				return nil
			})
			return nil
		})
	}()

	// Give echo listener time to start.
	time.Sleep(500 * time.Millisecond)

	rrClient := client.NewRequestReplyClient("DEV.QUEUE.1")
	reply, err := rrClient.RequestReply(context.Background(), "ping", 10*time.Second)
	require.NoError(t, err)
	assert.NotNil(t, reply)

	spans := spanExp.GetSpans()
	assert.True(t, hasSpanWithName(spans, "messaging request-reply"),
		"expected a 'messaging request-reply' span, got: %v", spanNames(spans))
	assert.True(t, hasSpanAttr(spans, "messaging request-reply", "messaging.system", "ibmmq"))
}

// ─── T057: Metrics ───────────────────────────────────────────────────────────

// TestOTelIntegration_PublishDurationMetric verifies that messaging.publish.duration
// is recorded after a successful send.
func TestOTelIntegration_PublishDurationMetric(t *testing.T) {
	host, port := startMQContainer(t)
	tc, _, reader := buildOTelConfig(t)

	cfg := mqConfig(host, port)
	cfg.Telemetry = tc

	client, err := mq.NewClient(cfg)
	require.NoError(t, err)
	defer client.Close()

	sender := client.NewSender("DEV.QUEUE.1")
	_, err = sender.Send(context.Background(), "metrics test")
	require.NoError(t, err)

	rm := collectMetrics(t, reader)
	h := findHistogram(rm, "messaging.publish.duration")
	require.NotNil(t, h, "messaging.publish.duration histogram must exist")
	require.NotEmpty(t, h.DataPoints, "must have at least one data point")
	assert.GreaterOrEqual(t, h.DataPoints[0].Count, uint64(1),
		"at least one publish duration must be recorded")
}

// TestOTelIntegration_ReceiveDurationMetric verifies that messaging.receive.duration
// is recorded after a message is dispatched to the handler.
func TestOTelIntegration_ReceiveDurationMetric(t *testing.T) {
	host, port := startMQContainer(t)
	tc, _, reader := buildOTelConfig(t)

	cfg := mqConfig(host, port)
	cfg.Telemetry = tc

	client, err := mq.NewClient(cfg)
	require.NoError(t, err)
	defer client.Close()

	// Seed a message.
	seedSender := client.NewSender("DEV.QUEUE.1")
	_, err = seedSender.Send(context.Background(), "receive metric test")
	require.NoError(t, err)

	received := make(chan struct{}, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	listener := client.NewListener("DEV.QUEUE.1")
	go func() {
		_ = listener.Start(ctx, func(_ context.Context, _ *mq.Message) error {
			received <- struct{}{}
			cancel()
			return nil
		})
	}()

	select {
	case <-received:
	case <-ctx.Done():
		t.Fatal("listener did not receive message within timeout")
	}

	time.Sleep(200 * time.Millisecond)

	rm := collectMetrics(t, reader)
	h := findHistogram(rm, "messaging.receive.duration")
	require.NotNil(t, h, "messaging.receive.duration histogram must exist")
	require.NotEmpty(t, h.DataPoints)
	assert.GreaterOrEqual(t, h.DataPoints[0].Count, uint64(1))
}

// TestOTelIntegration_ErrorsTotal verifies that messaging.errors.total is incremented
// when a send fails due to a connection error.
func TestOTelIntegration_ErrorsTotal(t *testing.T) {
	tc, _, reader := buildOTelConfig(t)

	cfg := mq.Config{
		QueueManagerName:     "QM1",
		ConnectionName:       "localhost(19999)", // unreachable
		ChannelName:          "DEV.APP.SVRCONN",
		Username:             "app",
		Password:             "passw0rd",
		MaxRetries:           0,
		InitialRetryInterval: 1 * time.Millisecond,
		Telemetry:            tc,
	}

	client, err := mq.NewClient(cfg)
	require.NoError(t, err)
	defer client.Close()

	sender := client.NewSender("ANY.QUEUE")
	_, sendErr := sender.Send(context.Background(), "will fail")
	assert.Error(t, sendErr)

	rm := collectMetrics(t, reader)
	ctr := findCounter(rm, "messaging.errors.total")
	require.NotNil(t, ctr, "messaging.errors.total counter must exist")
	require.NotEmpty(t, ctr.DataPoints)
	assert.GreaterOrEqual(t, ctr.DataPoints[0].Value, int64(1))
}

// spanNames returns the names of all collected spans for diagnostic messages.
func spanNames(spans tracetest.SpanStubs) []string {
	names := make([]string, len(spans))
	for i, s := range spans {
		names[i] = s.Name
	}
	return names
}
