//go:build integration

package integration_test

// T078: SD-002 TLS — connect to IBM MQ configured with TLS using a self-signed
// certificate pair; assert Listener.Start() succeeds and a message round-trip completes.
//
// This test requires a separately provisioned IBM MQ container with TLS configured.
// It reads connection parameters from environment variables so it can run in any
// environment that provides an MQ+TLS instance:
//
//   MQ_TLS_HOST         — MQ host (e.g. "localhost")
//   MQ_TLS_PORT         — MQ listener port (e.g. "1415")
//   MQ_TLS_QMGR         — Queue manager name (e.g. "QM1")
//   MQ_TLS_CHANNEL      — Channel name (e.g. "DEV.SSL.SVRCONN")
//   MQ_TLS_KEY_REPO     — Path to key repository without extension
//   MQ_TLS_CERT_LABEL   — Certificate label
//   MQ_TLS_CIPHER_SPEC  — TLS cipher spec (e.g. "TLS_RSA_WITH_AES_256_CBC_SHA256")
//   MQ_TLS_QUEUE        — Queue name to use for the round-trip test
//
// If any required variable is missing the test is skipped automatically.

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/bancolombia/commons-mq-go/mq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// tlsConfigFromEnv loads TLS test parameters from environment variables.
// Returns nil and a skip message if any required variable is absent.
func tlsConfigFromEnv() (*mq.Config, string) {
	vars := map[string]string{
		"MQ_TLS_HOST":        "",
		"MQ_TLS_PORT":        "",
		"MQ_TLS_QMGR":        "",
		"MQ_TLS_CHANNEL":     "",
		"MQ_TLS_KEY_REPO":    "",
		"MQ_TLS_CERT_LABEL":  "",
		"MQ_TLS_CIPHER_SPEC": "",
		"MQ_TLS_QUEUE":       "",
	}
	for k := range vars {
		v := os.Getenv(k)
		if v == "" {
			return nil, fmt.Sprintf("skipping TLS integration test: env var %s is not set", k)
		}
		vars[k] = v
	}

	port, err := strconv.Atoi(vars["MQ_TLS_PORT"])
	if err != nil {
		return nil, fmt.Sprintf("skipping TLS integration test: MQ_TLS_PORT=%q is not a valid port", vars["MQ_TLS_PORT"])
	}

	cfg := &mq.Config{
		QueueManagerName:     vars["MQ_TLS_QMGR"],
		ConnectionName:       fmt.Sprintf("%s(%d)", vars["MQ_TLS_HOST"], port),
		ChannelName:          vars["MQ_TLS_CHANNEL"],
		InputConcurrency:     1,
		MaxRetries:           2,
		InitialRetryInterval: 500 * time.Millisecond,
		TLS: &mq.TLSConfig{
			KeyRepository:    vars["MQ_TLS_KEY_REPO"],
			CertificateLabel: vars["MQ_TLS_CERT_LABEL"],
			CipherSpec:       vars["MQ_TLS_CIPHER_SPEC"],
		},
	}
	return cfg, ""
}

// T078: Connect to IBM MQ over TLS; assert Listener.Start succeeds and a message
// round-trip completes successfully.
func TestListener_Integration_TLS_MessageRoundTrip(t *testing.T) {
	cfg, skipMsg := tlsConfigFromEnv()
	if skipMsg != "" {
		t.Skip(skipMsg)
	}
	queueName := os.Getenv("MQ_TLS_QUEUE")

	client, err := mq.NewClient(*cfg)
	require.NoError(t, err, "NewClient must succeed with valid TLS config")
	defer client.Close()

	const payload = "tls-round-trip-test-payload"
	putMessage(t, *cfg, queueName, payload)

	received := make(chan string, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	l := client.NewListener(queueName)
	go func() {
		_ = l.Start(ctx, func(_ context.Context, msg *mq.Message) error {
			received <- string(msg.Body)
			cancel()
			return nil
		})
	}()

	select {
	case body := <-received:
		assert.Equal(t, payload, body,
			"TLS round-trip: delivered message body must match sent payload")
	case <-time.After(5 * time.Second):
		t.Fatal("TLS round-trip: message was not delivered within 5 seconds")
	}
}

// TestListenerTLS_Start_MissingKeyRepository_ReturnsError verifies that supplying a
// TLSConfig with a key repository that does not exist causes Listener.Start to return
// a descriptive error without leaking credential paths.
func TestListenerTLS_Start_MissingKeyRepository_ReturnsError(t *testing.T) {
	cfg, skipMsg := tlsConfigFromEnv()
	if skipMsg != "" {
		t.Skip(skipMsg)
	}

	// Override with a non-existent key repository to force a connection failure.
	cfg.TLS.KeyRepository = "/non/existent/keyrepo"
	cfg.MaxRetries = 0

	client, err := mq.NewClient(*cfg)
	require.NoError(t, err)
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	l := client.NewListener(os.Getenv("MQ_TLS_QUEUE"), mq.WithListenerMaxRetries(0))
	startErr := l.Start(ctx, func(_ context.Context, _ *mq.Message) error { return nil })

	require.Error(t, startErr,
		"Listener.Start with invalid key repository must return an error")
	assert.NotContains(t, startErr.Error(), "/non/existent/keyrepo",
		"error must not leak the TLS.KeyRepository path")
}
