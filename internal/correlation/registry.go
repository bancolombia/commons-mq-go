// Package correlation provides a thread-safe in-memory registry that maps
// correlation IDs to response channels, enabling concurrent request-reply
// callers on a shared IBM MQ reply queue (FixedQueueSelector mode) to each
// receive only their own correlated response.
package correlation

import "sync"

// Registry is a thread-safe map from correlation ID (hex string) to a buffered
// response channel of type T.
//
// Typical usage in FixedQueueSelector request-reply:
//
//  1. Caller generates a unique correlation ID.
//  2. Calls Register to get a dedicated response channel.
//  3. Sends the request with that correlation ID.
//  4. A background goroutine receives the reply and calls Deliver.
//  5. Caller blocks on the channel (with a timeout select).
//  6. Caller calls Deregister to clean up regardless of success or timeout.
type Registry[T any] struct {
	mu      sync.RWMutex
	pending map[string]chan T
}

// NewRegistry creates an empty Registry.
func NewRegistry[T any]() *Registry[T] {
	return &Registry[T]{pending: make(map[string]chan T)}
}

// Register creates a buffered channel of capacity 1 for correlID and stores it
// in the registry. Returns the channel so the caller can block on it.
// If correlID is already registered the existing channel is replaced and returned.
func (r *Registry[T]) Register(correlID string) chan T {
	ch := make(chan T, 1)
	r.mu.Lock()
	r.pending[correlID] = ch
	r.mu.Unlock()
	return ch
}

// Deliver routes msg to the channel registered for correlID.
// If correlID is not registered (unknown or already deregistered) Deliver is
// a deliberate no-op — it never panics, never blocks, and never leaks goroutines.
func (r *Registry[T]) Deliver(correlID string, msg T) {
	r.mu.RLock()
	ch, ok := r.pending[correlID]
	r.mu.RUnlock()
	if !ok {
		return
	}
	// Non-blocking send: if the channel already has a value (duplicate delivery),
	// drop the duplicate rather than blocking the delivery goroutine.
	select {
	case ch <- msg:
	default:
	}
}

// Deregister removes the channel associated with correlID from the registry.
// The channel is NOT closed, so callers that are still blocked on it will
// simply never receive a value and can rely on their own timeout/context.
// Calling Deregister for an unknown correlID is a no-op.
func (r *Registry[T]) Deregister(correlID string) {
	r.mu.Lock()
	delete(r.pending, correlID)
	r.mu.Unlock()
}

// Len returns the number of currently registered correlation IDs.
// Primarily useful for tests.
func (r *Registry[T]) Len() int {
	r.mu.RLock()
	n := len(r.pending)
	r.mu.RUnlock()
	return n
}
