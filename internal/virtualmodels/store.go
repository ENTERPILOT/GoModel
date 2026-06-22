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

// collectVirtualModels drains a row iterator into a slice. It mirrors the
// shared collector used by the legacy stores so the SQL backends do not inline
// near-identical loops.
func collectVirtualModels(next func() (VirtualModel, bool, error), rowsErr func() error) ([]VirtualModel, error) {
	result := make([]VirtualModel, 0)
	for {
		vm, ok, err := next()
		if err != nil {
			return nil, err
		}
		if !ok {
			break
		}
		result = append(result, vm)
	}
	if err := rowsErr(); err != nil {
		return nil, fmt.Errorf("iterate virtual models: %w", err)
	}
	return result, nil
}

// stampUpsert sets timestamps the way the legacy stores did: CreatedAt on
// insert, UpdatedAt always.
func stampUpsert(vm *VirtualModel) {
	now := time.Now().UTC()
	if vm.CreatedAt.IsZero() {
		vm.CreatedAt = now
	}
	vm.UpdatedAt = now
}
