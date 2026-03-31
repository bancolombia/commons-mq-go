//go:build integration

package bench_test

// SC-002: Throughput benchmark — asserts ≥ 1,000 messages/sec per concurrent
// consumer connection under sustained send + receive load.
//
// Run with:
//   go test -tags integration -bench=BenchmarkThroughput ./tests/bench/ -benchtime=10s

import (
	"context"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bancolombia/commons-mq-go/mq"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// startBenchMQContainer returns the host and port of an IBM MQ server for
// benchmarks. If MQ_HOST and MQ_PORT are set in the environment the existing
// server is used directly and no container is started. Otherwise a fresh IBM MQ
// container is launched via testcontainers and torn down at the end of the test.
func startBenchMQContainer(tb testing.TB) (host string, port int) {
	tb.Helper()

	// ── Use an already-running server when the env vars are present ───────────
	if h := os.Getenv("MQ_HOST"); h != "" {
		p, err := strconv.Atoi(os.Getenv("MQ_PORT"))
		if err != nil || p == 0 {
			tb.Fatalf("MQ_HOST is set but MQ_PORT is missing or invalid")
		}
		tb.Logf("bench: using external MQ server at %s:%d", h, p)
		return h, p
	}

	img := os.Getenv("MQ_IMAGE_TAG")
	if img == "" {
		img = "ibm-mqadvanced-server-dev:9.4.5.0-arm64"
		tb.Logf("bench: using default MQ Image %s", img)
	} else {
		tb.Logf("bench: using external MQ Image configured via env 'MQ_IMAGE_TAG' : %s", img)
	}

	// ── Fall back to testcontainers ───────────────────────────────────────────
	ctx := context.Background()
	req := testcontainers.ContainerRequest{
		Image: img,
		Env: map[string]string{
			"LICENSE":         "accept",
			"MQ_QMGR_NAME":    "QM1",
			"MQ_APP_PASSWORD": "passw0rd",
		},
		ExposedPorts: []string{"1414/tcp"},
		WaitingFor: wait.ForAll(
			wait.ForListeningPort("1414/tcp"),
			//wait.ForLog("AMQ9003I"),
		).WithStartupTimeout(60 * time.Second),
	}
	ctr, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		tb.Fatalf("start MQ container: %v", err)
	}
	tb.Cleanup(func() {
		timeout := 10 * time.Second
		_ = ctr.Stop(ctx, &timeout)
	})
	h, err := ctr.Host(ctx)
	if err != nil {
		tb.Fatalf("get host: %v", err)
	}
	p, err := ctr.MappedPort(ctx, "1414/tcp")
	if err != nil {
		tb.Fatalf("get port: %v", err)
	}
	return h, p.Int()
}

// benchMQConfig builds a mq.Config for the given host/port.
// Connection settings are read from the environment when present so that
// an external server can be targeted without modifying the source:
//
//	MQ_QUEUE_MANAGER  queue manager name   (default: QM1)
//	MQ_CHANNEL        channel name         (default: DEV.APP.SVRCONN)
//	MQ_USERNAME       app user             (default: app)
//	MQ_PASSWORD       app password         (default: passw0rd)
func benchMQConfig(host string, port int) mq.Config {
	return mq.Config{
		QueueManagerName:  envOrDefaultBench("MQ_QUEUE_MANAGER", "QM1"),
		ConnectionName:    host + "(" + itoa(port) + ")",
		ChannelName:       envOrDefaultBench("MQ_CHANNEL", "DEV.APP.SVRCONN"),
		Username:          envOrDefaultBench("MQ_USERNAME", "app"),
		Password:          envOrDefaultBench("MQ_PASSWORD", "passw0rd"),
		InputConcurrency:  1,
		OutputConcurrency: 1,
	}
}

func envOrDefaultBench(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}

func itoa(n int) string {
	return string(rune('0'+n/1000)) + string(rune('0'+(n/100)%10)) +
		string(rune('0'+(n/10)%10)) + string(rune('0'+n%10))
}

// BenchmarkThroughput_SendReceive measures sustained messages per second using
// one sender goroutine and one listener with InputConcurrency=1.
// SC-002 target: ≥ 1,000 msg/s.
//
// The number of messages sent is max(b.N, MQ_BENCH_MESSAGES).
// MQ_BENCH_MESSAGES defaults to 500 so that a meaningful load is always applied
// even when the benchmark framework would otherwise keep b.N at 1.
// Override via:
//
//	MQ_BENCH_MESSAGES=2000 go test -tags integration -bench=BenchmarkThroughput ./tests/bench/ -benchtime=1x
func BenchmarkThroughput_SendReceive(b *testing.B) {
	minMsgs := 500
	if v := os.Getenv("MQ_BENCH_MESSAGES"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			minMsgs = n
		}
	}

	host, port := startBenchMQContainer(b)
	cfg := benchMQConfig(host, port)

	client, err := mq.NewClient(cfg)
	if err != nil {
		b.Fatalf("NewClient: %v", err)
	}
	defer client.Close()

	sender := client.NewSender("DEV.QUEUE.1")

	// Pre-load b.N messages onto the queue.
	payload := make([]byte, 256) // 256-byte message
	for i := range payload {
		payload[i] = byte(i & 0xFF)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var received atomic.Int64

	listener := client.NewListener("DEV.QUEUE.1",
		mq.WithListenerConcurrency(1))
	listenerReady := make(chan struct{})
	var once sync.Once
	go func() {
		_ = listener.Start(ctx, func(_ context.Context, _ *mq.Message) error {
			once.Do(func() { close(listenerReady) })
			received.Add(1)
			return nil
		})
	}()
	// Send a warm-up message so the listener signals readiness on its first
	// delivery. Without this the listenerReady channel never closes because no
	// messages are in flight yet.
	if _, err := sender.Send(context.Background(), "warmup"); err != nil {
		b.Fatalf("warmup Send: %v", err)
	}
	select {
	case <-listenerReady:
	case <-time.After(30 * time.Second):
		b.Fatal("listener did not start in time")
	}
	received.Store(0) // discard the warm-up message from the counter

	nMsgs := b.N
	if nMsgs < minMsgs {
		nMsgs = minMsgs
	}

	b.ResetTimer()
	start := time.Now()

	for i := range nMsgs {
		_ = i
		if _, err := sender.Send(context.Background(), string(payload)); err != nil {
			b.Fatalf("Send: %v", err)
		}
	}
	b.Logf("sent %d messages, waiting for receive...", nMsgs)

	// Wait for all messages to be received.
	deadline := time.Now().Add(60 * time.Second)
	for received.Load() < int64(nMsgs) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}

	elapsed := time.Since(start)
	throughput := float64(nMsgs) / elapsed.Seconds()
	b.ReportMetric(throughput, "msg/s")

	if throughput < 1000 {
		b.Errorf("SC-002 FAIL: throughput %.0f msg/s < 1000 msg/s target (sent %d messages)", throughput, nMsgs)
	}
}
