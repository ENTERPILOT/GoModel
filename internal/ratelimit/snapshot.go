package ratelimit

import "time"

// WindowSnapshot is one persisted request/token sliding window.
// Partition is empty for a shared rule and the child path for a per-child
// template (Rule.EffectiveSubject).
type WindowSnapshot struct {
	Scope               string `bson:"scope"`
	Subject             string `bson:"subject"`
	Partition           string `bson:"partition"`
	PeriodSeconds       int64  `bson:"period_seconds"`
	RequestsWindowStart int64  `bson:"requests_window_start"`
	RequestsCurrent     int64  `bson:"requests_current"`
	RequestsPrevious    int64  `bson:"requests_previous"`
	TokensWindowStart   int64  `bson:"tokens_window_start"`
	TokensCurrent       int64  `bson:"tokens_current"`
	TokensPrevious      int64  `bson:"tokens_previous"`
	UpdatedAt           int64  `bson:"updated_at,omitempty"`
}

func definitionKey(scope RuleScope, subject string, periodSeconds int64) ruleKey {
	return ruleKey{scope: scope, subject: subject, periodSeconds: periodSeconds}
}

func (l *limiter) snapshot(rules []Rule) []WindowSnapshot {
	l.mu.Lock()
	defer l.mu.Unlock()

	perChild := make(map[ruleKey]bool, len(rules))
	for _, rule := range rules {
		if rule.PeriodSeconds <= 0 {
			continue
		}
		perChild[definitionKey(rule.Scope, rule.Subject, rule.PeriodSeconds)] = rule.PerChild
	}

	byKey := make(map[ruleKey]*WindowSnapshot)
	add := func(key ruleKey, kind counterKind, counter windowCounter) {
		wantChild, ok := perChild[definitionKey(key.scope, key.subject, key.periodSeconds)]
		if !ok {
			return
		}
		if wantChild != (key.partition != "") {
			return
		}
		snap := byKey[key]
		if snap == nil {
			snap = &WindowSnapshot{
				Scope:         string(key.scope),
				Subject:       key.subject,
				Partition:     key.partition,
				PeriodSeconds: key.periodSeconds,
			}
			byKey[key] = snap
		}
		switch kind {
		case requestCounter:
			snap.RequestsWindowStart = counter.windowStart
			snap.RequestsCurrent = counter.current
			snap.RequestsPrevious = counter.previous
		case tokenCounter:
			snap.TokensWindowStart = counter.windowStart
			snap.TokensCurrent = counter.current
			snap.TokensPrevious = counter.previous
		}
	}

	for key, counter := range l.requests {
		add(key, requestCounter, *counter)
	}
	for key, counter := range l.tokens {
		add(key, tokenCounter, *counter)
	}

	out := make([]WindowSnapshot, 0, len(byKey))
	for _, snap := range byKey {
		out = append(out, *snap)
	}
	return out
}

func (l *limiter) restore(snapshots []WindowSnapshot, rules []Rule, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()

	byDef := make(map[ruleKey]Rule, len(rules))
	for _, rule := range rules {
		byDef[definitionKey(rule.Scope, rule.Subject, rule.PeriodSeconds)] = rule
	}

	nowUnix := now.Unix()
	for _, snap := range snapshots {
		if snap.PeriodSeconds <= 0 {
			continue
		}
		rule, ok := byDef[definitionKey(RuleScope(snap.Scope), snap.Subject, snap.PeriodSeconds)]
		if !ok {
			continue
		}
		if rule.PerChild != (snap.Partition != "") {
			continue
		}
		latest := max(snap.RequestsWindowStart, snap.TokensWindowStart)
		if latest > 0 && latest+2*snap.PeriodSeconds < nowUnix {
			continue
		}

		key := ruleKey{
			scope:         RuleScope(snap.Scope),
			subject:       snap.Subject,
			partition:     snap.Partition,
			periodSeconds: snap.PeriodSeconds,
		}
		if snap.RequestsWindowStart != 0 || snap.RequestsCurrent != 0 || snap.RequestsPrevious != 0 {
			counter := &windowCounter{
				windowStart: snap.RequestsWindowStart,
				current:     snap.RequestsCurrent,
				previous:    snap.RequestsPrevious,
			}
			l.requests[key] = counter
			l.trackCounterExpiry(requestCounter, key, counter)
		}
		if snap.TokensWindowStart != 0 || snap.TokensCurrent != 0 || snap.TokensPrevious != 0 {
			counter := &windowCounter{
				windowStart: snap.TokensWindowStart,
				current:     snap.TokensCurrent,
				previous:    snap.TokensPrevious,
			}
			l.tokens[key] = counter
			l.trackCounterExpiry(tokenCounter, key, counter)
		}
	}
}
