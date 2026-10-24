package runtimesettings

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"

	"github.com/enterpilot/gomodel/ext"
	"github.com/enterpilot/gomodel/internal/storage"
	"github.com/enterpilot/gomodel/internal/storage/sqlx"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

var (
	ErrNotFound = errors.New("runtime setting not found")
	ErrLocked   = errors.New("runtime setting is managed externally")
	ErrInvalid  = errors.New("invalid runtime setting value")
)

// Service validates, persists, applies, and reconciles registered settings.
type Service struct {
	mu       sync.Mutex
	store    Store
	settings map[string]ext.RuntimeSetting
	order    []string
	rejected map[string]string
	cancel   context.CancelFunc
	done     chan struct{}
}

// NewStore opens the deployment-wide key/value store on the shared backend.
// Besides the registered settings it holds other small per-deployment state
// that must live with the database rather than the filesystem, such as the
// install identifier.
func NewStore(ctx context.Context, backend storage.Storage) (Store, error) {
	store, err := storage.ResolveSQLBackend[Store](ctx, backend,
		func(db sqlx.DB) (Store, error) { return NewSQLStore(ctx, db) },
		func(database *mongo.Database) (Store, error) { return NewMongoDBStore(ctx, database) },
	)
	if err != nil {
		return nil, fmt.Errorf("create runtime settings store: %w", err)
	}
	return store, nil
}

// New restores registered settings and starts cross-instance reconciliation.
func New(ctx context.Context, backend storage.Storage, registered []ext.RuntimeSetting) (*Service, error) {
	if len(registered) == 0 {
		return nil, nil
	}
	store, err := NewStore(ctx, backend)
	if err != nil {
		return nil, err
	}
	service := &Service{
		store:    store,
		settings: make(map[string]ext.RuntimeSetting, len(registered)),
		rejected: make(map[string]string),
	}
	editable := false
	for _, setting := range registered {
		if setting == nil {
			continue
		}
		descriptor := setting.Descriptor()
		key := strings.TrimSpace(descriptor.Key)
		if key == "" {
			return nil, fmt.Errorf("runtime setting has an empty key")
		}
		if _, exists := service.settings[key]; exists {
			return nil, fmt.Errorf("runtime setting %q is registered more than once", key)
		}
		service.settings[key] = setting
		service.order = append(service.order, key)
		if descriptor.Locked {
			continue
		}
		editable = true
		if len(descriptor.Options) == 0 {
			return nil, fmt.Errorf("runtime setting %q has no allowed options", key)
		}
		if !valueAllowed(descriptor, descriptor.Value) {
			return nil, fmt.Errorf("runtime setting %q current value %q is not an allowed option", key, descriptor.Value)
		}
		value, found, getErr := store.Get(ctx, key)
		if getErr != nil {
			return nil, getErr
		}
		if found && value != descriptor.Value {
			if !valueAllowed(descriptor, value) {
				slog.Warn("ignoring invalid persisted runtime setting", "key", key, "value", value)
				service.rejected[key] = value
				continue
			}
			if applyErr := setting.Apply(value); applyErr != nil {
				slog.Warn("ignoring invalid persisted runtime setting", "key", key, "value", value, "error", applyErr)
			}
		}
	}
	slices.Sort(service.order)
	if editable {
		service.startSync()
	}
	return service, nil
}

// List returns the current setting descriptors in stable key order.
func (s *Service) List() []ext.SettingDescriptor {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]ext.SettingDescriptor, 0, len(s.order))
	for _, key := range s.order {
		result = append(result, s.settings[key].Descriptor())
	}
	return result
}

// Update applies and persists one setting, rolling back if persistence fails.
func (s *Service) Update(ctx context.Context, key, value string) (ext.SettingDescriptor, error) {
	if s == nil {
		return ext.SettingDescriptor{}, ErrNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	setting, ok := s.settings[key]
	if !ok {
		return ext.SettingDescriptor{}, ErrNotFound
	}
	previous := setting.Descriptor()
	if previous.Locked {
		return ext.SettingDescriptor{}, ErrLocked
	}
	if !valueAllowed(previous, value) {
		return ext.SettingDescriptor{}, fmt.Errorf("%w: %q is not allowed for %s", ErrInvalid, value, key)
	}
	if err := setting.Apply(value); err != nil {
		return ext.SettingDescriptor{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if err := s.store.Set(ctx, key, setting.Descriptor().Value); err != nil {
		if rollbackErr := setting.Apply(previous.Value); rollbackErr != nil {
			slog.Error("failed to roll back runtime setting", "key", key, "error", rollbackErr)
		}
		return ext.SettingDescriptor{}, err
	}
	return setting.Descriptor(), nil
}

// Close stops background reconciliation.
func (s *Service) Close() error {
	if s == nil || s.cancel == nil {
		return nil
	}
	s.cancel()
	<-s.done
	return nil
}

func valueAllowed(descriptor ext.SettingDescriptor, value string) bool {
	return slices.ContainsFunc(descriptor.Options, func(option ext.SettingOption) bool {
		return option.Value == value
	})
}
