package modeloverrides

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"gomodel/internal/core"
)

type compiledOverride struct {
	override Override
}

type snapshot struct {
	order         []string
	bySelector    map[string]Override
	modelWide     map[string]compiledOverride
	providerWide  map[string]compiledOverride
	exact         map[string]compiledOverride
	defaultEnable bool
}

// Service keeps model access overrides cached in memory.
type Service struct {
	store          Store
	catalog        Catalog
	defaultEnabled bool
	current        atomic.Value
	refreshMu      sync.Mutex
}

// NewService creates a model override service backed by storage.
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
	service.current.Store(snapshot{
		order:         []string{},
		bySelector:    map[string]Override{},
		modelWide:     map[string]compiledOverride{},
		providerWide:  map[string]compiledOverride{},
		exact:         map[string]compiledOverride{},
		defaultEnable: defaultEnabled,
	})
	return service, nil
}

// EnabledByDefault reports the process-wide model availability default.
func (s *Service) EnabledByDefault() bool {
	if s == nil {
		return true
	}
	return s.defaultEnabled
}

// Refresh reloads overrides from storage and atomically swaps the snapshot.
func (s *Service) Refresh(ctx context.Context) error {
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()

	overrides, err := s.store.List(ctx)
	if err != nil {
		return fmt.Errorf("list model overrides: %w", err)
	}

	next := snapshot{
		order:         make([]string, 0, len(overrides)),
		bySelector:    make(map[string]Override, len(overrides)),
		modelWide:     make(map[string]compiledOverride),
		providerWide:  make(map[string]compiledOverride),
		exact:         make(map[string]compiledOverride),
		defaultEnable: s.defaultEnabled,
	}

	for _, override := range overrides {
		normalized, err := normalizeStoredOverride(override)
		if err != nil {
			return fmt.Errorf("load model override %q: %w", override.Selector, err)
		}
		next.order = append(next.order, normalized.Selector)
		next.bySelector[normalized.Selector] = normalized

		compiled := compiledOverride{override: normalized}
		switch normalized.ScopeKind() {
		case ScopeProviderModel:
			next.exact[exactMatchKey(normalized.ProviderName, normalized.Model)] = compiled
		case ScopeProvider:
			next.providerWide[normalized.ProviderName] = compiled
		default:
			next.modelWide[normalized.Model] = compiled
		}
	}
	sort.Strings(next.order)
	s.current.Store(next)
	return nil
}

func (s *Service) snapshot() snapshot {
	if s == nil {
		return snapshot{
			order:         []string{},
			bySelector:    map[string]Override{},
			modelWide:     map[string]compiledOverride{},
			providerWide:  map[string]compiledOverride{},
			exact:         map[string]compiledOverride{},
			defaultEnable: true,
		}
	}
	return s.current.Load().(snapshot)
}

// List returns all cached overrides sorted by selector.
func (s *Service) List() []Override {
	snap := s.snapshot()
	result := make([]Override, 0, len(snap.order))
	for _, selector := range snap.order {
		override := snap.bySelector[selector]
		override.Enabled = cloneEnabled(override.Enabled)
		override.AllowedOnlyForUserPaths = append([]string(nil), override.AllowedOnlyForUserPaths...)
		result = append(result, override)
	}
	return result
}

// ListViews returns all cached overrides with scope metadata.
func (s *Service) ListViews() []View {
	overrides := s.List()
	result := make([]View, 0, len(overrides))
	for _, override := range overrides {
		result = append(result, View{
			Override:  override,
			ScopeKind: override.ScopeKind(),
		})
	}
	return result
}

// Get returns one cached override by normalized selector.
func (s *Service) Get(selector string) (*Override, bool) {
	normalized, _, _, err := normalizeSelectorInput(selectorProviderNames(s.catalog), selector)
	if err != nil {
		return nil, false
	}
	override, ok := s.snapshot().bySelector[normalized]
	if !ok {
		return nil, false
	}
	override.Enabled = cloneEnabled(override.Enabled)
	override.AllowedOnlyForUserPaths = append([]string(nil), override.AllowedOnlyForUserPaths...)
	return &override, true
}

// Upsert validates and stores one override, then refreshes the in-memory snapshot.
func (s *Service) Upsert(ctx context.Context, override Override) error {
	if s == nil {
		return fmt.Errorf("model override service is required")
	}

	normalized, err := normalizeOverrideInput(s.catalog, override)
	if err != nil {
		return err
	}
	if normalized.Enabled == nil && !normalized.ForceDisabled && len(normalized.AllowedOnlyForUserPaths) == 0 {
		return newValidationError("override must enable, force disable, or set allowed_only_for_user_paths", nil)
	}
	if normalized.Enabled != nil && !*normalized.Enabled {
		return newValidationError("enabled=false is not supported; use force_disabled=true or omit enabled", nil)
	}
	if err := s.store.Upsert(ctx, normalized); err != nil {
		return fmt.Errorf("upsert model override: %w", err)
	}
	if err := s.Refresh(ctx); err != nil {
		return fmt.Errorf("refresh model overrides: %w", err)
	}
	return nil
}

