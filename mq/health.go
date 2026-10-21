package mq

import "fmt"

// healthProber is a function registered by Sender, Listener, and other managed
// components so that [Client.Health] can aggregate their liveness without
// importing concrete types.
type healthProber func() []ConnectionStatus

// Health returns the aggregate health of all IBM MQ connections managed by this
// Client. It collects [ConnectionStatus] entries from every registered component
// (Sender pool, Listener consumers, RequestReplyClients) and sets
// [HealthStatus.Healthy] to true only when every entry reports Alive == true.
//
// Idle senders and request-reply clients that have not attempted a connection
// yet contribute no entries — the library is not unhealthy simply because a
// component has not been used. A Listener, by contrast, is expected to be
// consuming: its consumers report [StateUnknown] until Start is called and
// [StateStopped] once they exit, both of which are not alive.
//
// Health reflects the last known state of each connection; it does not perform
// an active network probe.
func (c *Client) Health() HealthStatus {
	c.mu.Lock()
	closed := c.closed
	probers := make([]healthProber, len(c.healthProbers))
	copy(probers, c.healthProbers)
	c.mu.Unlock()

	var details []ConnectionStatus
	for _, probe := range probers {
		details = append(details, probe()...)
	}

	if closed {
		details = append([]ConnectionStatus{{
			QueueManager: c.cfg.QueueManagerName,
			Alive:        false,
			Reason:       "client is closed",
			Component:    "client",
		}}, details...)
	}

	healthy := true
	for _, d := range details {
		if !d.Alive {
			healthy = false
			break
		}
	}

	return HealthStatus{
		Healthy: healthy,
		Details: details,
	}
}

// registerHealthProber adds a prober function to the Client.
// Called by NewListener, NewSender, and NewRequestReplyClient to hook into Health().
func (c *Client) registerHealthProber(probe healthProber) {
	c.mu.Lock()
	c.healthProbers = append(c.healthProbers, probe)
	c.mu.Unlock()
}

// senderHealthProber returns a health probe function for a Sender.
//
// It reports the Sender's tracked connection state, which is updated by every
// dial, reconnect, and successful send. This ensures a Sender that never
// connected (or whose last connection was discarded after exhausted retries)
// is reported as unhealthy instead of silently disappearing from the results.
func senderHealthProber(s *Sender) healthProber {
	return func() []ConnectionStatus {
		s.mu.Lock()
		created := s.created
		closed := s.closed
		state := s.state
		lastErr := s.lastErr
		s.mu.Unlock()

		qm := s.client.cfg.QueueManagerName
		component := fmt.Sprintf("sender %q", s.queue)
		dead := func(reason string) []ConnectionStatus {
			return []ConnectionStatus{{
				QueueManager: qm,
				Alive:        false,
				Reason:       reason,
				Component:    component,
			}}
		}

		if closed {
			return dead("sender is closed")
		}

		switch state {
		case StateReconnecting:
			return dead("connection state: reconnecting")
		case StateFailed:
			reason := "connection state: failed"
			if lastErr != nil {
				reason += ": " + lastErr.Error()
			}
			return dead(reason)
		}

		// If no connections have been created yet the sender is idle — report healthy,
		// unless a previous connection attempt failed and left an error behind.
		if created == 0 {
			if lastErr != nil {
				return dead("no live connections: " + lastErr.Error())
			}
			return nil
		}

		// Synchronize with Sender.close draining the pool during shutdown.
		s.poolMu.Lock()
		defer s.poolMu.Unlock()

		select {
		case conn := <-s.pool:
			connState := conn.State()
			s.pool <- conn // return immediately
			if connState == StateConnected {
				return []ConnectionStatus{{QueueManager: qm, Alive: true, Component: component}}
			}
			return dead("connection state: " + stateLabel(connState))
		default:
			// All connections are checked out and actively in use. Only claim
			// liveness when the sender has not observed a failure.
			if state == StateConnected || state == StateUnknown {
				return []ConnectionStatus{{QueueManager: qm, Alive: true, Component: component}}
			}
			return dead("connection state: " + stateLabel(state))
		}
	}
}

// listenerHealthProber returns a health probe function for a Listener. It emits
// one entry per consumer so partial failures are visible even when other
// consumers remain connected.
func listenerHealthProber(l *Listener) healthProber {
	return func() []ConnectionStatus {
		l.mu.Lock()
		closed := l.closed
		l.mu.Unlock()

		qm := l.client.cfg.QueueManagerName
		details := make([]ConnectionStatus, 0, len(l.states))
		for i := range l.states {
			state := ConnectionState(l.states[i].Load())
			alive := state == StateConnected && !closed
			reason := ""
			switch {
			case closed:
				reason = "listener is closed"
			case !alive:
				reason = "connection state: " + stateLabel(state)
			}
			details = append(details, ConnectionStatus{
				QueueManager: qm,
				Alive:        alive,
				Reason:       reason,
				Component:    fmt.Sprintf("listener %q consumer %d", l.queue, i),
			})
		}
		return details
	}
}

// requestReplyHealthProber returns a health probe for a RequestReplyClient.
// It contributes no entry until the client has been used.
func requestReplyHealthProber(r *RequestReplyClient) healthProber {
	return func() []ConnectionStatus {
		r.mu.Lock()
		closed := r.closed
		state := r.state
		lastErr := r.lastErr
		r.mu.Unlock()

		qm := r.client.cfg.QueueManagerName
		component := fmt.Sprintf("request-reply %q", r.requestQueue)

		if closed {
			return []ConnectionStatus{{
				QueueManager: qm,
				Alive:        false,
				Reason:       "request-reply client is closed",
				Component:    component,
			}}
		}
		if state == StateUnknown && lastErr == nil {
			return nil // never used
		}
		if state == StateFailed {
			reason := "connection state: failed"
			if lastErr != nil {
				reason += ": " + lastErr.Error()
			}
			return []ConnectionStatus{{
				QueueManager: qm,
				Alive:        false,
				Reason:       reason,
				Component:    component,
			}}
		}
		return []ConnectionStatus{{
			QueueManager: qm,
			Alive:        true,
			Component:    component,
		}}
	}
}

// stateLabel returns a human-readable label for a ConnectionState.
func stateLabel(s ConnectionState) string {
	switch s {
	case StateConnected:
		return "connected"
	case StateReconnecting:
		return "reconnecting"
	case StateFailed:
		return "failed"
	case StateStopped:
		return "stopped"
	case StateUnknown:
		return "unknown"
	default:
		return "unknown"
	}
}
