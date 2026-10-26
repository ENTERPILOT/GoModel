package ratelimit

import "time"

// WindowSnapshot is one persisted request/token sliding window.
// Partition is empty for a shared rule and the child path for a per-child
// template (Rule.EffectiveSubject). Writer is the random ID of the process
// that wrote the row, so replicas sharing a store keep separate rows instead
// of overwriting each other's; rows imported from before writer IDs existed
// carry an empty writer.
type WindowSnapshot struct {
	Scope               string `bson:"scope"`
	Subject             string `bson:"subject"`
	Partition           string `bson:"partition"`
	PeriodSeconds       int64  `bson:"period_seconds"`
	Writer              string `bson:"writer"`
	RequestsWindowStart int64  `bson:"requests_window_start"`
	RequestsCurrent     int64  `bson:"requests_current"`
	RequestsPrevious    int64  `bson:"requests_previous"`
	TokensWindowStart   int64  `bson:"tokens_window_start"`
	TokensCurrent       int64  `bson:"tokens_current"`
	TokensPrevious      int64  `bson:"tokens_previous"`
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

// restore loads persisted windows into the limiter. A key can have one row
// per writer: every replica sharing the store writes its own, and a restarted
// process comes back under a new writer ID next to the rows of its previous
// run. Rows for the same key are merged per counter (requests and tokens
// separately):
//
//  1. Each row is rolled forward to the window containing now, exactly as
//     admission would roll it (windowCounter.advance): a row one period behind
//     shifts current into previous, an older one drops to zero.
//  2. The merged counter takes the maximum current and the maximum previous
//     across those normalized rows.
//
// The maximum, not the sum, is deliberate. A single instance restarting gets
// its own counts back: its last run started from the run before it, so its
// rows dominate every older one field by field. With N replicas each serving
// about 1/N of the traffic, the largest peer row approximates this replica's
// own share, where a sum would charge it for all N.
func (l *limiter) restore(snapshots []WindowSnapshot, rules []Rule, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()

	byDef := make(map[ruleKey]Rule, len(rules))
	for _, rule := range rules {
		byDef[definitionKey(rule.Scope, rule.Subject, rule.PeriodSeconds)] = rule
	}

	nowUnix := now.Unix()
	requests := make(map[ruleKey]*windowCounter)
	tokens := make(map[ruleKey]*windowCounter)
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
		mergeCounter(requests, key, windowCounter{
			windowStart: snap.RequestsWindowStart,
			current:     snap.RequestsCurrent,
			previous:    snap.RequestsPrevious,
		}, now)
		mergeCounter(tokens, key, windowCounter{
			windowStart: snap.TokensWindowStart,
			current:     snap.TokensCurrent,
			previous:    snap.TokensPrevious,
		}, now)
	}
	for key, counter := range requests {
		l.requests[key] = counter
		l.trackCounterExpiry(requestCounter, key, counter)
	}
	for key, counter := range tokens {
		l.tokens[key] = counter
		l.trackCounterExpiry(tokenCounter, key, counter)
	}
}

// mergeCounter folds one persisted row into the merged counter for key, as
// described on restore. A row that holds nothing at now leaves no counter.
func mergeCounter(merged map[ruleKey]*windowCounter, key ruleKey, row windowCounter, now time.Time) {
	if row.current == 0 && row.previous == 0 {
		return
	}
	row.advance(now, key.periodSeconds)
	if row.current == 0 && row.previous == 0 {
		return
	}
	existing := merged[key]
	if existing == nil {
		merged[key] = &row
		return
	}
	existing.current = max(existing.current, row.current)
	existing.previous = max(existing.previous, row.previous)
}
