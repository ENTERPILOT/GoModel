package aliases

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"gomodel/internal/core"
)

// Catalog exposes the concrete model catalog used to validate alias targets.
type Catalog interface {
	Supports(model string) bool
	GetProviderType(model string) string
	LookupModel(model string) (*core.Model, bool)
}

type snapshot struct {
	aliases map[string]Alias
	order   []string
}

// Service keeps aliases cached in memory and refreshes them from storage.
type Service struct {
	store   Store
	catalog Catalog

	mu       sync.RWMutex
	snapshot snapshot
}

// NewService creates an alias service backed by the provided store and catalog.
func NewService(store Store, catalog Catalog) (*Service, error) {
	if store == nil {
		return nil, fmt.Errorf("store is required")
	}
	if catalog == nil {
		return nil, fmt.Errorf("catalog is required")
	}
	return &Service{store: store, catalog: catalog}, nil
}

// Refresh reloads aliases from storage and atomically swaps the in-memory snapshot.
func (s *Service) Refresh(ctx context.Context) error {
	aliases, err := s.store.List(ctx)
	if err != nil {
		return fmt.Errorf("list aliases: %w", err)
	}

	next := snapshot{
		aliases: make(map[string]Alias, len(aliases)),
		order:   make([]string, 0, len(aliases)),
	}
	for _, alias := range aliases {
		normalized, err := normalizeAlias(alias)
		if err != nil {
			return fmt.Errorf("load alias %q: %w", alias.Name, err)
		}
		next.aliases[normalized.Name] = normalized
		next.order = append(next.order, normalized.Name)
	}
	sort.Strings(next.order)

	s.mu.Lock()
	s.snapshot = next
	s.mu.Unlock()
	return nil
}

// List returns all cached aliases sorted by name.
func (s *Service) List() []Alias {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]Alias, 0, len(s.snapshot.order))
	for _, name := range s.snapshot.order {
		result = append(result, s.snapshot.aliases[name])
	}
	return result
}

// ListViews returns aliases with current validity derived from the concrete model catalog.
func (s *Service) ListViews() []View {
	aliases := s.List()
	views := make([]View, 0, len(aliases))
	for _, alias := range aliases {
		view := View{Alias: alias}
		selector, err := alias.TargetSelector()
		if err == nil {
			view.ResolvedModel = selector.QualifiedModel()
			view.ProviderType = strings.TrimSpace(s.catalog.GetProviderType(view.ResolvedModel))
			view.Valid = s.catalog.Supports(view.ResolvedModel)
		}
		views = append(views, view)
	}
	return views
}

// Get returns one cached alias by name.
func (s *Service) Get(name string) (*Alias, bool) {
	name = normalizeName(name)
	if name == "" {
		return nil, false
	}

	s.mu.RLock()
	defer s.mu.RUnlock()
	alias, ok := s.snapshot.aliases[name]
	if !ok {
		return nil, false
	}
	copy := alias
	return &copy, true
}

// ResolveSelector resolves a selector through the alias table.
// Explicit provider selection bypasses aliases.
func (s *Service) ResolveSelector(selector core.ModelSelector) (Resolution, bool) {
	resolution := Resolution{Requested: selector, Resolved: selector}
	if strings.TrimSpace(selector.Provider) != "" {
		return resolution, false
	}

	alias, ok := s.Get(selector.Model)
	if !ok || !alias.Enabled {
		return resolution, false
	}

	target, err := alias.TargetSelector()
	if err != nil {
		return resolution, false
	}
	if !s.catalog.Supports(target.QualifiedModel()) {
		return resolution, false
	}

	resolution.Resolved = target
	resolution.Alias = alias
	return resolution, true
}

// Resolve resolves raw model/provider inputs through the alias table.
func (s *Service) Resolve(model, provider string) (Resolution, bool, error) {
	selector, err := core.ParseModelSelector(model, provider)
	if err != nil {
		return Resolution{}, false, err
	}
	resolution, ok := s.ResolveSelector(selector)
	return resolution, ok, nil
}

// Supports reports whether an alias currently resolves to a concrete model.
func (s *Service) Supports(model string) bool {
	selector, err := core.ParseModelSelector(model, "")
	if err != nil {
		return false
	}
	_, ok := s.ResolveSelector(selector)
	return ok
}

// GetProviderType returns the resolved provider type for an alias, or empty if unresolved.
func (s *Service) GetProviderType(model string) string {
	selector, err := core.ParseModelSelector(model, "")
	if err != nil {
		return ""
	}
	resolution, ok := s.ResolveSelector(selector)
	if !ok {
		return ""
	}
	return strings.TrimSpace(s.catalog.GetProviderType(resolution.Resolved.QualifiedModel()))
}

// ExposedModels returns enabled aliases projected as model-list entries.
func (s *Service) ExposedModels() []core.Model {
	aliases := s.List()
	result := make([]core.Model, 0, len(aliases))
	for _, alias := range aliases {
		if !alias.Enabled {
			continue
		}
		selector, err := alias.TargetSelector()
		if err != nil {
			continue
		}
		model, ok := s.catalog.LookupModel(selector.QualifiedModel())
		if !ok || model == nil {
			continue
		}
		cloned := *model
		cloned.ID = alias.Name
		result = append(result, cloned)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

// Upsert validates and stores an alias, then refreshes the in-memory snapshot.
func (s *Service) Upsert(ctx context.Context, alias Alias) error {
	normalized, err := normalizeAlias(alias)
	if err != nil {
		return err
	}
	if err := s.validate(normalized); err != nil {
		return err
	}
	if err := s.store.Upsert(ctx, normalized); err != nil {
		return fmt.Errorf("upsert alias: %w", err)
	}
	if err := s.Refresh(ctx); err != nil {
		return fmt.Errorf("refresh aliases: %w", err)
	}
	return nil
}

// Delete removes an alias from storage and refreshes the in-memory snapshot.
func (s *Service) Delete(ctx context.Context, name string) error {
	name = normalizeName(name)
	if name == "" {
		return newValidationError("alias name is required", nil)
	}
	if err := s.store.Delete(ctx, name); err != nil {
		return fmt.Errorf("delete alias: %w", err)
	}
	if err := s.Refresh(ctx); err != nil {
		return fmt.Errorf("refresh aliases: %w", err)
	}
	return nil
}

func (s *Service) validate(alias Alias) error {
	target, err := alias.TargetSelector()
	if err != nil {
		return newValidationError("invalid target selector: "+err.Error(), err)
	}
	if alias.Name == target.Model && target.Provider == "" {
		return newValidationError(fmt.Sprintf("alias %q cannot target itself", alias.Name), nil)
	}

	s.mu.RLock()
	defer s.mu.RUnlock()
	if target.Provider == "" {
		if existing, ok := s.snapshot.aliases[target.Model]; ok && existing.Name != alias.Name {
			return newValidationError(fmt.Sprintf("alias target %q refers to another alias", target.Model), nil)
		}
	}
	if !s.catalog.Supports(target.QualifiedModel()) {
		return newValidationError("target model not found: "+target.QualifiedModel(), nil)
	}
	return nil
}

// StartBackgroundRefresh periodically reloads aliases from storage until stopped.
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
