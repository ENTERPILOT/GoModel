package ratelimit

import (
	"sync"
	"time"
)

// windowCounter tracks a sliding-window count as two adjacent fixed windows.
// The estimated usage weights the previous window by its remaining overlap,
// which prevents the 2x burst a naive fixed window allows at boundaries.
type windowCounter struct {
	windowStart int64 // unix seconds, aligned to the period
	current     int64
	previous    int64
}

// advance rolls the counter forward so windowStart covers now.
func (w *windowCounter) advance(now time.Time, periodSeconds int64) {
	start := now.Unix() - now.Unix()%periodSeconds
	switch w.windowStart {
	case start:
	case start - periodSeconds:
		w.previous = w.current
		w.current = 0
		w.windowStart = start
	default:
		w.previous = 0
		w.current = 0
		w.windowStart = start
	}
}

// estimate returns the weighted sliding-window usage at now. advance must be
// called first for the same now.
func (w *windowCounter) estimate(now time.Time, periodSeconds int64) int64 {
	elapsed := min(max(now.Unix()-w.windowStart, 0), periodSeconds)
	weight := float64(periodSeconds-elapsed) / float64(periodSeconds)
	return int64(float64(w.previous)*weight) + w.current
}

// resetAfter returns the time until the current window rolls over.
func (w *windowCounter) resetAfter(now time.Time, periodSeconds int64) time.Duration {
	remaining := max(w.windowStart+periodSeconds-now.Unix(), 1)
	return time.Duration(remaining) * time.Second
}

// retryAfter returns the earliest wait after which the sliding-window
// estimate drops below limit. The next bucket boundary alone can
// under-promise: right after a rollover the previous window still weighs in,
// and a heavily overshot current count keeps blocking well past it. The
// estimate is non-increasing over time (absent new arrivals) and reaches 0
// within two periods, so a binary search against the real estimate function
// finds the exact second without duplicating its truncation math.
func (w *windowCounter) retryAfter(now time.Time, periodSeconds, limit int64) time.Duration {
	lo, hi := int64(1), 2*periodSeconds
	for lo < hi {
		mid := (lo + hi) / 2
		probe := *w
		at := time.Unix(now.Unix()+mid, 0)
		probe.advance(at, periodSeconds)
		if probe.estimate(at, periodSeconds) < limit {
			hi = mid
		} else {
			lo = mid + 1
		}
	}
	return time.Duration(lo) * time.Second
}

type ruleKey struct {
	scope         RuleScope
	subject       string
	periodSeconds int64
}

func keyForRule(rule Rule) ruleKey {
	return ruleKey{scope: rule.Scope, subject: rule.Subject, periodSeconds: rule.PeriodSeconds}
}

// limiter holds all live counters. Counters exist per rule (one shared
// counter for the rule's whole subject), so cardinality equals the number of
// configured rules and a single mutex is sufficient.
type limiter struct {
	mu       sync.Mutex
	requests map[ruleKey]*windowCounter
	tokens   map[ruleKey]*windowCounter
	inFlight map[ruleKey]int64
}

func newLimiter() *limiter {
	return &limiter{
		requests: make(map[ruleKey]*windowCounter),
		tokens:   make(map[ruleKey]*windowCounter),
		inFlight: make(map[ruleKey]int64),
	}
}

func (l *limiter) counter(counters map[ruleKey]*windowCounter, key ruleKey) *windowCounter {
	counter, ok := counters[key]
	if !ok {
		counter = &windowCounter{}
		counters[key] = counter
	}
	return counter
}

// admit checks every matching rule and, only when all pass, commits the
// request counters and concurrency slots. It returns the header snapshot for
// the most-constrained matching limits. On breach it reports the exceeded
// rule with the longest recovery, so Retry-After stays honest when several
// windows (e.g. minute and day) are exhausted at once.
func (l *limiter) admit(rules []Rule, now time.Time) (HeaderSnapshot, []ruleKey, *ExceededError) {
	l.mu.Lock()
	defer l.mu.Unlock()

	var exceeded *ExceededError
	for _, rule := range rules {
		if candidate := l.breach(rule, now); candidate != nil {
			if exceeded == nil || candidate.RetryAfter > exceeded.RetryAfter {
				exceeded = candidate
			}
		}
	}
	if exceeded != nil {
		return HeaderSnapshot{}, nil, exceeded
	}

	var headers HeaderSnapshot
	var held []ruleKey
	for _, rule := range rules {
		key := keyForRule(rule)
		if rule.PeriodSeconds == PeriodConcurrent {
			l.inFlight[key]++
			held = append(held, key)
			continue
		}
		if rule.MaxRequests != nil {
			counter := l.requests[key]
			counter.current++
			remaining := max(*rule.MaxRequests-counter.estimate(now, rule.PeriodSeconds), 0)
			if !headers.HasRequests || remaining < headers.RequestRemaining {
				headers.HasRequests = true
				headers.RequestLimit = *rule.MaxRequests
				headers.RequestRemaining = remaining
				headers.RequestResetAfter = counter.resetAfter(now, rule.PeriodSeconds)
			}
		}
		if rule.MaxTokens != nil {
			counter := l.tokens[key]
			remaining := max(*rule.MaxTokens-counter.estimate(now, rule.PeriodSeconds), 0)
			if !headers.HasTokens || remaining < headers.TokenRemaining {
				headers.HasTokens = true
				headers.TokenLimit = *rule.MaxTokens
				headers.TokenRemaining = remaining
				headers.TokenResetAfter = counter.resetAfter(now, rule.PeriodSeconds)
			}
		}
	}
	return headers, held, nil
}

