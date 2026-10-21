package mq

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"sync"
	"time"

	ibmmq "github.com/ibm-messaging/mq-golang/v5/ibmmq"
)

// sleepWithJitterCtx sleeps for d plus up to 10% random jitter to avoid
// thundering-herd reconnect storms when multiple connections are recovering
// simultaneously. It returns ctx.Err() if the context is cancelled first.
func sleepWithJitterCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		d = time.Millisecond
	}
	jitterCeil := int64(d / 10)
	if jitterCeil <= 0 {
		jitterCeil = 1
	}
	timer := time.NewTimer(d + time.Duration(rand.Int63n(jitterCeil)))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// retryDelay returns the capped exponential backoff for a reconnect attempt.
func retryDelay(cfg *Config, attempt int) time.Duration {
	d := BackoffDuration(cfg.InitialRetryInterval, cfg.RetryMultiplier, attempt)
	if d > cfg.MaxRetryInterval {
		d = cfg.MaxRetryInterval
	}
	return d
}

// BackoffDuration computes the reconnect delay for a given attempt index using
// simple exponential backoff:  initial * multiplier^attempt.
//
// Exported so unit tests can verify the calculation without triggering real
// network activity.
func BackoffDuration(initial time.Duration, multiplier float64, attempt int) time.Duration {
	d := float64(initial)
	for range attempt {
		d *= multiplier
	}
	return time.Duration(d)
}

// connection is an internal, unexported struct managing a single live IBM MQ connection.
// It is not part of the public API; Listener and Sender manage connection lifetimes.
type connection struct {
	queueManager ibmmq.MQQueueManager
	config       *Config

	mu        sync.RWMutex
	state     ConnectionState
	lastError error
}

// newConnection dials IBM MQ once and returns a live connection, or an error if
// the dial fails. The error never contains credential values.
func newConnection(cfg *Config) (*connection, error) {
	c := &connection{
		config: cfg,
		state:  StateReconnecting,
	}
	if err := c.dial(); err != nil {
		return nil, err
	}
	return c, nil
}

// newConnectionWithRetry dials IBM MQ, retrying up to Config.MaxRetries times
// with capped exponential backoff. It always makes at least one attempt, even
// when MaxRetries is 0. The context bounds every wait between attempts.
func newConnectionWithRetry(ctx context.Context, cfg *Config) (*connection, error) {
	attempts := cfg.MaxRetries
	if attempts < 1 {
		attempts = 1
	}

	var lastErr error
	for attempt := range attempts {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		c := &connection{config: cfg, state: StateReconnecting}
		if err := c.dial(); err == nil {
			return c, nil
		} else {
			lastErr = err
		}
		if attempt < attempts-1 {
			if err := sleepWithJitterCtx(ctx, retryDelay(cfg, attempt)); err != nil {
				return nil, err
			}
		}
	}

	return nil, fmt.Errorf("%w: queue manager %q failed to connect after %d attempts: %w",
		ErrConnectionFailed, cfg.QueueManagerName, attempts, lastErr)
}

// dial establishes (or re-establishes) the IBM MQ connection.
// Credential values are never included in returned errors.
func (c *connection) dial() error {
	cfg := c.config

	// Channel definition (MQCD) — identifies the server-connection channel and host.
	mqcd := ibmmq.NewMQCD()
	mqcd.ChannelName = cfg.ChannelName
	mqcd.ConnectionName = cfg.ConnectionName

	// Connection options (MQCNO) — client binding, attach channel and credentials.
	mqcno := ibmmq.NewMQCNO()
	mqcno.Options = ibmmq.MQCNO_CLIENT_BINDING
	mqcno.ClientConn = mqcd

	// Credentials (MQCSP) — only set when a username is provided.
	if cfg.Username != "" {
		mqcsp := ibmmq.NewMQCSP()
		mqcsp.AuthenticationType = ibmmq.MQCSP_AUTH_USER_ID_AND_PWD
		mqcsp.UserId = cfg.Username
		mqcsp.Password = cfg.Password
		mqcno.SecurityParms = mqcsp
	}

	// TLS settings (MQSCO) — only wire up when TLSConfig is provided.
	if cfg.TLS != nil {
		mqsco := ibmmq.NewMQSCO()
		mqsco.KeyRepository = cfg.TLS.KeyRepository
		mqsco.CertificateLabel = cfg.TLS.CertificateLabel
		mqsco.FipsRequired = cfg.TLS.FIPSRequired
		mqcno.SSLConfig = mqsco
		mqcd.SSLCipherSpec = cfg.TLS.CipherSpec
		mqcd.SSLPeerName = cfg.TLS.PeerName
	}

	qmgr, err := ibmmq.Connx(cfg.QueueManagerName, mqcno)
	if err != nil {
		// Sanitize: wrap with a message that identifies the queue manager name
		// but never includes Username, Password, or TLS credential paths.
		c.setState(StateFailed)
		c.setLastError(err)
		return fmt.Errorf("commons-mq-go: failed to connect to queue manager %q on %q: %w",
			cfg.QueueManagerName, cfg.ConnectionName, sanitizeError(err))
	}

	c.queueManager = qmgr
	c.setState(StateConnected)
	c.setLastError(nil)
	return nil
}

// State returns the current ConnectionState. Safe for concurrent use.
func (c *connection) State() ConnectionState {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.state
}

// setState updates the connection state. Safe for concurrent use.
func (c *connection) setState(s ConnectionState) {
	c.mu.Lock()
	c.state = s
	c.mu.Unlock()
}

