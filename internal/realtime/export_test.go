package realtime

import "time"

// SetHeartbeatCadenceForTest overrides the heartbeat cadence and returns a
// function restoring the previous values. Test-only.
func SetHeartbeatCadenceForTest(interval, timeout time.Duration) (restore func()) {
	prevInterval, prevTimeout := heartbeatInterval, heartbeatTimeout
	heartbeatInterval, heartbeatTimeout = interval, timeout
	return func() {
		heartbeatInterval, heartbeatTimeout = prevInterval, prevTimeout
	}
}
