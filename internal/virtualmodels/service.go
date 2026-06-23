package virtualmodels

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"gomodel/internal/core"
)

// Service is the single native engine over the virtual_models store. It serves
// both redirect resolution (alias behavior) and policy authorization (access
// override behavior) from one atomically swapped in-memory snapshot.
type Service struct {
	store          Store
	catalog        Catalog
	defaultEnabled bool

	current   atomic.Value // snapshot
	refreshMu sync.Mutex
}

// NewService creates a virtual models service backed by the store and catalog.
// defaultEnabled is the process-wide model availability default consulted when
// no policy matches.
func NewService(store Store, catalog Catalog, defaultEnabled bool) (*Service, error) {
	if store == nil {
		return nil, fmt.Errorf("store is required")
	}
	if catalog == nil {
		return nil, fmt.Errorf("catalog is required")
	}
	service := &Service{
		store:          store,
		catalog:        catalog,
		defaultEnabled: defaultEnabled,
	}
	service.current.Store(emptySnapshot(defaultEnabled))
	return service, nil
}

func (s *Service) snapshot() snapshot {
	if s == nil {
		return emptySnapshot(true)
	}
	return s.current.Load().(snapshot)
}

// Refresh reloads virtual models from storage and atomically swaps the snapshot.
func (s *Service) Refresh(ctx context.Context) error {
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()
	return s.refreshLocked(ctx)
}

func (s *Service) refreshLocked(ctx context.Context) error {
	rows, err := s.store.List(ctx)
	if err != nil {
		return fmt.Errorf("list virtual models: %w", err)
	}
	next, err := buildSnapshot(rows, s.defaultEnabled)
	if err != nil {
		return err
	}
	s.current.Store(next)
	return nil
}

