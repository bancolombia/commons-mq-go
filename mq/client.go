package mq

import (
	"errors"
	"sync"
)

// Client is the central entry point for the commons-mq-go library.
// It holds a validated Config and manages the lifecycle of all resources
// (connections, goroutines) created through its factory methods.
//
// Create a Client with [NewClient], then use the factory methods to obtain
// [Listener], [Sender], and RequestReplyClient instances.
// Call [Client.Close] once to release all resources.
type Client struct {
	cfg Config

	mu            sync.Mutex
	closed        bool
	closers       []func() error
	healthProbers []healthProber
}

// NewClient creates a new Client from the provided Config.
//
// Defaults are applied to zero-value fields (InputConcurrency, OutputConcurrency,
// MaxRetries, InitialRetryInterval, RetryMultiplier) before validation.
// Returns an error if Config validation fails; never returns an error that
// contains credential values.
//
// NewClient itself does not open any IBM MQ connections; connections are
// established lazily when [Listener.Start] or [Sender.Send] are first called.
func NewClient(cfg Config) (*Client, error) {
	cfg.applyDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &Client{cfg: cfg}, nil
}

// Close shuts down all connections and goroutines managed by this Client.
// It blocks until all in-flight operations complete.
//
// Close is idempotent: calling it more than once returns nil.
func (c *Client) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	closers := c.closers
	c.closers = nil
	c.mu.Unlock()

	var errs []error
	// Close in reverse registration order so downstream resources are released first.
	for i := len(closers) - 1; i >= 0; i-- {
		if err := closers[i](); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// register enqueues a cleanup function to be called by Close.
// Factory methods (NewListener, NewSender, etc.) call this to register
// their teardown logic.
func (c *Client) register(fn func() error) {
	c.mu.Lock()
	c.closers = append(c.closers, fn)
	c.mu.Unlock()
}

// NewListener creates a Listener for the named queue with the provided options.
// Options override the corresponding [Config] defaults (InputConcurrency, MaxRetries).
// Call [Listener.Start] to begin consuming messages.
//
// NewListener does not open any IBM MQ connections; connections are established
// lazily when [Listener.Start] is called.
func (c *Client) NewListener(queueName string, opts ...ListenerOption) *Listener {
	l := &Listener{
		client:      c,
		queue:       queueName,
		concurrency: c.cfg.InputConcurrency,
		maxRetries:  c.cfg.MaxRetries,
		tel:         newMQTelemetry(c.cfg.Telemetry),
	}
	for _, opt := range opts {
		opt(l)
	}
	// Register a health prober that reads the listener's last known connection state.
	c.registerHealthProber(listenerHealthProber(l, func() ConnectionState {
		return ConnectionState(l.lastConnState.Load())
	}))
	return l
}

// NewSender creates a Sender that publishes to destinationQueue.
// If destinationQueue is empty, [Config.OutputQueue] is used as the default.
// Options override the corresponding [Config] defaults (OutputConcurrency).
//
// The Sender's connection pool is populated lazily on the first send call.
// The Sender is automatically closed when [Client.Close] is called.
func (c *Client) NewSender(destinationQueue string, opts ...SenderOption) *Sender {
	if destinationQueue == "" {
		destinationQueue = c.cfg.OutputQueue
	}
	s := &Sender{
		client:   c,
		queue:    destinationQueue,
		maxConns: c.cfg.OutputConcurrency,
		pool:     make(chan *connection, c.cfg.OutputConcurrency),
		tel:      newMQTelemetry(c.cfg.Telemetry),
	}
	for _, opt := range opts {
		opt(s)
	}
	c.register(s.close)
	c.registerHealthProber(senderHealthProber(s))
	return s
}

// NewRequestReplyClient creates a [RequestReplyClient] in [TemporaryQueue] mode.
//
// Each call to [RequestReplyClient.RequestReply] opens a unique dynamic reply
// queue, sends the request, and blocks until a correlated response arrives or
// the caller-provided timeout elapses.
//
// The returned client is safe for concurrent use.
func (c *Client) NewRequestReplyClient(requestQueue string, opts ...RROption) *RequestReplyClient {
	r := &RequestReplyClient{
		client:       c,
		requestQueue: requestQueue,
		mode:         TemporaryQueue,
		tel:          newMQTelemetry(c.cfg.Telemetry),
	}
	for _, opt := range opts {
		opt(r)
	}
	c.register(r.Close)
	return r
}

// NewRequestReplyClientFixed creates a [RequestReplyClient] in [FixedQueueSelector] mode.
//
// Responses are received from replyQueue — a shared, pre-existing IBM MQ queue.
// Each concurrent call generates a unique correlation ID; IBM MQ routes each
// response to the correct waiting caller via MQMO_MATCH_CORREL_ID.
//
// The returned client is safe for concurrent use.
func (c *Client) NewRequestReplyClientFixed(requestQueue, replyQueue string, opts ...RROption) *RequestReplyClient {
	r := &RequestReplyClient{
		client:       c,
		requestQueue: requestQueue,
		replyQueue:   replyQueue,
		mode:         FixedQueueSelector,
		registry:     newFixedRRRegistry(),
		tel:          newMQTelemetry(c.cfg.Telemetry),
	}
	for _, opt := range opts {
		opt(r)
	}
	c.register(r.Close)
	return r
}

// Config returns a copy of the validated, defaults-applied configuration.
func (c *Client) Config() Config {
	return c.cfg
}
