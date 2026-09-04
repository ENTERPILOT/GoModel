package virtualmodels

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/goccy/go-json"
)

// ErrNotFound indicates a requested virtual model was not found.
var ErrNotFound = errors.New("virtual model not found")

// Store defines persistence operations for virtual models.
type Store interface {
	List(ctx context.Context) ([]VirtualModel, error)
	Get(ctx context.Context, source string) (*VirtualModel, error)
	Upsert(ctx context.Context, vm VirtualModel) error
	Delete(ctx context.Context, source string) error
	Close() error
}

func encodeTargets(targets []Target) (string, error) {
	if targets == nil {
		targets = []Target{}
	}
	data, err := json.Marshal(targets)
	if err != nil {
		return "", fmt.Errorf("encode targets: %w", err)
	}
	return string(data), nil
}

func encodeUserPaths(paths []string) (string, error) {
	if paths == nil {
		paths = []string{}
	}
	data, err := json.Marshal(paths)
	if err != nil {
		return "", fmt.Errorf("encode user_paths: %w", err)
	}
	return string(data), nil
}

// encodeStrategyConfig stores a strategy config as a JSON object; nil and
// empty both persist as "{}".
func encodeStrategyConfig(config map[string]any) (string, error) {
	if config == nil {
		config = map[string]any{}
	}
	data, err := json.Marshal(config)
	if err != nil {
		return "", fmt.Errorf("encode strategy_config: %w", err)
	}
	return string(data), nil
}

// decodeStrategyConfig reads a stored strategy config; an empty object loads
// as nil so rows written before the column existed compare equal to new ones.
func decodeStrategyConfig(data []byte) (map[string]any, error) {
	if len(data) == 0 {
		return nil, nil
	}
	var config map[string]any
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("decode strategy_config: %w", err)
	}
	if len(config) == 0 {
		return nil, nil
	}
	return config, nil
}

func decodeTargets(data []byte) ([]Target, error) {
	if len(data) == 0 {
		return nil, nil
	}
	var targets []Target
	if err := json.Unmarshal(data, &targets); err != nil {
		return nil, fmt.Errorf("decode targets: %w", err)
	}
	if len(targets) == 0 {
		return nil, nil
	}
	return targets, nil
}

func decodeUserPaths(data []byte) ([]string, error) {
	if len(data) == 0 {
		return nil, nil
	}
	var paths []string
	if err := json.Unmarshal(data, &paths); err != nil {
		return nil, fmt.Errorf("decode user_paths: %w", err)
	}
	if len(paths) == 0 {
		return nil, nil
	}
	return paths, nil
}

// stampUpsert sets CreatedAt on insert and UpdatedAt on every write.
func stampUpsert(vm *VirtualModel) {
	now := time.Now().UTC()
	if vm.CreatedAt.IsZero() {
		vm.CreatedAt = now
	}
	vm.UpdatedAt = now
}
