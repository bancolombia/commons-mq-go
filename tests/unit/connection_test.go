package unit_test

import (
	"errors"
	"testing"

	ibmmq "github.com/ibm-messaging/mq-golang/v5/ibmmq"

	"github.com/bancolombia/commons-mq-go/mq"
	"github.com/stretchr/testify/assert"
)

// ── MQRC error classification ─────────────────────────────────────────────────

// mqReturn creates a fake *ibmmq.MQReturn suitable for testing MQRC classification.
func mqReturn(cc, rc int32) error {
	return &ibmmq.MQReturn{
		MQCC: cc,
		MQRC: rc,
	}
}

func TestIsConnectionError_ConnectionCodes(t *testing.T) {
	connectionRCs := []struct {
		name string
		rc   int32
	}{
		{"MQRC_CONNECTION_BROKEN (2009)", ibmmq.MQRC_CONNECTION_BROKEN},
		{"MQRC_Q_MGR_NOT_AVAILABLE (2059)", ibmmq.MQRC_Q_MGR_NOT_AVAILABLE},
		{"MQRC_Q_MGR_QUIESCING (2161)", ibmmq.MQRC_Q_MGR_QUIESCING},
		{"MQRC_Q_MGR_STOPPING (2162)", ibmmq.MQRC_Q_MGR_STOPPING},
		{"MQRC_CONNECTION_QUIESCING (2202)", ibmmq.MQRC_CONNECTION_QUIESCING},
		{"MQRC_CONNECTION_STOPPING (2203)", ibmmq.MQRC_CONNECTION_STOPPING},
	}

	for _, tt := range connectionRCs {
		t.Run(tt.name, func(t *testing.T) {
			err := mqReturn(ibmmq.MQCC_FAILED, tt.rc)
			assert.True(t, mq.IsConnectionError(err),
				"MQRC %d should be classified as a connection error", tt.rc)
		})
	}
}

func TestIsConnectionError_NonConnectionCodes(t *testing.T) {
	nonConnectionRCs := []struct {
		name string
		rc   int32
	}{
		{"MQRC_NO_MSG_AVAILABLE (2033)", ibmmq.MQRC_NO_MSG_AVAILABLE},
		{"MQRC_Q_FULL (2053)", ibmmq.MQRC_Q_FULL},
		{"MQRC_NOT_AUTHORIZED (2035)", ibmmq.MQRC_NOT_AUTHORIZED},
		{"MQRC_Q_MGR_NAME_ERROR (2058)", ibmmq.MQRC_Q_MGR_NAME_ERROR},
	}

	for _, tt := range nonConnectionRCs {
		t.Run(tt.name, func(t *testing.T) {
			err := mqReturn(ibmmq.MQCC_FAILED, tt.rc)
			assert.False(t, mq.IsConnectionError(err),
				"MQRC %d should NOT be classified as a connection error", tt.rc)
		})
	}
}

func TestIsConnectionError_NilError(t *testing.T) {
	assert.False(t, mq.IsConnectionError(nil))
}

func TestIsConnectionError_NonMQError(t *testing.T) {
	assert.False(t, mq.IsConnectionError(errors.New("plain error")))
}

// ── Empty-queue classification ────────────────────────────────────────────────

func TestIsEmptyQueueError_ReturnsTrue_ForNoMsgAvailable(t *testing.T) {
	err := mqReturn(ibmmq.MQCC_FAILED, ibmmq.MQRC_NO_MSG_AVAILABLE)
	assert.True(t, mq.IsEmptyQueueError(err))
}

func TestIsEmptyQueueError_ReturnsFalse_ForConnectionErrors(t *testing.T) {
	connectionErrs := []int32{
		ibmmq.MQRC_CONNECTION_BROKEN,
		ibmmq.MQRC_Q_MGR_NOT_AVAILABLE,
		ibmmq.MQRC_Q_MGR_QUIESCING,
	}
	for _, rc := range connectionErrs {
		assert.False(t, mq.IsEmptyQueueError(mqReturn(ibmmq.MQCC_FAILED, rc)),
			"MQRC %d should not be an empty-queue error", rc)
	}
}

func TestIsEmptyQueueError_NilError(t *testing.T) {
	assert.False(t, mq.IsEmptyQueueError(nil))
}

func TestIsEmptyQueueError_NonMQError(t *testing.T) {
	assert.False(t, mq.IsEmptyQueueError(errors.New("plain error")))
}

// ── Mutual exclusivity ────────────────────────────────────────────────────────

// IsConnectionError and IsEmptyQueueError must be mutually exclusive for any
// IBM MQ return code — a code cannot be both.
func TestConnectionError_And_EmptyQueueError_AreMutuallyExclusive(t *testing.T) {
	knownRCs := []int32{
		ibmmq.MQRC_CONNECTION_BROKEN,
		ibmmq.MQRC_Q_MGR_NOT_AVAILABLE,
		ibmmq.MQRC_Q_MGR_QUIESCING,
		ibmmq.MQRC_Q_MGR_STOPPING,
		ibmmq.MQRC_CONNECTION_QUIESCING,
		ibmmq.MQRC_CONNECTION_STOPPING,
		ibmmq.MQRC_NO_MSG_AVAILABLE,
		ibmmq.MQRC_Q_FULL,
		ibmmq.MQRC_NOT_AUTHORIZED,
	}
	for _, rc := range knownRCs {
		err := mqReturn(ibmmq.MQCC_FAILED, rc)
		isConn := mq.IsConnectionError(err)
		isEmpty := mq.IsEmptyQueueError(err)
		assert.False(t, isConn && isEmpty,
			"MQRC %d cannot be both a connection error and an empty-queue error", rc)
	}
}

// ── ConnectionState constants ─────────────────────────────────────────────────

func TestConnectionState_Constants_HaveDistinctValues(t *testing.T) {
	states := []mq.ConnectionState{
		mq.StateConnected,
		mq.StateReconnecting,
		mq.StateFailed,
	}
	seen := make(map[mq.ConnectionState]bool)
	for _, s := range states {
		assert.False(t, seen[s], "ConnectionState %d is duplicated", s)
		seen[s] = true
	}
}

// ── newConnection fails gracefully without a real MQ server ──────────────────
//
// These tests exercise error-path behaviour in a CI environment where no MQ
// server is running. They verify that:
//   1. NewClient succeeds (lazy connection — no network dial in NewClient).
//   2. Config validation passes.
//
// The actual connection attempt (and state-machine transitions) are covered by
// integration tests that run against a real IBM MQ container.

func TestNewClient_SucceedsWithValidConfig_NoNetworkCallMade(t *testing.T) {
	// NewClient must not dial IBM MQ; it is a pure in-memory operation.
	client, err := mq.NewClient(mq.Config{
		QueueManagerName: "QM1",
		ConnectionName:   "localhost(1414)",
		ChannelName:      "DEV.APP.SVRCONN",
	})
	assert.NoError(t, err, "NewClient with valid config must not return an error")
	if client != nil {
		_ = client.Close()
	}
}

// ── ErrConnectionFailed sentinel ─────────────────────────────────────────────

func TestErrConnectionFailed_IsSentinel(t *testing.T) {
	// Confirm the sentinel is addressable and has a non-empty message.
	assert.NotNil(t, mq.ErrConnectionFailed)
	assert.NotEmpty(t, mq.ErrConnectionFailed.Error())
}

// ── ErrTimeout sentinel ───────────────────────────────────────────────────────

func TestErrTimeout_IsSentinel(t *testing.T) {
	assert.NotNil(t, mq.ErrTimeout)
	assert.NotEmpty(t, mq.ErrTimeout.Error())
}
