package ratelimit

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

var ErrNotFound = errors.New("rate limit rule not found")

// Store persists rate limit rule definitions. Live counters are in-memory
// and never stored.
type Store interface {
	ListRules(ctx context.Context) ([]Rule, error)
	UpsertRules(ctx context.Context, rules []Rule) error
	DeleteRule(ctx context.Context, scope RuleScope, subject string, periodSeconds int64) error
	ReplaceConfigRules(ctx context.Context, rules []Rule) error
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
