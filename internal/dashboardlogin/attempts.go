package dashboardlogin

import (
	"net/netip"
	"sync"
	"time"
)

const (
	// maxFailedAttempts failed sign-ins from one client within attemptWindow
	// lock that client out until the window ends.
	maxFailedAttempts = 10
	attemptWindow     = 15 * time.Minute
	// maxTrackedClients bounds the memory an attacker rotating addresses can
	// claim. Past it, clients not yet tracked are refused until entries expire.
	maxTrackedClients = 10000
)

// attemptLimiter slows password guessing by counting failed sign-ins per
// client address. It is per process: replicas count separately, which only
// multiplies an attacker's budget by the replica count.
type attemptLimiter struct {
	mu      sync.Mutex
	clients map[string]*attemptRecord
}

type attemptRecord struct {
	failures int
	expires  time.Time
}

func newAttemptLimiter() *attemptLimiter {
	return &attemptLimiter{clients: make(map[string]*attemptRecord)}
}

// retryAfter returns how long the client must wait before signing in again,
// or zero when it may try now.
func (l *attemptLimiter) retryAfter(clientIP string, now time.Time) time.Duration {
	key := clientKey(clientIP)
	l.mu.Lock()
	defer l.mu.Unlock()
	record, tracked := l.clients[key]
	if tracked && !now.Before(record.expires) {
		delete(l.clients, key)
		tracked = false
	}
	switch {
	case tracked && record.failures >= maxFailedAttempts:
		return record.expires.Sub(now)
	case !tracked && len(l.clients) >= maxTrackedClients && !l.sweep(now):
		return attemptWindow
	}
	return 0
}

// fail records a failed sign-in.
func (l *attemptLimiter) fail(clientIP string, now time.Time) {
	key := clientKey(clientIP)
	l.mu.Lock()
	defer l.mu.Unlock()
	if record, tracked := l.clients[key]; tracked && now.Before(record.expires) {
		record.failures++
		return
	}
	l.clients[key] = &attemptRecord{failures: 1, expires: now.Add(attemptWindow)}
}

// reset forgets a client's failures after it signed in.
func (l *attemptLimiter) reset(clientIP string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.clients, clientKey(clientIP))
}

// sweep drops expired records and reports whether that made room.
func (l *attemptLimiter) sweep(now time.Time) bool {
	for key, record := range l.clients {
		if !now.Before(record.expires) {
			delete(l.clients, key)
		}
	}
	return len(l.clients) < maxTrackedClients
}

// clientKey groups an IPv6 address by its /64, the block one subscriber
// usually holds, so rotating through it does not reset the count.
func clientKey(clientIP string) string {
	addr, err := netip.ParseAddr(clientIP)
	if err != nil {
		return clientIP
	}
	addr = addr.Unmap()
	if addr.Is4() {
		return addr.String()
	}
	prefix, err := addr.Prefix(64)
	if err != nil {
		return addr.String()
	}
	return prefix.String()
}
