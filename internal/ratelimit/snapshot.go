package ratelimit

// WindowSnapshot is one writer's persisted counters for one key: its
// request/token sliding windows, or for a concurrency rule its in-flight
// gauge. Partition is empty for a shared rule and the child path for a
// per-child template (Rule.EffectiveSubject). Writer is the random ID of the
// process that wrote the row, so replicas sharing a store keep separate rows
// instead of overwriting each other's; rows imported from before writer IDs
// existed carry an empty writer.
//
// A row holds only what its writer counted itself, never counts it loaded
// from other rows, so the cluster's count for a key is the sum of its rows
// (see peerView.sum). A restarted process writes under a new ID, and its
// earlier run's rows are summed like any other writer's.
type WindowSnapshot struct {
	Scope         string `bson:"scope"`
	Subject       string `bson:"subject"`
	Partition     string `bson:"partition"`
	PeriodSeconds int64  `bson:"period_seconds"`
	Writer        string `bson:"writer"`
	// Epoch is the CounterEpoch of the rule the row was counted under.
	Epoch               string `bson:"epoch"`
	RequestsWindowStart int64  `bson:"requests_window_start"`
	RequestsCurrent     int64  `bson:"requests_current"`
	RequestsPrevious    int64  `bson:"requests_previous"`
	TokensWindowStart   int64  `bson:"tokens_window_start"`
	TokensCurrent       int64  `bson:"tokens_current"`
	TokensPrevious      int64  `bson:"tokens_previous"`
	// InFlight is the writer's in-flight gauge on a concurrency rule
	// (PeriodConcurrent), whose window fields stay zero.
	InFlight int64 `bson:"in_flight"`
	// UpdatedAt is when the writer last saved the row, in unix seconds by
	// its own clock. Stores stamp it on save, whatever the snapshot holds,
	// and report it on load.
	UpdatedAt int64 `bson:"updated_at"`
}

func definitionKey(scope RuleScope, subject string, periodSeconds int64) ruleKey {
	return ruleKey{scope: scope, subject: subject, periodSeconds: periodSeconds}
}

// ruleIndex finds the rule definition a counter key or stored row belongs to.
type ruleIndex map[ruleKey]Rule

func indexRules(rules []Rule) ruleIndex {
	index := make(ruleIndex, len(rules))
	for _, rule := range rules {
		index[definitionKey(rule.Scope, rule.Subject, rule.PeriodSeconds)] = rule
	}
	return index
}

// lookup returns the rule for key, provided the key's partition fits the
// rule's mode: a shared rule owns only the empty partition, a per-child rule
// only child partitions.
func (index ruleIndex) lookup(key ruleKey) (Rule, bool) {
	rule, ok := index[definitionKey(key.scope, key.subject, key.periodSeconds)]
	if !ok || rule.PerChild != (key.partition != "") {
		return Rule{}, false
	}
	return rule, true
}

func newSnapshot(key ruleKey, rule Rule) WindowSnapshot {
	return WindowSnapshot{
		Scope:         string(key.scope),
		Subject:       key.subject,
		Partition:     key.partition,
		PeriodSeconds: key.periodSeconds,
		Epoch:         rule.CounterEpoch,
	}
}

// snapshot returns the rows this writer has to save: one per key whose
// request or token counter changed since the previous snapshot, and one per
// concurrency key whose gauge is nonzero, or dropped to zero since the
// previous snapshot published it. A row of an unchanged key is left as it is:
// it already holds the same counts, and a reader rolls it forward as admission
// rolls the counter. In-flight rows are republished every time, so peers can
// tell a live writer's slots from a crashed one's (see Service.inFlightLiveness).
//
// It clears the change marks; a save that fails hands its rows back through
// unsaved.
func (l *limiter) snapshot(rules []Rule) []WindowSnapshot {
	l.mu.Lock()
	defer l.mu.Unlock()

	index := indexRules(rules)
	var changed []ruleKey
	for _, counters := range []map[ruleKey]*windowCounter{l.requests, l.tokens} {
		for key, counter := range counters {
			if !counter.dirty {
				continue
			}
			counter.dirty = false
			if key.periodSeconds > 0 {
				changed = append(changed, key)
			}
		}
	}

	var out []WindowSnapshot
	seen := make(map[ruleKey]bool, len(changed))
	for _, key := range changed {
		rule, ok := index.lookup(key)
		if !ok || seen[key] {
			continue
		}
		seen[key] = true
		snap := newSnapshot(key, rule)
		if counter := l.requests[key]; counter != nil {
			snap.RequestsWindowStart = counter.windowStart
			snap.RequestsCurrent = counter.current
			snap.RequestsPrevious = counter.previous
		}
		if counter := l.tokens[key]; counter != nil {
			snap.TokensWindowStart = counter.windowStart
			snap.TokensCurrent = counter.current
			snap.TokensPrevious = counter.previous
		}
		out = append(out, snap)
	}

	for key, inFlight := range l.inFlight {
		rule, ok := index.lookup(key)
		if !ok || key.periodSeconds != PeriodConcurrent {
			continue
		}
		snap := newSnapshot(key, rule)
		snap.InFlight = inFlight
		out = append(out, snap)
		l.publishedInFlight[key] = struct{}{}
	}
	for key := range l.publishedInFlight {
		if l.inFlight[key] > 0 {
			continue
		}
		delete(l.publishedInFlight, key)
		if rule, ok := index.lookup(key); ok {
			out = append(out, newSnapshot(key, rule))
		}
	}
	return out
}

// unsaved marks the rows of a failed save for the next snapshot again. A
// counter reset since the snapshot has nothing left to mark.
func (l *limiter) unsaved(snapshots []WindowSnapshot) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, snap := range snapshots {
		key := ruleKey{
			scope:         RuleScope(snap.Scope),
			subject:       snap.Subject,
			partition:     snap.Partition,
			periodSeconds: snap.PeriodSeconds,
		}
		if snap.PeriodSeconds == PeriodConcurrent {
			// A nonzero gauge goes out with every snapshot anyway; a drop
			// to zero has to be retried.
			l.publishedInFlight[key] = struct{}{}
			continue
		}
		if counter := l.requests[key]; counter != nil {
			counter.dirty = true
		}
		if counter := l.tokens[key]; counter != nil {
			counter.dirty = true
		}
	}
}

// setRemote installs the other writers' summed counters, replacing the
// previous load's.
func (l *limiter) setRemote(requests, tokens map[ruleKey]windowCounter, inFlight map[ruleKey]int64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.remoteRequests = requests
	l.remoteTokens = tokens
	l.remoteInFlight = inFlight
}