// StartBackgroundRefresh periodically reloads virtual models until stopped.
func (s *Service) StartBackgroundRefresh(interval time.Duration) func() {
	if interval <= 0 {
		interval = time.Hour
	}

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
				refreshCtx, refreshCancel := context.WithTimeout(ctx, 30*time.Second)
				if err := s.Refresh(refreshCtx); err != nil {
					slog.Error("failed to refresh virtual models", "error", err)
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

// List returns all cached virtual models sorted by source.
func (s *Service) List() []VirtualModel {
	return s.snapshot().rows()
}

// Get returns one cached virtual model by source.
func (s *Service) Get(source string) (*VirtualModel, bool) {
	if vm, _, ok := s.snapshot().lookupCanonicalSource(source); ok {
		clone := vm.clone()
		return &clone, true
	}
	return nil, false
}

// ListViews returns all virtual models (redirects and policies) for the admin UI.
func (s *Service) ListViews() []View {
	rows := s.List()
	views := make([]View, 0, len(rows))
	for _, vm := range rows {
		view := View{
			Source:       vm.Source,
			Kind:         vm.Kind(),
			Targets:      vm.Targets,
			Strategy:     vm.Strategy,
			ProviderName: vm.ProviderName,
			Model:        vm.Model,
			UserPaths:    vm.UserPaths,
			Description:  vm.Description,
			Enabled:      vm.Enabled,
			CreatedAt:    vm.CreatedAt,
			UpdatedAt:    vm.UpdatedAt,
		}
		if vm.IsRedirect() {
			if selector, err := vm.targetSelector(); err == nil {
				view.ResolvedModel = selector.QualifiedModel()
				view.ProviderType = strings.TrimSpace(s.catalog.GetProviderType(view.ResolvedModel))
				view.Valid = s.catalog.Supports(view.ResolvedModel)
			}
		} else {
			view.ScopeKind = string(scopeKindFor(vm.Source, vm.ProviderName, vm.Model))
		}
		views = append(views, view)
	}
	return views
}

// Upsert validates and stores one virtual model, then refreshes the in-memory
// snapshot with rollback on refresh failure.
func (s *Service) Upsert(ctx context.Context, vm VirtualModel) error {
	if s == nil {
		return fmt.Errorf("virtual models service is required")
	}

	normalized, err := s.normalizeForUpsert(vm)
	if err != nil {
		return err
	}

	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()

	current := s.snapshot()
	if err := s.ensureSourceKind(current, normalized.Source, normalized.IsRedirect()); err != nil {
		return err
	}
	if err := s.validateRedirectTarget(current, normalized); err != nil {
		return err
	}
	if _, err := buildSnapshot(upsertRow(current.rows(), normalized), s.defaultEnabled); err != nil {
		return fmt.Errorf("validate virtual models: %w", err)
	}

	previous, existed := current.bySource[normalized.Source]
	if err := s.store.Upsert(ctx, normalized); err != nil {
		return fmt.Errorf("upsert virtual model: %w", err)
	}
	if err := s.refreshLocked(ctx); err != nil {
		rollbackCtx, cancel := rollbackContext()
		defer cancel()
		var rollbackErr error
		if existed {
			rollbackErr = s.store.Upsert(rollbackCtx, previous)
		} else {
			rollbackErr = s.store.Delete(rollbackCtx, normalized.Source)
		}
		if rollbackErr != nil {
			return fmt.Errorf("refresh virtual models: %w (rollback failed: %v)", err, rollbackErr)
		}
		return fmt.Errorf("refresh virtual models: %w", err)
	}
	return nil
}

// Delete removes one virtual model and refreshes the in-memory snapshot.
func (s *Service) Delete(ctx context.Context, source string) error {
	if s == nil {
		return fmt.Errorf("virtual models service is required")
	}
	source = strings.TrimSpace(source)
	if source == "" {
		return newValidationError("source is required", nil)
	}

	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()

	current := s.snapshot()
	previous, canonical, existed := current.lookupCanonicalSource(source)
	if !existed {
		return ErrNotFound
	}
	source = canonical

	if err := s.store.Delete(ctx, source); err != nil {
		if errors.Is(err, ErrNotFound) {
			return ErrNotFound
		}
		return fmt.Errorf("delete virtual model: %w", err)
	}
	if err := s.refreshLocked(ctx); err != nil {
		rollbackCtx, cancel := rollbackContext()
		defer cancel()
		if rollbackErr := s.store.Upsert(rollbackCtx, previous); rollbackErr != nil {
			return fmt.Errorf("refresh virtual models: %w (rollback failed: %v)", err, rollbackErr)
		}
		return fmt.Errorf("refresh virtual models: %w", err)
	}
	return nil
}

func (s *Service) normalizeForUpsert(vm VirtualModel) (VirtualModel, error) {
	if vm.IsRedirect() {
		normalized, _, err := normalizeRedirect(vm)
		return normalized, err
	}
	return normalizePolicyInput(s.catalog, vm)
}

// ensureSourceKind rejects an upsert that would clobber an existing row of the
// other kind. Source is a single namespace.
func (s *Service) ensureSourceKind(current snapshot, source string, wantRedirect bool) error {
	existing, ok := current.bySource[source]
	if !ok {
		return nil
	}
	if existing.IsRedirect() == wantRedirect {
		return nil
	}
	return crossKindError(source, wantRedirect)
}

// validateRedirectTarget enforces redirect-specific rules: a redirect cannot
// target itself, cannot target another redirect's source, and must resolve to a
// catalog-supported model.
func (s *Service) validateRedirectTarget(current snapshot, vm VirtualModel) error {
	if !vm.IsRedirect() {
		return nil
	}
	target, err := vm.targetSelector()
	if err != nil {
		return newValidationError("invalid target selector: "+err.Error(), err)
	}
	qualified := target.QualifiedModel()
	if vm.Source == qualified {
		return newValidationError(fmt.Sprintf("alias %q cannot target itself", vm.Source), nil)
	}
	if existing, ok := current.redirects[qualified]; ok && existing.vm.Source != vm.Source {
		return newValidationError(fmt.Sprintf("alias target %q refers to another alias", qualified), nil)
	}
	if !s.catalog.Supports(qualified) {
		return newValidationError("target model not found: "+qualified, nil)
	}
	return nil
}

func upsertRow(rows []VirtualModel, next VirtualModel) []VirtualModel {
	for i := range rows {
		if rows[i].Source == next.Source {
			rows[i] = next.clone()
			return rows
		}
	}
	return append(rows, next.clone())
}

func rollbackContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 30*time.Second)
}

// Compile-time check that *Service satisfies the resolver, user-path resolver,
// refresh-target, exposed-model lister, and authorizer seams its consumers
// (gateway, server, batch) depend on, so a signature drift fails to compile here.
var _ interface {
	ResolveModel(core.RequestedModelSelector) (core.ModelSelector, bool, error)
	ResolveModelForUserPath(context.Context, core.RequestedModelSelector) (core.ModelSelector, bool, error)
	ResolveRefreshTarget(core.RequestedModelSelector) (core.ModelSelector, bool, error)
	ExposedModels() []core.Model
	ExposedModelsFiltered(func(core.ModelSelector) bool) []core.Model
	ExposedModelsForUserPath(string, func(core.ModelSelector) bool) []core.Model
	ValidateModelAccess(context.Context, core.ModelSelector) error
	AllowsModel(context.Context, core.ModelSelector) bool
	FilterPublicModels(context.Context, []core.Model) []core.Model
} = (*Service)(nil)
