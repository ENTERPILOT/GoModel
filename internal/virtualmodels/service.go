package virtualmodels

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"

	"gomodel/internal/aliases"
	"gomodel/internal/modeloverrides"
	"gomodel/internal/validation"
)

// Service unifies the alias (redirect) and access-override (policy) engines
// behind one virtual_models store. It exposes the two engines for wiring into
// the existing resolver/authorizer seams, plus unified CRUD for the admin API.
//
// The redirect (alias) engine is always present. The policy (access-override)
// engine is built only when overrides are enabled; otherwise overrides is nil,
// matching the historical behavior where the subsystem was not initialized.
type Service struct {
	store     Store
	aliases   *aliases.Service
	overrides *modeloverrides.Service
}

// NewService composes the alias and access-override services over a single
// virtual_models store via role-partitioned adapters. When overridesEnabled is
// false the access-override engine is not built (and Overrides() returns nil).
func NewService(store Store, catalog Catalog, defaultEnabled, overridesEnabled bool) (*Service, error) {
	if store == nil {
		return nil, fmt.Errorf("store is required")
	}
	if catalog == nil {
		return nil, fmt.Errorf("catalog is required")
	}
	// Shared so cross-kind check-then-write upserts/deletes serialize across the
	// two engines' independent locks.
	writeMu := &sync.Mutex{}

	aliasSvc, err := aliases.NewService(redirectStore{store: store, writeMu: writeMu}, catalog)
	if err != nil {
		return nil, fmt.Errorf("create alias engine: %w", err)
	}
	svc := &Service{store: store, aliases: aliasSvc}

	if overridesEnabled {
		overrideSvc, err := modeloverrides.NewService(policyStore{store: store, writeMu: writeMu}, catalog, defaultEnabled)
		if err != nil {
			return nil, fmt.Errorf("create access-override engine: %w", err)
		}
		svc.overrides = overrideSvc
	}
	return svc, nil
}

// Aliases returns the composed alias engine (the gateway ModelResolver and
// ExposedModelLister).
func (s *Service) Aliases() *aliases.Service {
	if s == nil {
		return nil
	}
	return s.aliases
}

// Overrides returns the composed access-override engine (the gateway
// ModelAuthorizer).
func (s *Service) Overrides() *modeloverrides.Service {
	if s == nil {
		return nil
	}
	return s.overrides
}

// Refresh reloads the active engines from the shared store.
func (s *Service) Refresh(ctx context.Context) error {
	if err := s.aliases.Refresh(ctx); err != nil {
		return err
	}
	if s.overrides == nil {
		return nil
	}
	return s.overrides.Refresh(ctx)
}

// ListViews returns all virtual models (redirects and policies) for the admin UI.
func (s *Service) ListViews() []View {
	views := make([]View, 0)
	for _, av := range s.aliases.ListViews() {
		views = append(views, View{
			Source:        av.Name,
			Kind:          KindRedirect,
			Targets:       []Target{{Provider: av.TargetProvider, Model: av.TargetModel}},
			Description:   av.Description,
			Enabled:       av.Enabled,
			ResolvedModel: av.ResolvedModel,
			ProviderType:  av.ProviderType,
			Valid:         av.Valid,
			CreatedAt:     av.CreatedAt,
			UpdatedAt:     av.UpdatedAt,
		})
	}
	if s.overrides == nil {
		sort.Slice(views, func(i, j int) bool { return views[i].Source < views[j].Source })
		return views
	}
	for _, ov := range s.overrides.ListViews() {
		views = append(views, View{
			Source:       ov.Selector,
			Kind:         KindPolicy,
			ProviderName: ov.ProviderName,
			Model:        ov.Model,
			UserPaths:    ov.UserPaths,
			Enabled:      true,
			ScopeKind:    string(ov.ScopeKind),
			CreatedAt:    ov.CreatedAt,
			UpdatedAt:    ov.UpdatedAt,
		})
	}
	sort.Slice(views, func(i, j int) bool { return views[i].Source < views[j].Source })
	return views
}

// Upsert routes a virtual model to the right engine based on its kind, reusing
// each engine's validation verbatim.
func (s *Service) Upsert(ctx context.Context, vm VirtualModel) error {
	if vm.IsRedirect() {
		// v1 supports single-target redirects only. Reject multi-target /
		// strategy rather than silently dropping them in toAlias().
		if len(vm.Targets) > 1 || vm.Strategy != "" {
			return validation.NewError("multi-target redirects (load balancing) are not yet supported", nil)
		}
		return s.aliases.Upsert(ctx, vm.toAlias())
	}
	if s.overrides == nil {
		return validation.NewError("access policies are disabled (MODEL_OVERRIDES_ENABLED=false)", nil)
	}
	return s.overrides.Upsert(ctx, vm.toOverride())
}

// Delete removes a virtual model by source, routing to the right engine.
func (s *Service) Delete(ctx context.Context, source string) error {
	vm, err := s.store.Get(ctx, source)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return ErrNotFound
		}
		return err
	}
	if vm.IsRedirect() {
		return s.aliases.Delete(ctx, source)
	}
	if s.overrides == nil {
		return ErrNotFound
	}
	return s.overrides.Delete(ctx, source)
}