// Delete removes one override and refreshes the in-memory snapshot.
func (s *Service) Delete(ctx context.Context, selector string) error {
	normalized, _, _, err := normalizeSelectorInput(selectorProviderNames(s.catalog), selector)
	if err != nil {
		return err
	}
	if err := s.store.Delete(ctx, normalized); err != nil {
		return fmt.Errorf("delete model override: %w", err)
	}
	if err := s.Refresh(ctx); err != nil {
		return fmt.Errorf("refresh model overrides: %w", err)
	}
	return nil
}

// StartBackgroundRefresh periodically reloads model overrides from storage until stopped.
func (s *Service) StartBackgroundRefresh(interval time.Duration) func() {
	if interval <= 0 {
		interval = time.Minute
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
				_ = s.Refresh(refreshCtx)
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

// EffectiveState resolves the compiled access state for one concrete selector.
func (s *Service) EffectiveState(selector core.ModelSelector) EffectiveState {
	return s.snapshot().effectiveState(selector)
}

// AllowsModel reports whether selector is available for the effective request user path.
func (s *Service) AllowsModel(ctx context.Context, selector core.ModelSelector) bool {
	state := s.EffectiveState(selector)
	if !state.Enabled {
		return false
	}
	if len(state.AllowedOnlyForUserPaths) == 0 {
		return true
	}
	return userPathAllowed(core.UserPathFromContext(ctx), state.AllowedOnlyForUserPaths)
}

// ValidateModelAccess returns a typed request error when selector is not available.
func (s *Service) ValidateModelAccess(ctx context.Context, selector core.ModelSelector) error {
	state := s.EffectiveState(selector)
	if !state.Enabled {
		return core.NewInvalidRequestErrorWithStatus(
			http.StatusBadRequest,
			"requested model is not available",
			nil,
		).WithCode("model_access_denied")
	}
	if len(state.AllowedOnlyForUserPaths) == 0 {
		return nil
	}
	if userPathAllowed(core.UserPathFromContext(ctx), state.AllowedOnlyForUserPaths) {
		return nil
	}
	return core.NewInvalidRequestErrorWithStatus(
		http.StatusBadRequest,
		"requested model is not available for this API key",
		nil,
	).WithCode("model_access_denied")
}

// FilterPublicModels removes models that are unavailable for the effective request user path.
func (s *Service) FilterPublicModels(ctx context.Context, models []core.Model) []core.Model {
	if s == nil || len(models) == 0 {
		return models
	}

	result := make([]core.Model, 0, len(models))
	for _, model := range models {
		selector, err := core.ParseModelSelector(model.ID, "")
		if err != nil {
			continue
		}
		if !s.AllowsModel(ctx, selector) {
			continue
		}
		result = append(result, model)
	}
	return result
}

func (snap snapshot) effectiveState(selector core.ModelSelector) EffectiveState {
	model := strings.TrimSpace(selector.Model)
	providerName := strings.TrimSpace(selector.Provider)
	state := EffectiveState{
		Selector:       selectorString(providerName, model),
		ProviderName:   providerName,
		Model:          model,
		DefaultEnabled: snap.defaultEnable,
		Enabled:        snap.defaultEnable,
	}
	if model == "" && providerName == "" {
		return state
	}

	allowed := make([]string, 0)
	seenAllowed := make(map[string]struct{})
	addAllowed := func(paths []string) {
		for _, path := range paths {
			if _, exists := seenAllowed[path]; exists {
				continue
			}
			seenAllowed[path] = struct{}{}
			allowed = append(allowed, path)
		}
	}
	apply := func(rule compiledOverride, ok bool) {
		if !ok {
			return
		}
		if rule.override.Enabled != nil && *rule.override.Enabled {
			state.Enabled = true
		}
		if rule.override.ForceDisabled {
			state.Enabled = false
			state.ForceDisabled = true
		}
		addAllowed(rule.override.AllowedOnlyForUserPaths)
	}

	if modelWide, ok := snap.modelWide[model]; ok {
		apply(modelWide, true)
	}
	if providerWide, ok := snap.providerWide[providerName]; ok {
		apply(providerWide, true)
	}
	if exact, ok := snap.exact[exactMatchKey(providerName, model)]; ok {
		apply(exact, true)
	}

	sort.Strings(allowed)
	state.AllowedOnlyForUserPaths = allowed
	return state
}

func userPathAllowed(userPath string, allowed []string) bool {
	if len(allowed) == 0 {
		return true
	}
	userPath, err := core.NormalizeUserPath(userPath)
	if err != nil || userPath == "" {
		return false
	}
	ancestors := core.UserPathAncestors(userPath)
	for _, candidate := range ancestors {
		if _, ok := slices.BinarySearch(allowed, candidate); ok {
			return true
		}
	}
	return false
}
