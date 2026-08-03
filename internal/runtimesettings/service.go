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

type Service struct {
	mu       sync.Mutex
	store    Store
	settings map[string]ext.RuntimeSetting
	order    []string
}

func New(ctx context.Context, backend storage.Storage, registered []ext.RuntimeSetting) (*Service, error) {
	if len(registered) == 0 {
		return nil, nil
	}
	store, err := storage.ResolveSQLBackend[Store](ctx, backend,
		func(db sqlx.DB) (Store, error) { return NewSQLStore(ctx, db) },
		func(database *mongo.Database) (Store, error) { return NewMongoDBStore(ctx, database) },
	)
	if err != nil {
		return nil, fmt.Errorf("create runtime settings store: %w", err)
	}
	service := &Service{store: store, settings: make(map[string]ext.RuntimeSetting, len(registered))}
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
		value, found, getErr := store.Get(ctx, key)
		if getErr != nil {
			return nil, getErr
		}
		if found {
			if applyErr := setting.Apply(value); applyErr != nil {
				slog.Warn("ignoring invalid persisted runtime setting", "key", key, "value", value, "error", applyErr)
			}
		}
	}
	slices.Sort(service.order)
	return service, nil
}

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

func (s *Service) Update(ctx context.Context, key, value string) (ext.SettingDescriptor, error) {
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
	if !slices.ContainsFunc(previous.Options, func(option ext.SettingOption) bool { return option.Value == value }) {
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

func (s *Service) Close() error {
	if s == nil || s.store == nil {
		return nil
	}
	return s.store.Close()
}
