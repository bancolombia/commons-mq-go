//go:build integration

package bench_test

// SC-003: Reconnect timing benchmark — measures time from outage detection to
// first successfully delivered message after an IBM MQ container restart.
// Target: ≤ 30 seconds total recovery time.
//
// Run with:
//   go test -tags integration -bench=BenchmarkReconnect ./tests/bench/ -benchtime=1x

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/bancolombia/commons-mq-go/mq"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

func startBenchMQContainerWithRef(tb testing.TB) (testcontainers.Container, string, int) {
	tb.Helper()
	ctx := context.Background()

	img := os.Getenv("MQ_IMAGE_TAG")
	if img == "" {
		img = "ibm-mqadvanced-server-dev:9.4.5.0-arm64"
		tb.Logf("bench: using default MQ Image %s", img)
	} else {
		tb.Logf("bench: using external MQ Image configured via env 'MQ_IMAGE_TAG' : %s", img)
	}

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
			wait.ForLog("CWWKF0011I"),
		).WithStartupTimeout(60 * time.Second),
	}
	ctr, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		tb.Fatalf("start MQ container: %v", err)
	}
	tb.Logf("MQ container started with ID %s", ctr.GetContainerID())

	h, err := ctr.Host(ctx)
	if err != nil {
		tb.Fatalf("get host: %v", err)
	}
	tb.Logf("MQ container started with host %s", h)

	p, err := ctr.MappedPort(ctx, "1414/tcp")
	if err != nil {
		tb.Fatalf("get port: %v", err)
	}
	tb.Logf("MQ container started with port %d", p.Int())
	return ctr, h, p.Int()
}

// BenchmarkReconnect_ListenerRecovery measures the time from outage detection to
// first successfully received message after the IBM MQ container restarts.
// SC-003 target: ≤ 30 seconds.
func BenchmarkReconnect_ListenerRecovery(b *testing.B) {
	ctr, host, port := startBenchMQContainerWithRef(b)
	b.Cleanup(func() {
		timeout := 10 * time.Second
		_ = ctr.Stop(context.Background(), &timeout)
	})

	cfg := benchMQConfig(host, port)
	cfg.MaxRetries = 20
	cfg.InitialRetryInterval = 1 * time.Second
	cfg.RetryMultiplier = 1.5

	client, err := mq.NewClient(cfg)
	if err != nil {
		b.Fatalf("NewClient: %v", err)
	}
	defer client.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	received := make(chan struct{}, 1)
	listener := client.NewListener("DEV.QUEUE.1")
	go func() {
		_ = listener.Start(ctx, func(_ context.Context, _ *mq.Message) error {

			select {
			case received <- struct{}{}:
			default:
			}
			return nil
		})
	}()

	// Wait for listener to be ready by giving it time to connect.
	time.Sleep(2 * time.Second)

	// assert we can send a message before the outage to confirm connectivity.
	preSender := client.NewSender("DEV.QUEUE.1")
	msgid, errPs := preSender.Send(context.Background(), "test-msg-before-outage")
	if errPs != nil {
		b.Fatalf("send pre-outage message: %v", errPs)
	}
	b.Logf("Sent pre-outage message with ID %s", msgid)

	time.Sleep(2 * time.Second)

	b.ResetTimer()
	for range b.N {
		// Stop the container to simulate outage.
		stopTimeout := 5 * time.Second
		if err := ctr.Stop(context.Background(), &stopTimeout); err != nil {
			b.Fatalf("stop container: %v", err)
		}

		outageDetected := time.Now()

		// Restart the container.
		if err := ctr.Start(context.Background()); err != nil {
			b.Fatalf("restart container: %v", err)
		}

		// Send a probe message once container is up (using fresh direct connection).
		time.Sleep(5 * time.Second) // allow queue manager to reinitialise

		newHost, _ := ctr.Host(context.Background())
		newPort, _ := ctr.MappedPort(context.Background(), "1414/tcp")
		b.Logf("MQ container restarted with host %s and port %d", newHost, newPort.Int())

		probeCfg := benchMQConfig(newHost, newPort.Int())
		probeClient, err := mq.NewClient(probeCfg)
		if err == nil {
			probeSender := probeClient.NewSender("DEV.QUEUE.1")
			_, _ = probeSender.Send(context.Background(), "reconnect-probe")
			_ = probeClient.Close()
		}
		b.Log("Probe message sent to restarted container")

		// Wait for the listener to deliver the message.
		deadline := time.After(30 * time.Second)
		select {
		case <-received:
			recovery := time.Since(outageDetected)
			b.ReportMetric(recovery.Seconds(), "recovery_s")
			if recovery > 30*time.Second {
				b.Errorf("SC-003 FAIL: recovery took %.1fs > 30s target", recovery.Seconds())
			}
		case <-deadline:
			b.Error("SC-003 FAIL: listener did not recover within 30 seconds")
		}
	}
}
