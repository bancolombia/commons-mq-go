package mq

import ibmmq "github.com/ibm-messaging/mq-golang/v5/ibmmq"

// applyQueueOpenOptions merges QueueOptions into the MQOO (queue open options) flags
// and returns the updated flags. Called by consumer goroutines before opening a queue
// for input.
//
// Notes on Commons MQ-style properties in the native MQ API:
//   - TargetClient, MQMDReadEnabled, MQMDWriteEnabled, and PutAsyncAllowed have no
//     direct equivalent MQIA selectors accessible from the IBM MQ Go client; they are
//     Commons MQ layer concepts. They are intentionally left as no-ops here and callers can
//     rely on the IBM MQ default MQMD behaviour.
//   - ReadAheadAllowed maps to MQOO_READ_AHEAD in the native open-options bitmask.
func applyQueueOpenOptions(opts QueueOptions, oo int32) int32 {
	if opts.ReadAheadAllowed {
		oo |= ibmmq.MQOO_READ_AHEAD
	}
	return oo
}
