package mq

import "fmt"

// healthProber is a function registered by Sender, Listener, and other managed
// components so that [Client.Health] can aggregate their liveness without
// importing concrete types.
type healthProber func() []ConnectionStatus

// Health returns the aggregate health of all IBM MQ connections managed by this
// Client. It collects [ConnectionStatus] entries from every registered component
// (Sender pool, Listener consumers) and sets [HealthStatus.Healthy] to true only
// when every entry reports Alive == true.
//
// If no connections have been established yet (e.g., Sender pool is empty before
// the first send), Health returns Healthy == true with an empty Details slice —
// the library is not unhealthy simply because it has not been used.
func (c *Client) Health() HealthStatus {
	c.mu.Lock()
	probers := make([]healthProber, len(c.healthProbers))
	copy(probers, c.healthProbers)
	c.mu.Unlock()

	var details []ConnectionStatus
	for _, probe := range probers {
		details = append(details, probe()...)
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
// Called by NewListener and NewSender to hook into Health().
func (c *Client) registerHealthProber(probe healthProber) {
	c.mu.Lock()
	c.healthProbers = append(c.healthProbers, probe)
	c.mu.Unlock()
}

// senderHealthProber returns a health probe function for a Sender.
// It performs a non-blocking peek at the connection pool: if an idle connection
// is present its ConnectionState is checked; if the pool is empty and connections
// have been created, the sender is considered alive (all connections are in use).
func senderHealthProber(s *Sender) healthProber {
	return func() []ConnectionStatus {
		s.mu.Lock()
		created := s.created
		closed := s.closed
		s.mu.Unlock()

		qm := s.client.cfg.QueueManagerName

		if closed {
			return []ConnectionStatus{{
				QueueManager: qm,
				Alive:        false,
				Reason:       "sender is closed",
			}}
		}

		// If no connections have been created yet the sender is idle — report healthy.
		if created == 0 {
			return nil
		}

		// Non-blocking: borrow one idle connection from the pool to check its state.
		select {
		case conn := <-s.pool:
			state := conn.State()
			s.pool <- conn // return immediately
			alive := state == StateConnected
			reason := ""
			if !alive {
				reason = fmt.Sprintf("connection state: %s", stateLabel(state))
			}
			return []ConnectionStatus{{QueueManager: qm, Alive: alive, Reason: reason}}
		default:
			// All connections are checked out — they are actively in use, treat as alive.
			return []ConnectionStatus{{QueueManager: qm, Alive: true}}
		}
	}
}

// listenerHealthProber returns a health probe function for a Listener that reads
// the last connection state tracked by runConsumer.
func listenerHealthProber(l *Listener, stateFn func() ConnectionState) healthProber {
	return func() []ConnectionStatus {
		state := stateFn()
		alive := state == StateConnected
		reason := ""
		if !alive {
			reason = fmt.Sprintf("connection state: %s", stateLabel(state))
		}
		return []ConnectionStatus{{
			QueueManager: l.client.cfg.QueueManagerName,
			Alive:        alive,
			Reason:       reason,
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
	default:
		return "unknown"
	}
}
