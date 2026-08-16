package ratelimit

import (
	"container/heap"
	"runtime"
	"time"
)

const maxExpiryCleanupBatch = 64

type counterKind uint8

const (
	requestCounter counterKind = iota
	tokenCounter
)

type counterExpiryKey struct {
	kind counterKind
	rule ruleKey
}

type counterExpiry struct {
	key         counterExpiryKey
	windowStart int64
	expiresAt   int64
	index       int
}

type counterExpiryQueue []*counterExpiry

func (q counterExpiryQueue) Len() int           { return len(q) }
func (q counterExpiryQueue) Less(i, j int) bool { return q[i].expiresAt < q[j].expiresAt }
func (q counterExpiryQueue) Swap(i, j int) {
	q[i], q[j] = q[j], q[i]
	q[i].index = i
	q[j].index = j
}

func (q *counterExpiryQueue) Push(value any) {
	entry := value.(*counterExpiry)
	entry.index = len(*q)
	*q = append(*q, entry)
}

func (q *counterExpiryQueue) Pop() any {
	old := *q
	last := len(old) - 1
	entry := old[last]
	old[last] = nil
	entry.index = -1
	*q = old[:last]
	return entry
}

// trackCounterExpiry schedules one dynamic child counter for cleanup after
// its sliding-window history can no longer affect enforcement. The deadline
// uses wall time rather than the request timestamp because callers may inject
// historical timestamps for replay and tests. The caller holds l.mu.
func (l *limiter) trackCounterExpiry(kind counterKind, key ruleKey, counter *windowCounter) {
	if l.expiryClosed || key.partition == "" || key.periodSeconds <= 0 || counter == nil || counter.windowStart == 0 {
		return
	}
	if l.expiryByCounter == nil {
		l.expiryByCounter = make(map[counterExpiryKey]*counterExpiry)
		l.expiryWake = make(chan struct{}, 1)
		l.expiryStop = make(chan struct{})
		l.expiryDone = make(chan struct{})
	}
	if !l.expiryStarted {
		l.expiryStarted = true
		go l.runExpiryCleanup()
	}

	expiryKey := counterExpiryKey{kind: kind, rule: key}
	expiresAt := time.Now().Unix() + 2*key.periodSeconds
	if entry := l.expiryByCounter[expiryKey]; entry != nil {
		if entry.windowStart == counter.windowStart {
			return
		}
		entry.windowStart = counter.windowStart
		entry.expiresAt = expiresAt
		heap.Fix(&l.expiries, entry.index)
	} else {
		entry := &counterExpiry{key: expiryKey, windowStart: counter.windowStart, expiresAt: expiresAt}
		l.expiryByCounter[expiryKey] = entry
		heap.Push(&l.expiries, entry)
	}
	l.wakeExpiryCleanup()
}

// removeCounterExpiry forgets one counter removed by an explicit rule reset.
// The caller holds l.mu.
func (l *limiter) removeCounterExpiry(kind counterKind, key ruleKey) {
	entry := l.expiryByCounter[counterExpiryKey{kind: kind, rule: key}]
	if entry == nil {
		return
	}
	delete(l.expiryByCounter, entry.key)
	heap.Remove(&l.expiries, entry.index)
	l.wakeExpiryCleanup()
}

// resetCounterExpiries clears scheduled cleanup after a global reset. The
// caller holds l.mu.
func (l *limiter) resetCounterExpiries() {
	l.expiries = nil
	if l.expiryByCounter != nil {
		clear(l.expiryByCounter)
	}
	l.wakeExpiryCleanup()
}

func (l *limiter) wakeExpiryCleanup() {
	if l.expiryWake == nil {
		return
	}
	select {
	case l.expiryWake <- struct{}{}:
	default:
	}
}

// pruneCounterExpiries removes a bounded number of due counters without a map
// scan. It reports whether another due batch remains. The caller holds l.mu.
func (l *limiter) pruneCounterExpiries(now int64) bool {
	removed := 0
	for len(l.expiries) > 0 && l.expiries[0].expiresAt <= now && removed < maxExpiryCleanupBatch {
		entry := heap.Pop(&l.expiries).(*counterExpiry)
		delete(l.expiryByCounter, entry.key)
		switch entry.key.kind {
		case requestCounter:
			delete(l.requests, entry.key.rule)
		case tokenCounter:
			delete(l.tokens, entry.key.rule)
		}
		removed++
	}
	return len(l.expiries) > 0 && l.expiries[0].expiresAt <= now
}

func (l *limiter) runExpiryCleanup() {
	defer close(l.expiryDone)
	timer := time.NewTimer(time.Hour)
	if !timer.Stop() {
		<-timer.C
	}
	defer timer.Stop()

	for {
		l.mu.Lock()
		now := time.Now().Unix()
		moreDue := l.pruneCounterExpiries(now)
		hasExpiry := len(l.expiries) > 0
		var wait time.Duration
		if hasExpiry {
			wait = time.Duration(max(l.expiries[0].expiresAt-now, 0)) * time.Second
		}
		wake := l.expiryWake
		stop := l.expiryStop
		l.mu.Unlock()

		if moreDue {
			runtime.Gosched()
			continue
		}
		if !hasExpiry {
			select {
			case <-wake:
				continue
			case <-stop:
				return
			}
		}

		resetExpiryTimer(timer, wait)
		select {
		case <-timer.C:
		case <-wake:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
		case <-stop:
			return
		}
	}
}

func resetExpiryTimer(timer *time.Timer, wait time.Duration) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	timer.Reset(wait)
}

func (l *limiter) close() {
	if l == nil {
		return
	}
	l.expiryCloseOnce.Do(func() {
		l.mu.Lock()
		l.expiryClosed = true
		if !l.expiryStarted {
			l.mu.Unlock()
			return
		}
		close(l.expiryStop)
		done := l.expiryDone
		l.mu.Unlock()
		<-done
	})
}
