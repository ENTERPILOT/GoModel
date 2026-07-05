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
	DeleteRule(ctx context.Context, userPath string, periodSeconds int64) error
	ReplaceConfigRules(ctx context.Context, rules []Rule) error
	Close() error
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
		key := ruleStoreKey(item.UserPath, item.PeriodSeconds)
		if existing, ok := seen[key]; ok {
			normalized[existing] = item
			continue
		}
		seen[key] = len(normalized)
		normalized = append(normalized, item)
	}
	return normalized, nil
}

func ruleStoreKey(userPath string, periodSeconds int64) string {
	return strings.TrimSpace(userPath) + ":" + fmt.Sprint(periodSeconds)
}
