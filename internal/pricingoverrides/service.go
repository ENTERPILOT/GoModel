package pricingoverrides

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/enterpilot/gomodel/internal/modelselectors"
	"github.com/enterpilot/gomodel/internal/usage"
)

// Service keeps pricing overrides cached in memory and resolves effective pricing.
type Service struct {
	store     Store
	catalog   Catalog
	base      usage.PricingResolver
	current   atomic.Value
	refreshMu sync.Mutex
}

const refreshTimeout = 30 * time.Second

// NewService creates a pricing override service backed by storage.
func NewService(store Store, catalog Catalog, base usage.PricingResolver) (*Service, error) {
	if store == nil {
		return nil, fmt.Errorf("store is required")
	}
	if catalog == nil {
		return nil, fmt.Errorf("catalog is required")
	}

	service := &Service{
		store:   store,
		catalog: catalog,
		base:    base,
	}
	service.current.Store(emptySnapshot())
	return service, nil
}

// Refresh reloads overrides from storage and atomically swaps the snapshot.
func (s *Service) Refresh(ctx context.Context) error {
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()
	return s.refreshLocked(ctx)
}

func (s *Service) refreshLocked(ctx context.Context) error {
	overrides, err := s.store.List(ctx)
	if err != nil {
		return fmt.Errorf("list model pricing overrides: %w", err)
	}
	next, err := s.buildSnapshot(overrides)
	if err != nil {
		return err
	}
	s.current.Store(next)
	return nil
}

func (s *Service) snapshot() snapshot {
	if s == nil {
		return emptySnapshot()
	}
	return s.current.Load().(snapshot)
}

// List returns all cached overrides sorted by selector.
func (s *Service) List() []Override {
	snap := s.snapshot()
	result := make([]Override, 0, len(snap.order))
	for _, selector := range snap.order {
		result = append(result, overrideClone(snap.bySelector[selector]))
	}
	return result
}

// ListViews returns all cached overrides with scope metadata.
func (s *Service) ListViews() []View {
	overrides := s.List()
	result := make([]View, 0, len(overrides))
	for _, override := range overrides {
		result = append(result, viewForOverride(override))
	}
	return result
}

// GetView returns one cached override with scope metadata by normalized selector.
func (s *Service) GetView(selector string) (*View, bool) {
	override, ok := s.Get(selector)
	if !ok || override == nil {
		return nil, false
	}
	view := viewForOverride(*override)
	return &view, true
}

func viewForOverride(override Override) View {
	return View{
		Override:  override,
		ScopeKind: override.ScopeKind(),
	}
}

// Get returns one cached override by normalized selector.
func (s *Service) Get(selector string) (*Override, bool) {
	parts, err := modelselectors.NormalizeInput(s.catalog, selector)
	if err != nil {
		return nil, false
	}
	override, ok := s.snapshot().bySelector[parts.Selector]
	if !ok {
		return nil, false
	}
	override = overrideClone(override)
	return &override, true
}

// Upsert validates and stores one override, then refreshes the in-memory snapshot.
func (s *Service) Upsert(ctx context.Context, override Override) error {
	if s == nil {
		return fmt.Errorf("model pricing override service is required")
	}

	normalized, err := normalizeOverrideInput(s.catalog, override)
	if err != nil {
		return err
	}

	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()

	current := s.snapshot()
	if _, err := s.buildSnapshot(upsertOverride(snapshotOverrides(current), normalized)); err != nil {
		return fmt.Errorf("validate model pricing overrides: %w", err)
	}
	previous, existed := current.bySelector[normalized.Selector]
	if err := s.store.Upsert(ctx, normalized); err != nil {
		return fmt.Errorf("upsert model pricing override: %w", err)
	}
	if err := s.refreshLocked(ctx); err != nil {
		rollbackCtx, cancel := rollbackContext()
		defer cancel()

		var rollbackErr error
		if existed {
			rollbackErr = s.store.Upsert(rollbackCtx, previous)
		} else {
			rollbackErr = s.store.Delete(rollbackCtx, normalized.Selector)
		}
		if rollbackErr != nil {
			return s.reconcileSnapshotAfterRollbackFailureLocked("upsert", err, rollbackErr)
		}
		return fmt.Errorf("refresh model pricing overrides: %w", err)
	}
	return nil
}

// Delete removes one override and refreshes the in-memory snapshot.
func (s *Service) Delete(ctx context.Context, selector string) error {
	if s == nil {
		return fmt.Errorf("model pricing override service is required")
	}

	parts, err := modelselectors.NormalizeInput(s.catalog, selector)
	if err != nil {
		return err
	}

	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()

	current := s.snapshot()
	if _, err := s.buildSnapshot(deleteOverride(snapshotOverrides(current), parts.Selector)); err != nil {
		return fmt.Errorf("validate model pricing overrides: %w", err)
	}
	previous, existed := current.bySelector[parts.Selector]
	if err := s.store.Delete(ctx, parts.Selector); err != nil {
		return fmt.Errorf("delete model pricing override: %w", err)
	}
	if err := s.refreshLocked(ctx); err != nil {
		// If the selector was absent from the snapshot, there is no known previous
		// value to restore, so we intentionally skip rollback.
		if !existed {
			return fmt.Errorf("refresh model pricing overrides: %w", err)
		}
		rollbackCtx, cancel := rollbackContext()
		defer cancel()
		if rollbackErr := s.store.Upsert(rollbackCtx, previous); rollbackErr != nil {
			return s.reconcileSnapshotAfterRollbackFailureLocked("delete", err, rollbackErr)
		}
		return fmt.Errorf("refresh model pricing overrides: %w", err)
	}
	return nil
}

// StartBackgroundRefresh periodically reloads pricing overrides from storage until stopped.
// Each s.Refresh call is capped by refreshTimeout, and shorter intervals are clamped to refreshTimeout.
func (s *Service) StartBackgroundRefresh(interval time.Duration) func() {
	interval = normalizedRefreshInterval(interval)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	var once sync.Once

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
					slog.Error("failed to refresh model pricing overrides", "error", err)
				}
				refreshCancel()
			}
		}
	}()

	return func() {
		once.Do(func() {
			cancel()
			<-done
		})
	}
}

func normalizedRefreshInterval(interval time.Duration) time.Duration {
	if interval <= 0 {
		return time.Minute
	}
	if interval < refreshTimeout {
		return refreshTimeout
	}
	return interval
}

// rollbackContext deliberately uses context.Background with context.WithTimeout
// so cleanup can continue briefly even when the caller's request context is canceled.
func rollbackContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), refreshTimeout)
}

func (s *Service) reconcileSnapshotAfterRollbackFailureLocked(operation string, refreshErr, rollbackErr error) error {
	reconcileCtx, cancel := rollbackContext()
	defer cancel()

	if reconcileErr := s.refreshLocked(reconcileCtx); reconcileErr != nil {
		slog.Warn(
			"model pricing override snapshot may be stale after failed rollback",
			"operation", operation,
			"refresh_error", refreshErr,
			"rollback_error", rollbackErr,
			"reconcile_error", reconcileErr,
		)
		return fmt.Errorf("refresh model pricing overrides: %w (rollback failed: %v; snapshot refresh failed: %v)", refreshErr, rollbackErr, reconcileErr)
	}

	slog.Warn(
		"model pricing override rollback failed; refreshed snapshot from persisted state",
		"operation", operation,
		"refresh_error", refreshErr,
		"rollback_error", rollbackErr,
	)
	return fmt.Errorf("refresh model pricing overrides: %w (rollback failed: %v)", refreshErr, rollbackErr)
}
