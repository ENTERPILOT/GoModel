package headerpolicy

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/enterpilot/gomodel/internal/core"
)

type snapshot struct {
	definitions map[string]Definition
	order       []string
	registry    *Registry
}

// Service owns persisted definitions and their compiled in-memory catalog.
type Service struct {
	store Store

	refreshMu sync.Mutex
	mu        sync.RWMutex
	snapshot  snapshot
}

// NewService creates an empty service over store. Call Refresh before use.
func NewService(store Store) (*Service, error) {
	if store == nil {
		return nil, fmt.Errorf("store is required")
	}
	return &Service{store: store, snapshot: snapshot{
		definitions: map[string]Definition{}, order: []string{}, registry: newRegistry(),
	}}, nil
}

// Refresh atomically reloads definitions from persistence.
func (s *Service) Refresh(ctx context.Context) error {
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()
	definitions, err := s.store.List(ctx)
	if err != nil {
		return serviceError("list header policies", err)
	}
	next, err := buildSnapshot(definitions)
	if err != nil {
		return serviceError("load header policies", err)
	}
	s.mu.Lock()
	s.snapshot = next
	s.mu.Unlock()
	return nil
}

// StartBackgroundRefresh keeps the in-memory catalog synchronized across instances.
func (s *Service) StartBackgroundRefresh(parent context.Context, interval time.Duration) func() {
	if parent == nil || s == nil {
		return func() {}
	}
	if interval <= 0 {
		interval = time.Minute
	}
	ctx, cancel := context.WithCancel(parent)
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
				refreshCtx, refreshCancel := context.WithTimeout(ctx, 30*time.Second)
				_ = s.Refresh(refreshCtx)
				refreshCancel()
			}
		}
	}()
	var once sync.Once
	return func() { once.Do(func() { cancel(); <-done }) }
}

// UpsertDefinitions validates and persists definitions, then publishes the
// already-compiled prospective snapshot. A successful write is never followed
// by a second fallible store read.
func (s *Service) UpsertDefinitions(ctx context.Context, definitions []Definition) error {
	if s == nil || len(definitions) == 0 {
		return nil
	}
	normalized := make([]Definition, 0, len(definitions))
	seen := make(map[string]struct{}, len(definitions))
	for _, definition := range definitions {
		def, err := normalizeDefinition(definition)
		if err != nil {
			return err
		}
		if _, duplicate := seen[def.Name]; duplicate {
			return newValidationError("duplicate header policy name: "+def.Name, nil)
		}
		seen[def.Name] = struct{}{}
		normalized = append(normalized, def)
	}
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()

	current, err := s.store.List(ctx)
	if err != nil {
		return serviceError("list header policies", err)
	}
	nextDefinitions := definitionsByName(current)
	normalized = prepareUpserts(nextDefinitions, normalized, time.Now().UTC())
	for _, definition := range normalized {
		nextDefinitions[definition.Name] = definition
	}
	next, err := buildSnapshot(definitionsFromMap(nextDefinitions))
	if err != nil {
		return serviceError("load header policies", err)
	}
	if err := s.store.UpsertMany(ctx, normalized); err != nil {
		return serviceError("upsert header policies", err)
	}
	s.publish(next)
	return nil
}

// Upsert validates and stores one definition.
func (s *Service) Upsert(ctx context.Context, definition Definition) error {
	def, err := normalizeDefinition(definition)
	if err != nil {
		return err
	}
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()

	current, err := s.store.List(ctx)
	if err != nil {
		return serviceError("list header policies", err)
	}
	nextDefinitions := definitionsByName(current)
	def = prepareUpserts(nextDefinitions, []Definition{def}, time.Now().UTC())[0]
	nextDefinitions[def.Name] = def
	next, err := buildSnapshot(definitionsFromMap(nextDefinitions))
	if err != nil {
		return serviceError("load header policies", err)
	}
	if err := s.store.Upsert(ctx, def); err != nil {
		return serviceError("upsert header policy", err)
	}
	s.publish(next)
	return nil
}

