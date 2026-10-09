package metadataoverrides

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/modelselectors"
	"github.com/enterpilot/gomodel/internal/storage"
)

// Registry is the model registry surface the service needs: configured
// provider names for selector validation, and the override layer it fills.
type Registry interface {
	modelselectors.Catalog
	SetDashboardMetadataOverrides(map[string]map[string]*core.ModelMetadata)
}

// Service caches metadata overrides and keeps the registry's dashboard layer
// in sync with storage.
type Service struct {
	store     Store
	registry  Registry
	current   atomic.Pointer[map[string]Override]
	refreshMu sync.Mutex
}

const refreshTimeout = 30 * time.Second

// NewService creates a metadata override service backed by storage.
func NewService(store Store, registry Registry) (*Service, error) {
	if store == nil {
		return nil, fmt.Errorf("store is required")
	}
	if registry == nil {
		return nil, fmt.Errorf("registry is required")
	}
	s := &Service{store: store, registry: registry}
	empty := map[string]Override{}
	s.current.Store(&empty)
	return s, nil
}

// Refresh reloads overrides from storage and applies them to the registry.
// A stored row that no longer validates is skipped with a warning rather than
// failing the refresh, so one bad row cannot block the rest or startup.
func (s *Service) Refresh(ctx context.Context) error {
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()

	stored, err := s.store.List(ctx)
	if err != nil {
		return fmt.Errorf("list model metadata overrides: %w", err)
	}
	next := make(map[string]Override, len(stored))
	layer := make(map[string]map[string]*core.ModelMetadata)
	for _, row := range stored {
		override, err := normalizeStored(row)
		if err != nil {
			slog.Warn("skipping invalid model metadata override", "selector", row.Selector, "error", err)
			continue
		}
		next[override.Selector] = override
		if layer[override.ProviderName] == nil {
			layer[override.ProviderName] = make(map[string]*core.ModelMetadata)
		}
		layer[override.ProviderName][override.Model] = override.Metadata.coreMetadata()
	}
	s.current.Store(&next)
	s.registry.SetDashboardMetadataOverrides(layer)
	return nil
}

// List returns all cached overrides sorted by selector.
func (s *Service) List() []Override {
	current := *s.current.Load()
	result := make([]Override, 0, len(current))
	for _, override := range current {
		result = append(result, cloneOverride(override))
	}
	slices.SortFunc(result, func(a, b Override) int { return strings.Compare(a.Selector, b.Selector) })
	return result
}

// Get returns one cached override by selector.
func (s *Service) Get(selector string) (Override, bool) {
	parts, err := modelselectors.NormalizeInput(s.registry, selector)
	if err != nil {
		return Override{}, false
	}
	override, ok := (*s.current.Load())[parts.Selector]
	if !ok {
		return Override{}, false
	}
	return cloneOverride(override), true
}

// Upsert validates and stores one override, then applies it. It returns the
// normalized override.
func (s *Service) Upsert(ctx context.Context, override Override) (Override, error) {
	normalized, err := normalizeInput(s.registry, override)
	if err != nil {
		return Override{}, err
	}
	if err := s.store.Upsert(ctx, normalized); err != nil {
		return Override{}, err
	}
	if err := s.Refresh(ctx); err != nil {
		return Override{}, err
	}
	saved, ok := (*s.current.Load())[normalized.Selector]
	if !ok {
		return Override{}, fmt.Errorf("model metadata override %q missing after save", normalized.Selector)
	}
	return cloneOverride(saved), nil
}

// Delete removes one override and reverts the model to its lower layers.
func (s *Service) Delete(ctx context.Context, selector string) error {
	parts, err := modelselectors.NormalizeInput(s.registry, selector)
	if err != nil {
		return err
	}
	if err := s.store.Delete(ctx, parts.Selector); err != nil {
		return err
	}
	return s.Refresh(ctx)
}

// StartBackgroundRefresh periodically reloads overrides so changes made
// through another gateway instance sharing the storage are picked up.
func (s *Service) StartBackgroundRefresh(interval time.Duration) func() {
	interval = max(interval, refreshTimeout)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				refreshCtx, refreshCancel := context.WithTimeout(ctx, refreshTimeout)
				if err := s.Refresh(refreshCtx); err != nil {
					slog.Error("failed to refresh model metadata overrides", "error", err)
				}
				refreshCancel()
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			cancel()
			<-done
		})
	}
}

// Result holds the initialized service and the resources it owns.
type Result struct {
	Service     *Service
	store       Store
	stopRefresh func()
	closeOnce   sync.Once
	closeErr    error
}

// Close stops the background refresh and releases the store.
func (r *Result) Close() error {
	if r == nil {
		return nil
	}
	r.closeOnce.Do(func() {
		if r.stopRefresh != nil {
			r.stopRefresh()
		}
		if r.store != nil {
			r.closeErr = r.store.Close()
		}
	})
	return r.closeErr
}

// New creates the metadata override subsystem on the shared storage, loads
// the stored overrides into the registry, and starts the background refresh
// on the same interval as the other dashboard-managed stores.
func New(ctx context.Context, cfg *config.Config, shared storage.Storage, registry Registry) (*Result, error) {
	if shared == nil {
		return nil, fmt.Errorf("shared storage is required")
	}
	if cfg == nil {
		return nil, fmt.Errorf("config is required")
	}
	store, err := createStore(ctx, shared)
	if err != nil {
		return nil, err
	}
	service, err := NewService(store, registry)
	if err != nil {
		return nil, err
	}
	if err := service.Refresh(ctx); err != nil {
		return nil, err
	}
	interval := time.Minute
	if cfg.Workflows.RefreshInterval > 0 {
		interval = cfg.Workflows.RefreshInterval
	}
	return &Result{
		Service:     service,
		store:       store,
		stopRefresh: service.StartBackgroundRefresh(interval),
	}, nil
}
