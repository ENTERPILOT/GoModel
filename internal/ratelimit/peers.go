package ratelimit

import (
	"context"
	"time"
)

const (
	// peerLoadOverlap is how far before the previous load an incremental
	// load starts reading. A save stamps its rows before its transaction
	// commits, up to persistTimeout later, and replicas' clocks differ a
	// little; reading that far back again catches both.
	peerLoadOverlap = 2 * persistTimeout
	// peerFullLoadInterval is how often the incremental load is replaced by a
	// full one. Nothing should need it, but it bounds what any row missed
	// for whatever reason (a clock far off, a stalled commit) can cost, and
	// drops rows other replicas deleted.
	peerFullLoadInterval = time.Minute
	// minInFlightLiveness is the floor of Service.inFlightLiveness.
	minInFlightLiveness = 5 * time.Second
)

// rowIdentity is the key every store gives a counter row.
type rowIdentity struct {
	scope         string
	subject       string
	partition     string
	periodSeconds int64
	writer        string
}

func identityOf(snap WindowSnapshot) rowIdentity {
	return rowIdentity{
		scope:         snap.Scope,
		subject:       snap.Subject,
		partition:     snap.Partition,
		periodSeconds: snap.PeriodSeconds,
		writer:        snap.Writer,
	}
}

// peerView caches the other writers' rows between loads, so a load only has
// to read the rows that changed since the previous one. Rows nobody rewrites
// any more, a stopped writer's, stay cached until their windows roll out.
// It is guarded by Service.persistMu.
type peerView struct {
	rows map[rowIdentity]WindowSnapshot
	// loadedAt is the clock reading taken just before the last successful
	// load, and fullAt that of the last full one.
	loadedAt time.Time
	fullAt   time.Time
}

// merge folds a load into the cache. A full load replaces it.
func (v *peerView) merge(rows []WindowSnapshot, self string, full bool) {
	if full || v.rows == nil {
		v.rows = make(map[rowIdentity]WindowSnapshot, len(rows))
	}
	for _, row := range rows {
		if row.Writer == self {
			continue
		}
		v.rows[identityOf(row)] = row
	}
}

// sum adds the cached rows up per key, as of now, for the rules in force:
//
//   - A window row is rolled forward to the window containing now, as
//     admission rolls a counter, and added to the key's request and token
//     sums. Rolling forward is linear, so the sliding estimate of the sum is
//     the sum of the rows' estimates. Rows counted under another epoch of the
//     rule (see Rule.CounterEpoch) are skipped; rows whose windows rolled out
//     are dropped from the cache.
//   - An in-flight row counts only while its writer keeps rewriting it: one
//     not saved within liveness is a stopped or crashed writer's, and its
//     slots are free. It is dropped from the cache then, and once it reads
//     zero. In-flight rows ignore the epoch, as a reset leaves in-flight
//     gauges alone (see limiter.reset).
//
// A key nobody else counted gets no entry, and a cluster of one gets nil maps.
func (v *peerView) sum(rules []Rule, now time.Time, liveness time.Duration) (requests, tokens map[ruleKey]windowCounter, inFlight map[ruleKey]int64) {
	index := indexRules(rules)
	nowUnix := now.Unix()
	for id, row := range v.rows {
		key := ruleKey{
			scope:         RuleScope(row.Scope),
			subject:       row.Subject,
			partition:     row.Partition,
			periodSeconds: row.PeriodSeconds,
		}
		switch {
		case row.PeriodSeconds == PeriodConcurrent:
			if row.InFlight <= 0 || liveness <= 0 || nowUnix-row.UpdatedAt > int64(liveness/time.Second) {
				delete(v.rows, id)
				continue
			}
			if _, ok := index.lookup(key); !ok {
				continue
			}
			if inFlight == nil {
				inFlight = make(map[ruleKey]int64)
			}
			inFlight[key] += row.InFlight
		case row.PeriodSeconds < 0:
			delete(v.rows, id)
		default:
			latest := max(row.RequestsWindowStart, row.TokensWindowStart)
			if latest+2*row.PeriodSeconds < nowUnix {
				delete(v.rows, id)
				continue
			}
			rule, ok := index.lookup(key)
			if !ok || row.Epoch != rule.CounterEpoch {
				continue
			}
			requests = addCounter(requests, key, windowCounter{
				windowStart: row.RequestsWindowStart,
				current:     row.RequestsCurrent,
				previous:    row.RequestsPrevious,
			}, now)
			tokens = addCounter(tokens, key, windowCounter{
				windowStart: row.TokensWindowStart,
				current:     row.TokensCurrent,
				previous:    row.TokensPrevious,
			}, now)
		}
	}
	return requests, tokens, inFlight
}

// addCounter adds one row's counter, rolled to now, to the sum for key. A row
// that holds nothing at now adds no entry.
func addCounter(sums map[ruleKey]windowCounter, key ruleKey, row windowCounter, now time.Time) map[ruleKey]windowCounter {
	if row.current == 0 && row.previous == 0 {
		return sums
	}
	row = row.rolledTo(now, key.periodSeconds)
	if row.current == 0 && row.previous == 0 {
		return sums
	}
	if sums == nil {
		sums = make(map[ruleKey]windowCounter)
	}
	sum := sums[key]
	sum.windowStart = row.windowStart
	sum.current += row.current
	sum.previous += row.previous
	sums[key] = sum
	return sums
}

// inFlightLiveness is how long a peer's in-flight row counts after its last
// save. Live writers rewrite it every flush, so three missed flushes, with a
// floor for very short intervals, mean the writer stopped. Without a flush
// loop nothing rewrites in-flight rows, or reloads them, so none count.
func (s *Service) inFlightLiveness() time.Duration {
	if s.flushInterval <= 0 {
		return 0
	}
	return max(3*s.flushInterval, minInFlightLiveness)
}

// loadPeers reads the rows other writers saved since the previous load (or
// all of them, see peerFullLoadInterval) and installs their sums as the
// limiter's remote counters.
func (s *Service) loadPeers(ctx context.Context) error {
	startedAt := s.now()
	s.persistMu.Lock()
	full := s.peers.rows == nil || startedAt.Sub(s.peers.fullAt) >= peerFullLoadInterval
	var since int64
	if !full {
		since = s.peers.loadedAt.Add(-peerLoadOverlap).Unix()
	}
	s.persistMu.Unlock()

	// The read runs outside persistMu, so a slow store never holds up a
	// Refresh. installPeers sums under the rules in force when it runs.
	loadCtx, cancel := persistContext(ctx)
	rows, err := s.store.LoadCounters(loadCtx, since)
	cancel()
	if err != nil {
		return err
	}
	s.installPeers(rows, startedAt, full)
	return nil
}

// installPeers merges a load into the cache and installs the sums. It runs
// under persistMu, like Refresh's swap and the resets it implies, so sums
// taken under rules a Refresh just replaced never land after its resets.
func (s *Service) installPeers(rows []WindowSnapshot, loadedAt time.Time, full bool) {
	s.persistMu.Lock()
	defer s.persistMu.Unlock()
	s.peers.merge(rows, s.writer, full)
	s.peers.loadedAt = loadedAt
	if full {
		s.peers.fullAt = loadedAt
	}
	requests, tokens, inFlight := s.peers.sum(s.Rules(), s.now(), s.inFlightLiveness())
	s.limiter.setRemote(requests, tokens, inFlight)
}