// Delete removes one definition.
func (s *Service) Delete(ctx context.Context, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return newValidationError("header policy name is required", nil)
	}
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()

	current, err := s.store.List(ctx)
	if err != nil {
		return serviceError("list header policies", err)
	}
	nextDefinitions := definitionsByName(current)
	delete(nextDefinitions, name)
	next, err := buildSnapshot(definitionsFromMap(nextDefinitions))
	if err != nil {
		return serviceError("load header policies", err)
	}
	if err := s.store.Delete(ctx, name); err != nil {
		if errors.Is(err, ErrNotFound) {
			return ErrNotFound
		}
		return serviceError("delete header policy", err)
	}
	s.publish(next)
	return nil
}

// List returns cloned definitions sorted by name.
func (s *Service) List() []Definition {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]Definition, 0, len(s.snapshot.order))
	for _, name := range s.snapshot.order {
		result = append(result, cloneDefinition(s.snapshot.definitions[name]))
	}
	return result
}

// ListViews returns admin projections sorted by name.
func (s *Service) ListViews() []View {
	definitions := s.List()
	views := make([]View, 0, len(definitions))
	for _, definition := range definitions {
		views = append(views, ViewFromDefinition(definition))
	}
	return views
}

// Get returns a cloned definition.
func (s *Service) Get(name string) (*Definition, bool) {
	name = strings.TrimSpace(name)
	s.mu.RLock()
	defer s.mu.RUnlock()
	definition, ok := s.snapshot.definitions[name]
	if !ok {
		return nil, false
	}
	copy := cloneDefinition(definition)
	return &copy, true
}

// Names implements Catalog.
func (s *Service) Names() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]string(nil), s.snapshot.order...)
}

// BuildHeaderPolicies implements Catalog.
func (s *Service) BuildHeaderPolicies(steps []Reference) ([]core.HeaderPolicy, error) {
	if len(steps) == 0 {
		return nil, nil
	}
	s.mu.RLock()
	registry := s.snapshot.registry
	s.mu.RUnlock()
	return registry.BuildHeaderPolicies(steps)
}

func buildSnapshot(definitions []Definition) (snapshot, error) {
	next := snapshot{definitions: make(map[string]Definition, len(definitions)), order: make([]string, 0, len(definitions)), registry: newRegistry()}
	for _, definition := range definitions {
		def, err := normalizeDefinition(definition)
		if err != nil {
			return snapshot{}, fmt.Errorf("load header policy %q: %w", definition.Name, err)
		}
		if err := next.registry.register(def); err != nil {
			return snapshot{}, err
		}
		next.definitions[def.Name] = def
		next.order = append(next.order, def.Name)
	}
	sort.Strings(next.order)
	return next, nil
}

func (s *Service) publish(next snapshot) {
	s.mu.Lock()
	s.snapshot = next
	s.mu.Unlock()
}

func definitionsByName(definitions []Definition) map[string]Definition {
	result := make(map[string]Definition, len(definitions))
	for _, definition := range definitions {
		result[definition.Name] = cloneDefinition(definition)
	}
	return result
}

func definitionsFromMap(definitions map[string]Definition) []Definition {
	result := make([]Definition, 0, len(definitions))
	for _, definition := range definitions {
		result = append(result, definition)
	}
	return result
}

func prepareUpserts(current map[string]Definition, definitions []Definition, now time.Time) []Definition {
	result := make([]Definition, 0, len(definitions))
	for _, definition := range definitions {
		if existing, ok := current[definition.Name]; ok && !existing.CreatedAt.IsZero() {
			definition.CreatedAt = existing.CreatedAt
		} else {
			definition.CreatedAt = now
		}
		definition.UpdatedAt = now
		result = append(result, definition)
	}
	return result
}

func serviceError(message string, err error) error {
	if err == nil {
		return nil
	}
	if gatewayErr, ok := errors.AsType[*core.GatewayError](err); ok {
		return gatewayErr
	}
	if IsValidationError(err) {
		return core.NewInvalidRequestError(message+": "+err.Error(), err)
	}
	return core.NewProviderError("", http.StatusBadGateway, message+": "+err.Error(), err)
}
