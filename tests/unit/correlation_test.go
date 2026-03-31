package unit_test

// T038: Unit tests for correlation.Registry.
// Verifies concurrent register/deliver/timeout, no cross-routing, and
// that Deliver for an unknown correlationID is a complete no-op.

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bancolombia/commons-mq-go/internal/correlation"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ── Register / Deliver basic ──────────────────────────────────────────────────

func TestRegistry_Register_ReturnsChannel(t *testing.T) {
	reg := correlation.NewRegistry[string]()
	ch := reg.Register("corr-1")
	assert.NotNil(t, ch, "Register must return a non-nil channel")
}

func TestRegistry_Deliver_RoutesValueToChannel(t *testing.T) {
	reg := correlation.NewRegistry[string]()
	ch := reg.Register("corr-1")
	reg.Deliver("corr-1", "hello")

	select {
	case v := <-ch:
		assert.Equal(t, "hello", v)
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Deliver did not send value to channel within timeout")
	}
}

func TestRegistry_Deliver_UnknownCorrelID_IsNoOp(t *testing.T) {
	reg := correlation.NewRegistry[string]()
	// Deliver to an ID that was never registered — must not panic or block.
	assert.NotPanics(t, func() {
		reg.Deliver("unknown-id", "payload")
	})
}

func TestRegistry_Deliver_AfterDeregister_IsNoOp(t *testing.T) {
	reg := correlation.NewRegistry[string]()
	reg.Register("corr-1")
	reg.Deregister("corr-1")
	// After deregister the channel is gone; Deliver must be a no-op.
	assert.NotPanics(t, func() {
		reg.Deliver("corr-1", "late-response")
	})
}

// ── Deregister ────────────────────────────────────────────────────────────────

func TestRegistry_Deregister_RemovesEntry(t *testing.T) {
	reg := correlation.NewRegistry[int]()
	reg.Register("corr-1")
	assert.Equal(t, 1, reg.Len())
	reg.Deregister("corr-1")
	assert.Equal(t, 0, reg.Len())
}

func TestRegistry_Deregister_UnknownID_IsNoOp(t *testing.T) {
	reg := correlation.NewRegistry[int]()
	assert.NotPanics(t, func() {
		reg.Deregister("nonexistent")
	})
}

// ── No cross-routing ─────────────────────────────────────────────────────────

// TestRegistry_NoCrossRouting verifies that each Deliver call routes only to the
// correct channel and never to another registered correlation ID.
func TestRegistry_NoCrossRouting(t *testing.T) {
	const n = 10
	reg := correlation.NewRegistry[int]()

	channels := make([]chan int, n)
	for i := range n {
		id := string(rune('a' + i)) // "a".."j"
		channels[i] = reg.Register(id)
	}

	// Deliver a unique value to each channel via its own correlation ID.
	for i := range n {
		id := string(rune('a' + i))
		reg.Deliver(id, i*100)
	}

	// Each channel must receive exactly its own value.
	for i := range n {
		select {
		case v := <-channels[i]:
			assert.Equal(t, i*100, v, "channel %d received wrong value", i)
		case <-time.After(100 * time.Millisecond):
			t.Fatalf("channel %d did not receive a value", i)
		}
	}
}

// ── Concurrent access ─────────────────────────────────────────────────────────

// TestRegistry_ConcurrentRegisterDeliver verifies the registry is safe under
// concurrent register/deliver/deregister from many goroutines.
func TestRegistry_ConcurrentRegisterDeliver(t *testing.T) {
	const goroutines = 50
	reg := correlation.NewRegistry[int]()

	var wg sync.WaitGroup
	var misses atomic.Int32

	for i := range goroutines {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			id := string(rune(0x4e00 + idx)) // unique rune per goroutine
			ch := reg.Register(id)
			reg.Deliver(id, idx)

			select {
			case v := <-ch:
				if v != idx {
					misses.Add(1)
				}
			case <-time.After(500 * time.Millisecond):
				misses.Add(1)
			}
			reg.Deregister(id)
		}(i)
	}

	wg.Wait()
	assert.Equal(t, int32(0), misses.Load(),
		"all goroutines must receive their own value without cross-routing")
}

// TestRegistry_Len_ReturnsActiveCount verifies Len tracks registrations correctly.
func TestRegistry_Len_ReturnsActiveCount(t *testing.T) {
	reg := correlation.NewRegistry[string]()
	require.Equal(t, 0, reg.Len())

	reg.Register("a")
	reg.Register("b")
	assert.Equal(t, 2, reg.Len())

	reg.Deregister("a")
	assert.Equal(t, 1, reg.Len())

	reg.Deregister("b")
	assert.Equal(t, 0, reg.Len())
}

// TestRegistry_Deliver_DuplicateDelivery_DropsSecond ensures a second Deliver to
// the same correlID after the first does not block or panic (buffered channel = 1).
func TestRegistry_Deliver_DuplicateDelivery_NoBlock(t *testing.T) {
	reg := correlation.NewRegistry[string]()
	_ = reg.Register("corr-dup")

	assert.NotPanics(t, func() {
		reg.Deliver("corr-dup", "first")
		reg.Deliver("corr-dup", "second") // must be dropped, not block
	})
}

// TestRegistry_Timeout_CallerReceivesNothing verifies that a registered channel
// for which no Deliver is called remains empty (simulating a timeout scenario).
func TestRegistry_Timeout_ChannelRemainsEmpty(t *testing.T) {
	reg := correlation.NewRegistry[string]()
	ch := reg.Register("corr-timeout")
	defer reg.Deregister("corr-timeout")

	// No Deliver is called; after a short wait the channel must be empty.
	select {
	case v := <-ch:
		t.Fatalf("expected empty channel but received %q", v)
	case <-time.After(50 * time.Millisecond):
		// Expected: no message arrived.
	}
}
