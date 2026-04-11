package modeloverrides

import (
	"context"
	"fmt"
	"log/slog"
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
	global        compiledOverride
	hasGlobal     bool
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
		global:        compiledOverride{},
		hasGlobal:     false,
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
	return s.refreshLocked(ctx)
}

func (s *Service) refreshLocked(ctx context.Context) error {
	overrides, err := s.store.List(ctx)
	if err != nil {
		return fmt.Errorf("list model overrides: %w", err)
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
		return snapshot{
			order:         []string{},
			bySelector:    map[string]Override{},
			global:        compiledOverride{},
			hasGlobal:     false,
			modelWide:     map[string]compiledOverride{},
			providerWide:  map[string]compiledOverride{},
			exact:         map[string]compiledOverride{},
			defaultEnable: true,
		}
	}
	return s.current.Load().(snapshot)
}

func (s *Service) buildSnapshot(overrides []Override) (snapshot, error) {
	next := snapshot{
		order:         make([]string, 0, len(overrides)),
		bySelector:    make(map[string]Override, len(overrides)),
		global:        compiledOverride{},
		hasGlobal:     false,
		modelWide:     make(map[string]compiledOverride),
		providerWide:  make(map[string]compiledOverride),
		exact:         make(map[string]compiledOverride),
		defaultEnable: s.defaultEnabled,
	}

	for _, override := range overrides {
		normalized, err := normalizeStoredOverride(override)
		if err != nil {
			return snapshot{}, fmt.Errorf("load model override %q: %w", override.Selector, err)
		}
		next.order = append(next.order, normalized.Selector)
		next.bySelector[normalized.Selector] = normalized

		compiled := compiledOverride{override: normalized}
		switch normalized.ScopeKind() {
		case ScopeGlobal:
			next.global = compiled
			next.hasGlobal = true
		case ScopeProviderModel:
			next.exact[exactMatchKey(normalized.ProviderName, normalized.Model)] = compiled
		case ScopeProvider:
			next.providerWide[normalized.ProviderName] = compiled
		default:
			next.modelWide[normalized.Model] = compiled
		}
	}
	sort.Strings(next.order)
	return next, nil
}

func cloneOverride(override Override) Override {
	override.UserPaths = append([]string(nil), override.UserPaths...)
	return override
}

func snapshotOverrides(snap snapshot) []Override {
	result := make([]Override, 0, len(snap.order))
	for _, selector := range snap.order {
		result = append(result, cloneOverride(snap.bySelector[selector]))
	}
	return result
}

func upsertOverride(overrides []Override, next Override) []Override {
	for i := range overrides {
		if overrides[i].Selector == next.Selector {
			overrides[i] = cloneOverride(next)
			return overrides
		}
	}
	return append(overrides, cloneOverride(next))
}

func deleteOverride(overrides []Override, selector string) []Override {
	result := make([]Override, 0, len(overrides))
	for _, override := range overrides {
		if override.Selector == selector {
			continue
		}
		result = append(result, cloneOverride(override))
	}
	return result
}

func rollbackContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 30*time.Second)
}

// List returns all cached overrides sorted by selector.
func (s *Service) List() []Override {
	snap := s.snapshot()
	result := make([]Override, 0, len(snap.order))
	for _, selector := range snap.order {
		override := snap.bySelector[selector]
		override.UserPaths = append([]string(nil), override.UserPaths...)
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
	override.UserPaths = append([]string(nil), override.UserPaths...)
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

	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()

	current := s.snapshot()
	if _, err := s.buildSnapshot(upsertOverride(snapshotOverrides(current), normalized)); err != nil {
		return fmt.Errorf("validate model overrides: %w", err)
	}
	previous, existed := current.bySelector[normalized.Selector]
	if err := s.store.Upsert(ctx, normalized); err != nil {
		return fmt.Errorf("upsert model override: %w", err)
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
			return fmt.Errorf("refresh model overrides: %w (rollback failed: %v)", err, rollbackErr)
		}
		return fmt.Errorf("refresh model overrides: %w", err)
	}
	return nil
}

// Delete removes one override and refreshes the in-memory snapshot.
func (s *Service) Delete(ctx context.Context, selector string) error {
	if s == nil {
		return fmt.Errorf("model override service is required")
	}

	normalized, _, _, err := normalizeSelectorInput(selectorProviderNames(s.catalog), selector)
	if err != nil {
		return err
	}

	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()

	current := s.snapshot()
	if _, err := s.buildSnapshot(deleteOverride(snapshotOverrides(current), normalized)); err != nil {
		return fmt.Errorf("validate model overrides: %w", err)
	}
	previous, existed := current.bySelector[normalized]
	if err := s.store.Delete(ctx, normalized); err != nil {
		return fmt.Errorf("delete model override: %w", err)
	}
	if err := s.refreshLocked(ctx); err != nil {
		if !existed {
			return fmt.Errorf("refresh model overrides: %w", err)
		}

		rollbackCtx, cancel := rollbackContext()
		defer cancel()
		if rollbackErr := s.store.Upsert(rollbackCtx, previous); rollbackErr != nil {
			return fmt.Errorf("refresh model overrides: %w (rollback failed: %v)", err, rollbackErr)
		}
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
				if err := s.Refresh(refreshCtx); err != nil {
					slog.Error("failed to refresh model overrides", "error", err)
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
	if len(state.UserPaths) == 0 {
		return true
	}
	return userPathAllowed(core.UserPathFromContext(ctx), state.UserPaths)
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
	if len(state.UserPaths) == 0 {
		return nil
	}
	if userPathAllowed(core.UserPathFromContext(ctx), state.UserPaths) {
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

	if rule, ok := snap.matchingOverride(providerName, model); ok {
		state.Enabled = true
		state.UserPaths = append([]string(nil), rule.override.UserPaths...)
	}

	return state
}

func (snap snapshot) matchingOverride(providerName, model string) (compiledOverride, bool) {
	if key := exactMatchKey(providerName, model); key != "" {
		if exact, ok := snap.exact[key]; ok {
			return exact, true
		}
	}
	if providerName != "" {
		if providerWide, ok := snap.providerWide[providerName]; ok {
			return providerWide, true
		}
	}
	if model != "" {
		if modelWide, ok := snap.modelWide[model]; ok {
			return modelWide, true
		}
	}
	if snap.hasGlobal {
		return snap.global, true
	}
	return compiledOverride{}, false
}

func userPathAllowed(userPath string, allowed []string) bool {
	if len(allowed) == 0 {
		return true
	}
	if _, ok := slices.BinarySearch(allowed, "/"); ok {
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