// setLastError stores the most recent connection error.
func (c *connection) setLastError(err error) {
	c.mu.Lock()
	c.lastError = err
	c.mu.Unlock()
}

// LastError returns the most recent connection error, if any.
func (c *connection) LastError() error {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.lastError
}

// QueueManager returns a pointer to the underlying IBM MQ queue manager handle.
// Callers must check c.State() == StateConnected before using the handle.
// The pointer is only valid for the lifetime of this connection.
func (c *connection) QueueManager() *ibmmq.MQQueueManager {
	return &c.queueManager
}

// Close disconnects from IBM MQ. Safe to call once.
func (c *connection) Close() error {
	c.setState(StateFailed)
	if err := c.queueManager.Disc(); err != nil {
		return fmt.Errorf("commons-mq-go: failed to disconnect from queue manager %q: %w",
			c.config.QueueManagerName, sanitizeError(err))
	}
	return nil
}

// IsConnectionError reports whether err represents an IBM MQ connection-level failure
// that should trigger a reconnect attempt — as opposed to a queue or message error.
//
// Specifically it returns true for:
//   - MQRC_CONNECTION_BROKEN       (2009)
//   - MQRC_HCONN_ERROR             (2018)
//   - MQRC_Q_MGR_NOT_AVAILABLE     (2059)
//   - MQRC_Q_MGR_QUIESCING         (2161)
//   - MQRC_Q_MGR_STOPPING          (2162)
//   - MQRC_CONNECTION_QUIESCING    (2202)
//   - MQRC_CONNECTION_STOPPING     (2203)
//   - MQRC_Q_MGR_NOT_ACTIVE        (2223)
//   - MQRC_CONNECTION_ERROR        (2273)
//   - MQRC_CONNECTION_SUSPENDED    (2521)
//   - MQRC_CONNECTION_STOPPED      (2528)
func IsConnectionError(err error) bool {
	var mqRet *ibmmq.MQReturn
	if !errors.As(err, &mqRet) {
		return false
	}
	switch mqRet.MQRC {
	case ibmmq.MQRC_CONNECTION_BROKEN,
		ibmmq.MQRC_HCONN_ERROR,
		ibmmq.MQRC_Q_MGR_NOT_AVAILABLE,
		ibmmq.MQRC_Q_MGR_QUIESCING,
		ibmmq.MQRC_Q_MGR_STOPPING,
		ibmmq.MQRC_CONNECTION_QUIESCING,
		ibmmq.MQRC_CONNECTION_STOPPING,
		ibmmq.MQRC_Q_MGR_NOT_ACTIVE,
		ibmmq.MQRC_CONNECTION_ERROR,
		ibmmq.MQRC_CONNECTION_SUSPENDED,
		ibmmq.MQRC_CONNECTION_STOPPED:
		return true
	}
	return false
}

// Reconnect attempts to re-establish the IBM MQ connection using exponential backoff.
//
// It transitions the connection to [StateReconnecting], dials immediately, then
// retries up to Config.MaxRetries times with a capped delay between attempts.
// On success it returns nil and the connection is in [StateConnected].
// If ctx is cancelled it returns ctx.Err(). When all retries are exhausted it
// returns an error wrapping [ErrConnectionFailed] and the connection is in
// [StateFailed]. The error never contains credential values.
func (c *connection) Reconnect(ctx context.Context) error {
	c.setState(StateReconnecting)

	cfg := c.config
	attempts := cfg.MaxRetries
	if attempts < 1 {
		attempts = 1
	}

	var lastErr error
	for attempt := range attempts {
		if err := ctx.Err(); err != nil {
			return err
		}

		// Disconnect cleanly if we still have a handle before retrying.
		_ = c.queueManager.Disc()

		if err := c.dial(); err == nil {
			return nil
		} else {
			lastErr = err
		}

		if attempt < attempts-1 {
			if err := sleepWithJitterCtx(ctx, retryDelay(cfg, attempt)); err != nil {
				return err
			}
		}
	}

	c.setState(StateFailed)
	return fmt.Errorf("%w: queue manager %q exhausted %d reconnect attempts: %w",
		ErrConnectionFailed, cfg.QueueManagerName, attempts, lastErr)
}

// IsEmptyQueueError reports whether err is MQRC_NO_MSG_AVAILABLE (2033),
// which indicates the queue is empty and is not considered a real error.
func IsEmptyQueueError(err error) bool {
	var mqRet *ibmmq.MQReturn
	if !errors.As(err, &mqRet) {
		return false
	}
	return mqRet.MQRC == ibmmq.MQRC_NO_MSG_AVAILABLE
}

// sanitizeError wraps an IBM MQ error in a way that preserves the MQCC/MQRC codes
// but never echoes back any credential material that may have been in the MQCNO.
// For *ibmmq.MQReturn, the standard Error() method only prints MQCC/MQRC/verb —
// no credentials — so we can safely wrap it directly.
func sanitizeError(err error) error {
	if err == nil {
		return nil
	}
	var mqRet *ibmmq.MQReturn
	if errors.As(err, &mqRet) {
		// MQReturn.Error() format: "MQCC=2 MQRC=2059 MQRC_Q_MGR_NOT_AVAILABLE"
		// This contains no credential information — safe to wrap as-is.
		return err
	}
	// For any other error type, return a generic message to avoid leaking details.
	return errors.New("IBM MQ operation failed")
}
