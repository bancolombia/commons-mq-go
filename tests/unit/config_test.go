package unit_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/bancolombia/commons-mq-go/mq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// minimalValid returns the smallest set of fields that passes Validate() after
// NewClient applies defaults, so we can compose test cases by overriding one field.
func minimalValid() mq.Config {
	return mq.Config{
		QueueManagerName:     "QM1",
		ConnectionName:       "localhost(1414)",
		ChannelName:          "DEV.APP.SVRCONN",
		InputConcurrency:     1,
		OutputConcurrency:    1,
		MaxRetries:           0, // 0 = no retries, still valid
		RetryMultiplier:      1.0,
		InitialRetryInterval: time.Second,
	}
}

// ── Required field presence ──────────────────────────────────────────────────

func TestConfig_Validate_RequiredFields(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*mq.Config)
		wantMsg string
	}{
		{
			name:    "missing QueueManagerName",
			mutate:  func(c *mq.Config) { c.QueueManagerName = "" },
			wantMsg: "QueueManagerName is required",
		},
		{
			name:    "missing ConnectionName",
			mutate:  func(c *mq.Config) { c.ConnectionName = "" },
			wantMsg: "ConnectionName is required",
		},
		{
			name:    "missing ChannelName",
			mutate:  func(c *mq.Config) { c.ChannelName = "" },
			wantMsg: "ChannelName is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := minimalValid()
			tt.mutate(&cfg)
			err := cfg.Validate()
			require.Error(t, err)
			assert.ErrorIs(t, err, mq.ErrInvalidConfig)
			assert.Contains(t, err.Error(), tt.wantMsg)
		})
	}
}

func TestConfig_Validate_AllRequiredFieldsPresent_OK(t *testing.T) {
	cfg := minimalValid()
	err := cfg.Validate()
	assert.NoError(t, err)
}

// ── Boundary values ──────────────────────────────────────────────────────────

func TestConfig_Validate_InputConcurrency(t *testing.T) {
	t.Run("zero is invalid", func(t *testing.T) {
		cfg := minimalValid()
		cfg.InputConcurrency = 0
		err := cfg.Validate()
		require.Error(t, err)
		assert.ErrorIs(t, err, mq.ErrInvalidConfig)
		assert.Contains(t, err.Error(), "InputConcurrency")
	})

	t.Run("negative is invalid", func(t *testing.T) {
		cfg := minimalValid()
		cfg.InputConcurrency = -1
		err := cfg.Validate()
		require.Error(t, err)
		assert.ErrorIs(t, err, mq.ErrInvalidConfig)
	})

	t.Run("one is valid", func(t *testing.T) {
		cfg := minimalValid()
		cfg.InputConcurrency = 1
		assert.NoError(t, cfg.Validate())
	})

	t.Run("large value is valid", func(t *testing.T) {
		cfg := minimalValid()
		cfg.InputConcurrency = 100
		assert.NoError(t, cfg.Validate())
	})
}

func TestConfig_Validate_OutputConcurrency(t *testing.T) {
	t.Run("zero is invalid", func(t *testing.T) {
		cfg := minimalValid()
		cfg.OutputConcurrency = 0
		err := cfg.Validate()
		require.Error(t, err)
		assert.ErrorIs(t, err, mq.ErrInvalidConfig)
		assert.Contains(t, err.Error(), "OutputConcurrency")
	})

	t.Run("one is valid", func(t *testing.T) {
		cfg := minimalValid()
		cfg.OutputConcurrency = 1
		assert.NoError(t, cfg.Validate())
	})
}

func TestConfig_Validate_RetryMultiplier(t *testing.T) {
	t.Run("below 1.0 is invalid", func(t *testing.T) {
		cfg := minimalValid()
		cfg.RetryMultiplier = 0.5
		err := cfg.Validate()
		require.Error(t, err)
		assert.ErrorIs(t, err, mq.ErrInvalidConfig)
		assert.Contains(t, err.Error(), "RetryMultiplier")
	})

	t.Run("0.0 is invalid", func(t *testing.T) {
		cfg := minimalValid()
		cfg.RetryMultiplier = 0.0
		err := cfg.Validate()
		require.Error(t, err)
	})

	t.Run("exactly 1.0 is valid (no growth)", func(t *testing.T) {
		cfg := minimalValid()
		cfg.RetryMultiplier = 1.0
		assert.NoError(t, cfg.Validate())
	})

	t.Run("2.0 is valid", func(t *testing.T) {
		cfg := minimalValid()
		cfg.RetryMultiplier = 2.0
		assert.NoError(t, cfg.Validate())
	})
}

func TestConfig_Validate_ProducerTTL(t *testing.T) {
	t.Run("negative is invalid", func(t *testing.T) {
		cfg := minimalValid()
		cfg.ProducerTTL = -1 * time.Second
		err := cfg.Validate()
		require.Error(t, err)
		assert.ErrorIs(t, err, mq.ErrInvalidConfig)
		assert.Contains(t, err.Error(), "ProducerTTL")
	})

	t.Run("zero is valid (indefinite)", func(t *testing.T) {
		cfg := minimalValid()
		cfg.ProducerTTL = 0
		assert.NoError(t, cfg.Validate())
	})

	t.Run("positive is valid", func(t *testing.T) {
		cfg := minimalValid()
		cfg.ProducerTTL = 5 * time.Second
		assert.NoError(t, cfg.Validate())
	})
}

