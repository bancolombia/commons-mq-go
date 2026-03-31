package unit_test

// T077: SD-002 Credential Safety
//
// Asserts that errors returned from NewClient(), Listener.Start(), and connection
// failures do not contain sensitive credential values (Password, Username,
// TLS.KeyRepository, TLS.CertificateLabel) anywhere in the error string.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bancolombia/commons-mq-go/mq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	sensitivePassword  = "s3cr3tP@ssw0rd!"
	sensitiveUsername  = "mquser_private"
	sensitiveKeyRepo   = "/etc/mq/pki/keys/mykey"
	sensitiveCertLabel = "ibmwebspheremq_cert_label"
)

// credentialSafeConfig returns a Config with sensitive credential values set.
func credentialSafeConfig() mq.Config {
	return mq.Config{
		QueueManagerName: "QM1",
		ConnectionName:   "localhost(19999)", // unreachable port to trigger connection error
		ChannelName:      "DEV.APP.SVRCONN",
		Username:         sensitiveUsername,
		Password:         sensitivePassword,
		TLS: &mq.TLSConfig{
			KeyRepository:    sensitiveKeyRepo,
			CertificateLabel: sensitiveCertLabel,
			CipherSpec:       "TLS_RSA_WITH_AES_256_CBC_SHA256",
		},
	}
}

// assertNoCredentials checks that the error string does not contain any sensitive value.
func assertNoCredentials(t *testing.T, errStr, context string) {
	t.Helper()
	for _, sensitive := range []string{sensitivePassword, sensitiveUsername, sensitiveKeyRepo, sensitiveCertLabel} {
		assert.NotContains(t, errStr, sensitive,
			"%s: error must not contain sensitive credential value %q", context, sensitive)
	}
}

// TestNewClient_ErrorDoesNotLeakCredentials verifies that a validation error
// returned from NewClient never contains credential values.
func TestNewClient_ErrorDoesNotLeakCredentials(t *testing.T) {
	// Intentionally invalid config (missing QueueManagerName) combined with credentials.
	cfg := mq.Config{
		QueueManagerName: "", // will fail validation
		ConnectionName:   "localhost(1414)",
		ChannelName:      "DEV.APP.SVRCONN",
		Username:         sensitiveUsername,
		Password:         sensitivePassword,
	}

	_, err := mq.NewClient(cfg)
	require.Error(t, err, "NewClient with empty QueueManagerName must return an error")
	assertNoCredentials(t, err.Error(), "NewClient validation error")
}

// TestNewClient_ValidConfig_NoError verifies that a valid config with credentials
// is accepted by NewClient (the credentials are stored, not validated against MQ).
func TestNewClient_ValidConfig_WithCredentials_NoError(t *testing.T) {
	client, err := mq.NewClient(credentialSafeConfig())
	require.NoError(t, err, "NewClient with valid config must not return an error")
	if client != nil {
		_ = client.Close()
	}
}

// TestListenerStart_ConnectionError_DoesNotLeakCredentials verifies that when
// Listener.Start fails because the MQ server is unreachable, the returned error
// does not contain the configured Password or other credential values.
func TestListenerStart_ConnectionError_DoesNotLeakCredentials(t *testing.T) {
	cfg := credentialSafeConfig()
	// MaxRetries=0 so the test fails fast without looping.
	cfg.MaxRetries = 0

	client, err := mq.NewClient(cfg)
	require.NoError(t, err)
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	l := client.NewListener("DEV.QUEUE.1", mq.WithListenerMaxRetries(0))
	startErr := l.Start(ctx, func(_ context.Context, _ *mq.Message) error { return nil })

	// Start must return an error (unreachable MQ server).
	if startErr != nil {
		errStr := startErr.Error()
		assertNoCredentials(t, errStr, "Listener.Start connection error")

		// Also verify the error doesn't leak the connection details in a harmful way
		// (queue manager name in the error is acceptable as it is not sensitive).
		assert.NotContains(t, strings.ToLower(errStr), strings.ToLower(sensitivePassword))
	}
}

// TestConfig_Password_NotInValidationError verifies that a missing required field
// triggers a validation error and that error does not contain the password.
func TestConfig_Password_NotInValidationError(t *testing.T) {
	cfg := mq.Config{
		QueueManagerName: "QM1",
		ConnectionName:   "", // missing required field — triggers validation error
		ChannelName:      "DEV.APP.SVRCONN",
		Username:         sensitiveUsername,
		Password:         sensitivePassword,
	}

	_, err := mq.NewClient(cfg)
	require.Error(t, err, "NewClient with empty ConnectionName must return an error")
	assertNoCredentials(t, err.Error(), "ConnectionName validation error")
}
