package ratelimit

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

var ErrNotFound = errors.New("rate limit rule not found")

// inFlightRowRetention is how long a store keeps an in-flight row nobody
// saves any more. Peers stop counting it after a few flush intervals (see
// Service.inFlightLiveness); this only bounds how long it takes space.
const inFlightRowRetention = PeriodDaySeconds

// counterRetention is how long a store keeps a counter row after its last
// save: two of its periods for a window row, inFlightRowRetention for an
// in-flight one.
func counterRetention(periodSeconds int64) int64 {
	if periodSeconds == PeriodConcurrent {
		return inFlightRowRetention
	}
	return 2 * periodSeconds
}

// Store persists rate limit rule definitions and optional window snapshots.
type Store interface {
	ListRules(ctx context.Context) ([]Rule, error)
	UpsertRules(ctx context.Context, rules []Rule) error
	DeleteRule(ctx context.Context, scope RuleScope, subject string, periodSeconds int64) error
	ReplaceConfigRules(ctx context.Context, rules []Rule) error
	// LoadCounters returns the rows saved at or after since, in unix seconds
	// by the writer's clock (WindowSnapshot.UpdatedAt), by every writer but
	// exclude; since 0 returns them all, and an empty exclude every writer's.
	LoadCounters(ctx context.Context, since int64, exclude string) ([]WindowSnapshot, error)
	// SaveCounters upserts the given snapshots, keyed by their Writer as well
	// as their window, so writers never overwrite each other's rows, and
	// stamps them with the current time and the time they expire (see
	// counterRetention). A snapshot holds each key at most once.
	// Nothing is deleted to make room for a write, so a crash mid-save costs at
	// most the flush interval rather than a whole window.
	SaveCounters(ctx context.Context, snapshots []WindowSnapshot) error
	// PruneCounters deletes the rows that expired: window rows no writer has
	// saved for two of their own periods — by then every count they hold has
	// rolled out — and in-flight rows no writer has saved for a day, long
	// after peers stopped counting them. It runs apart from SaveCounters,
	// about once a minute per replica, as every row it finds is one nobody
	// reads any more.
	PruneCounters(ctx context.Context) error
	// ResetCounter gives the rule a new counter epoch, so every replica's
	// next Refresh clears its counts and loads skip every row saved before,
	// and removes the window rows of every writer. In-flight rows stay: they
	// hold no counts to reset, only slots live requests still take, which a
	// reset leaves to them in memory too (see limiter.reset).
	// ResetAllCounters does the same for every rule.
	ResetCounter(ctx context.Context, scope RuleScope, subject string, periodSeconds int64) error
	ResetAllCounters(ctx context.Context) error
	Close() error
}

// validatePeriodSeconds is the single source of the period sanity check used
// by rule normalization and every DeleteRule path.
func validatePeriodSeconds(periodSeconds int64) error {
	if periodSeconds < 0 {
		return fmt.Errorf("period_seconds must be 0 (concurrent) or greater")
	}
	return nil
}

func normalizeRulesForUpsert(rules []Rule) ([]Rule, error) {
	if len(rules) == 0 {
		return nil, nil
	}
	normalized := make([]Rule, 0, len(rules))
	seen := make(map[string]int, len(rules))
	for _, rule := range rules {
		item, err := NormalizeRule(rule)
		if err != nil {
			return nil, err
		}
		key := ruleStoreKey(item.Scope, item.Subject, item.PeriodSeconds)
		if existing, ok := seen[key]; ok {
			normalized[existing] = item
			continue
		}
		seen[key] = len(normalized)
		normalized = append(normalized, item)
	}
	return normalized, nil
}

func ruleStoreKey(scope RuleScope, subject string, periodSeconds int64) string {
	return string(scope) + ":" + strings.TrimSpace(subject) + ":" + fmt.Sprint(periodSeconds)
}