func TestConfig_Validate_MaxRetries(t *testing.T) {
	t.Run("negative is invalid", func(t *testing.T) {
		cfg := minimalValid()
		cfg.MaxRetries = -1
		err := cfg.Validate()
		require.Error(t, err)
		assert.ErrorIs(t, err, mq.ErrInvalidConfig)
		assert.Contains(t, err.Error(), "MaxRetries")
	})

	t.Run("zero is valid (no retries)", func(t *testing.T) {
		cfg := minimalValid()
		cfg.MaxRetries = 0
		assert.NoError(t, cfg.Validate())
	})

	t.Run("positive is valid", func(t *testing.T) {
		cfg := minimalValid()
		cfg.MaxRetries = 10
		assert.NoError(t, cfg.Validate())
	})
}

// ── Multiple simultaneous errors ─────────────────────────────────────────────

func TestConfig_Validate_MultipleErrors_AllReported(t *testing.T) {
	cfg := mq.Config{} // all zero values — every required field missing
	err := cfg.Validate()
	require.Error(t, err)
	assert.ErrorIs(t, err, mq.ErrInvalidConfig)
	// All three required field names must appear.
	assert.Contains(t, err.Error(), "QueueManagerName")
	assert.Contains(t, err.Error(), "ConnectionName")
	assert.Contains(t, err.Error(), "ChannelName")
}

// ── Defaults applied by NewClient ────────────────────────────────────────────

func TestNewClient_AppliesDefaults(t *testing.T) {
	// Provide only required fields; NewClient fills in the rest.
	client, err := mq.NewClient(mq.Config{
		QueueManagerName: "QM1",
		ConnectionName:   "localhost(1414)",
		ChannelName:      "DEV.APP.SVRCONN",
	})
	require.NoError(t, err)
	defer client.Close()

	cfg := client.Config()
	assert.Equal(t, 1, cfg.InputConcurrency, "InputConcurrency default is 1")
	assert.Equal(t, 1, cfg.OutputConcurrency, "OutputConcurrency default is 1")
	assert.Equal(t, 10, cfg.MaxRetries, "MaxRetries default is 10")
	assert.Equal(t, 2*time.Second, cfg.InitialRetryInterval, "InitialRetryInterval default is 2s")
	assert.Equal(t, 2.0, cfg.RetryMultiplier, "RetryMultiplier default is 2.0")
}

func TestNewClient_ExplicitValuesNotOverwritten(t *testing.T) {
	client, err := mq.NewClient(mq.Config{
		QueueManagerName:     "QM1",
		ConnectionName:       "localhost(1414)",
		ChannelName:          "DEV.APP.SVRCONN",
		InputConcurrency:     5,
		OutputConcurrency:    3,
		MaxRetries:           20,
		InitialRetryInterval: 500 * time.Millisecond,
		RetryMultiplier:      1.5,
	})
	require.NoError(t, err)
	defer client.Close()

	cfg := client.Config()
	assert.Equal(t, 5, cfg.InputConcurrency)
	assert.Equal(t, 3, cfg.OutputConcurrency)
	assert.Equal(t, 20, cfg.MaxRetries)
	assert.Equal(t, 500*time.Millisecond, cfg.InitialRetryInterval)
	assert.InDelta(t, 1.5, cfg.RetryMultiplier, 0.001)
}

// ── NewClient surfaces ErrInvalidConfig ──────────────────────────────────────

func TestNewClient_InvalidConfig_ReturnsErrInvalidConfig(t *testing.T) {
	_, err := mq.NewClient(mq.Config{}) // missing all required fields
	require.Error(t, err)
	assert.True(t, errors.Is(err, mq.ErrInvalidConfig))
}

// ── Client.Close idempotency ──────────────────────────────────────────────────

func TestClient_Close_Idempotent(t *testing.T) {
	client, err := mq.NewClient(mq.Config{
		QueueManagerName: "QM1",
		ConnectionName:   "localhost(1414)",
		ChannelName:      "DEV.APP.SVRCONN",
	})
	require.NoError(t, err)

	assert.NoError(t, client.Close(), "first Close should succeed")
	assert.NoError(t, client.Close(), "second Close should be a no-op")
}

// ── Security: credential values must not appear in validation errors ──────────

func TestConfig_Validate_CredentialsNotLeakedInErrors(t *testing.T) {
	cfg := mq.Config{
		// Required fields missing — guarantees a validation error.
		Username: "secret-user",
		Password: "super-secret-password",
		TLS: &mq.TLSConfig{
			KeyRepository:    "/path/to/secret/keystore",
			CertificateLabel: "my-secret-cert",
		},
	}
	err := cfg.Validate()
	require.Error(t, err)

	errStr := err.Error()
	assert.False(t, strings.Contains(errStr, "secret-user"), "Username must not appear in error")
	assert.False(t, strings.Contains(errStr, "super-secret-password"), "Password must not appear in error")
	assert.False(t, strings.Contains(errStr, "/path/to/secret/keystore"), "KeyRepository must not appear in error")
	assert.False(t, strings.Contains(errStr, "my-secret-cert"), "CertificateLabel must not appear in error")
}

// ── TLSConfig optional ────────────────────────────────────────────────────────

func TestConfig_Validate_TLSConfig_Nil_IsValid(t *testing.T) {
	cfg := minimalValid()
	cfg.TLS = nil
	assert.NoError(t, cfg.Validate())
}

func TestConfig_Validate_TLSConfig_Present_DoesNotAffectValidation(t *testing.T) {
	cfg := minimalValid()
	cfg.TLS = &mq.TLSConfig{
		KeyRepository:    "/var/mqm/ssl/keystore",
		CertificateLabel: "ibmwebspheremq",
		CipherSpec:       "TLS_RSA_WITH_AES_256_CBC_SHA256",
	}
	assert.NoError(t, cfg.Validate())
}
