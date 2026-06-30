package failover

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/goccy/go-json"
)

var ErrNotFound = errors.New("failover mapping not found")
var ErrManaged = errors.New("failover mapping is managed by configuration")

// Store defines persistence operations for dashboard-managed failover mappings.
type Store interface {
	List(ctx context.Context) ([]Rule, error)
	Get(ctx context.Context, source string) (*Rule, error)
	Upsert(ctx context.Context, rule Rule) error
	Delete(ctx context.Context, source string) error
	DeleteAll(ctx context.Context) error
	Close() error
}

func encodeTargets(targets []string) (string, error) {
	if targets == nil {
		targets = []string{}
	}
	data, err := json.Marshal(targets)
	if err != nil {
		return "", fmt.Errorf("encode targets: %w", err)
	}
	return string(data), nil
}

func decodeTargets(data []byte) ([]string, error) {
	if len(data) == 0 {
		return nil, nil
	}
	var targets []string
	if err := json.Unmarshal(data, &targets); err != nil {
		return nil, fmt.Errorf("decode targets: %w", err)
	}
	if len(targets) == 0 {
		return nil, nil
	}
	return targets, nil
}

func stampUpsert(rule *Rule) {
	now := time.Now().UTC()
	if rule.CreatedAt.IsZero() {
		rule.CreatedAt = now
	}
	rule.UpdatedAt = now
}

func collectRules(next func() (Rule, bool, error), rowsErr func() error) ([]Rule, error) {
	result := make([]Rule, 0)
	for {
		rule, ok, err := next()
		if err != nil {
			return nil, err
		}
		if !ok {
			break
		}
		result = append(result, rule)
	}
	if err := rowsErr(); err != nil {
		return nil, fmt.Errorf("iterate failover mappings: %w", err)
	}
	return result, nil
}