// breach reports whether one rule currently rejects one more request, without
// consuming anything. The caller must hold l.mu.
func (l *limiter) breach(rule Rule, now time.Time) *ExceededError {
	key := keyForRule(rule)
	if rule.PeriodSeconds == PeriodConcurrent {
		if l.inFlight[key] >= *rule.MaxRequests {
			return &ExceededError{
				Rule:       rule,
				Scope:      ScopeConcurrency,
				Observed:   l.inFlight[key],
				Limit:      *rule.MaxRequests,
				RetryAfter: time.Second,
			}
		}
		return nil
	}
	if rule.MaxRequests != nil {
		counter := l.counter(l.requests, key)
		counter.advance(now, rule.PeriodSeconds)
		if used := counter.estimate(now, rule.PeriodSeconds); used >= *rule.MaxRequests {
			return &ExceededError{
				Rule:       rule,
				Scope:      ScopeRequests,
				Observed:   used,
				Limit:      *rule.MaxRequests,
				RetryAfter: counter.retryAfter(now, rule.PeriodSeconds, *rule.MaxRequests),
			}
		}
	}
	if rule.MaxTokens != nil {
		counter := l.counter(l.tokens, key)
		counter.advance(now, rule.PeriodSeconds)
		if used := counter.estimate(now, rule.PeriodSeconds); used >= *rule.MaxTokens {
			return &ExceededError{
				Rule:       rule,
				Scope:      ScopeTokens,
				Observed:   used,
				Limit:      *rule.MaxTokens,
				RetryAfter: counter.retryAfter(now, rule.PeriodSeconds, *rule.MaxTokens),
			}
		}
	}
	return nil
}

// available reports whether every rule would currently admit one more
// request. It is a read-only probe for routing decisions (load balancing and
// failover skip saturated targets); admission stays the authoritative check.
func (l *limiter) available(rules []Rule, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, rule := range rules {
		if l.breach(rule, now) != nil {
			return false
		}
	}
	return true
}

// release returns previously held concurrency slots.
func (l *limiter) release(held []ruleKey) {
	if len(held) == 0 {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, key := range held {
		if l.inFlight[key] > 0 {
			l.inFlight[key]--
		}
	}
}

// recordTokens adds consumed tokens to every matching token window.
func (l *limiter) recordTokens(rules []Rule, tokens int64, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, rule := range rules {
		if rule.PeriodSeconds == PeriodConcurrent || rule.MaxTokens == nil {
			continue
		}
		counter := l.counter(l.tokens, keyForRule(rule))
		counter.advance(now, rule.PeriodSeconds)
		counter.current += tokens
	}
}

// status reports the live counter state for one rule.
func (l *limiter) status(rule Rule, now time.Time) Status {
	l.mu.Lock()
	defer l.mu.Unlock()

	key := keyForRule(rule)
	status := Status{Rule: rule}
	if rule.PeriodSeconds == PeriodConcurrent {
		status.InFlight = l.inFlight[key]
		if rule.MaxRequests != nil {
			remaining := max(*rule.MaxRequests-status.InFlight, 0)
			status.RequestsRemaining = &remaining
		}
		return status
	}

	start := now.Unix() - now.Unix()%rule.PeriodSeconds
	status.WindowStart = time.Unix(start, 0).UTC()
	status.WindowEnd = time.Unix(start+rule.PeriodSeconds, 0).UTC()
	if rule.MaxRequests != nil {
		counter := l.counter(l.requests, key)
		counter.advance(now, rule.PeriodSeconds)
		status.RequestsUsed = counter.estimate(now, rule.PeriodSeconds)
		remaining := max(*rule.MaxRequests-status.RequestsUsed, 0)
		status.RequestsRemaining = &remaining
	}
	if rule.MaxTokens != nil {
		counter := l.counter(l.tokens, key)
		counter.advance(now, rule.PeriodSeconds)
		status.TokensUsed = counter.estimate(now, rule.PeriodSeconds)
		remaining := max(*rule.MaxTokens-status.TokensUsed, 0)
		status.TokensRemaining = &remaining
	}
	return status
}

// reset clears the window counters for one rule. In-flight gauges are left
// alone: they reflect requests that genuinely still hold slots.
func (l *limiter) reset(key ruleKey) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.requests, key)
	delete(l.tokens, key)
}

// resetAll clears every window counter.
func (l *limiter) resetAll() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.requests = make(map[ruleKey]*windowCounter)
	l.tokens = make(map[ruleKey]*windowCounter)
}
