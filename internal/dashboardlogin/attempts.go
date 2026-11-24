package dashboardlogin

import (
	"net/netip"
	"sync"
	"time"
)

const (
	// maxFailedAttempts sign-ins from one client within attemptWindow, none
	// of them successful, lock that client out until the window ends.
	maxFailedAttempts = 10
	attemptWindow     = 15 * time.Minute
	// maxTrackedClients bounds the memory an attacker rotating addresses can
	// claim. Past it, clients not yet tracked are refused until entries expire.
	maxTrackedClients = 10000
)

// attemptLimiter slows password guessing by counting sign-in attempts per
// client address until one succeeds. It is per process: replicas count
// separately, which only multiplies an attacker's budget by the replica count.
type attemptLimiter struct {
	mu      sync.Mutex
	clients map[string]*attemptRecord
}

type attemptRecord struct {
	attempts int
	expires  time.Time
}

func newAttemptLimiter() *attemptLimiter {
	return &attemptLimiter{clients: make(map[string]*attemptRecord)}
}

// admit counts a sign-in attempt and returns zero, or returns how long the
// client must wait when it has none left. The attempt is counted on the way
// in, not once its password proves wrong, so requests a client sends at the
// same time cannot all be checked before any of them counts.
func (l *attemptLimiter) admit(clientIP string, now time.Time) time.Duration {
	key := clientKey(clientIP)
	l.mu.Lock()
	defer l.mu.Unlock()
	if record, tracked := l.clients[key]; tracked && now.Before(record.expires) {
		if record.attempts >= maxFailedAttempts {
			return record.expires.Sub(now)
		}
		record.attempts++
		return 0
	}
	// Untracked, or tracked by a record that expired and is replaced below.
	if _, expired := l.clients[key]; !expired && len(l.clients) >= maxTrackedClients && !l.sweep(now) {
		return attemptWindow
	}
	l.clients[key] = &attemptRecord{attempts: 1, expires: now.Add(attemptWindow)}
	return 0
}

// release gives back the attempt admit counted at admitted, for a sign-in
// turned away before its password was checked. A record begun after that
// moment never counted the attempt, so it is left alone.
func (l *attemptLimiter) release(clientIP string, admitted time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	record, tracked := l.clients[clientKey(clientIP)]
	if !tracked || record.attempts == 0 || record.expires.Add(-attemptWindow).After(admitted) {
		return
	}
	record.attempts--
}

// reset forgets a client's attempts after it signed in.
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
