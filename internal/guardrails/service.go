package guardrails

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"sync"

	"gomodel/internal/core"
)

type serviceSnapshot struct {
	definitions map[string]Definition
	order       []string
	registry    *Registry
}

// Service keeps reusable guardrails cached in memory and refreshes them from storage.
type Service struct {
	store    Store
	executor ChatCompletionExecutor

	refreshMu sync.Mutex
	mu        sync.RWMutex
	snapshot  serviceSnapshot
}

// NewService creates a guardrail service backed by the provided store.
func NewService(store Store, executors ...ChatCompletionExecutor) (*Service, error) {
	if store == nil {
		return nil, fmt.Errorf("store is required")
	}
	var executor ChatCompletionExecutor
	if len(executors) > 0 {
		executor = executors[0]
	}
	return &Service{
		store:    store,
		executor: executor,
		snapshot: serviceSnapshot{
			definitions: map[string]Definition{},
			order:       []string{},
			registry:    NewRegistry(),
		},
	}, nil
}

// Refresh reloads guardrails from storage and atomically swaps the in-memory snapshot.
func (s *Service) Refresh(ctx context.Context) error {
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()

	return s.refreshLocked(ctx)
}

// SetExecutor swaps the auxiliary chat executor used by llm_based_altering
// guardrails and rebuilds the in-memory snapshot atomically.
func (s *Service) SetExecutor(ctx context.Context, executor ChatCompletionExecutor) error {
	if s == nil {
		return nil
	}

	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()

	definitions, err := s.store.List(ctx)
	if err != nil {
		return guardrailServiceError("list guardrails", err)
	}
	next, err := buildSnapshot(definitions, executor)
	if err != nil {
		return guardrailServiceError("load guardrails", err)
	}

	s.mu.Lock()
	s.executor = executor
	s.snapshot = next
	s.mu.Unlock()
	return nil
}

func (s *Service) refreshLocked(ctx context.Context) error {
	definitions, err := s.store.List(ctx)
	if err != nil {
		return guardrailServiceError("list guardrails", err)
	}
	next, err := buildSnapshot(definitions, s.executor)
	if err != nil {
		return guardrailServiceError("load guardrails", err)
	}

	s.mu.Lock()
	s.snapshot = next
	s.mu.Unlock()
	return nil
}

// UpsertDefinitions validates and upserts a definition set, then swaps the snapshot on success.
func (s *Service) UpsertDefinitions(ctx context.Context, definitions []Definition) error {
	if s == nil || len(definitions) == 0 {
		return nil
	}

	normalized := make([]Definition, 0, len(definitions))
	for _, definition := range definitions {
		normalizedDefinition, err := normalizeDefinition(definition)
		if err != nil {
			return err
		}
		normalized = append(normalized, normalizedDefinition)
	}

	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()

	currentDefinitions, err := s.store.List(ctx)
	if err != nil {
		return guardrailServiceError("list guardrails", err)
	}
	nextDefinitions := definitionMap(currentDefinitions)
	for _, definition := range normalized {
		nextDefinitions[definition.Name] = definition
	}
	next, err := buildSnapshot(definitionsFromMap(nextDefinitions), s.executor)
	if err != nil {
		return err
	}
	if err := s.store.UpsertMany(ctx, normalized); err != nil {
		return guardrailServiceError("upsert guardrails", err)
	}
	s.mu.Lock()
	s.snapshot = next
	s.mu.Unlock()
	return nil
}

// List returns all cached guardrail definitions sorted by name.
func (s *Service) List() []Definition {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]Definition, 0, len(s.snapshot.order))
	for _, name := range s.snapshot.order {
		result = append(result, cloneDefinition(s.snapshot.definitions[name]))
	}
	return result
}

// ListViews returns all cached guardrail definitions with lightweight summaries.
func (s *Service) ListViews() []View {
	definitions := s.List()
	views := make([]View, 0, len(definitions))
	for _, definition := range definitions {
		views = append(views, ViewFromDefinition(definition))
	}
	return views
}

// Get returns one cached guardrail by name.
func (s *Service) Get(name string) (*Definition, bool) {
	name = normalizeDefinitionName(name)
	if name == "" {
		return nil, false
	}

	s.mu.RLock()
	defer s.mu.RUnlock()
	definition, ok := s.snapshot.definitions[name]
	if !ok {
		return nil, false
	}
	copy := cloneDefinition(definition)
	return &copy, true
}

// Upsert validates and stores a guardrail definition, then swaps the snapshot on success.
func (s *Service) Upsert(ctx context.Context, definition Definition) error {
	normalized, err := normalizeDefinition(definition)
	if err != nil {
		return err
	}

	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()

	currentDefinitions, err := s.store.List(ctx)
	if err != nil {
		return guardrailServiceError("list guardrails", err)
	}
	nextDefinitions := definitionMap(currentDefinitions)
	nextDefinitions[normalized.Name] = normalized
	next, err := buildSnapshot(definitionsFromMap(nextDefinitions), s.executor)
	if err != nil {
		return err
	}
	if err := s.store.Upsert(ctx, normalized); err != nil {
		return guardrailServiceError("upsert guardrail", err)
	}
	s.mu.Lock()
	s.snapshot = next
	s.mu.Unlock()
	return nil
}

// Delete removes a guardrail definition from storage and swaps the snapshot on success.
func (s *Service) Delete(ctx context.Context, name string) error {
	name = normalizeDefinitionName(name)
	if name == "" {
		return newValidationError("guardrail name is required", nil)
	}

	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()

	currentDefinitions, err := s.store.List(ctx)
	if err != nil {
		return guardrailServiceError("list guardrails", err)
	}
	nextDefinitions := definitionMap(currentDefinitions)
	delete(nextDefinitions, name)
	next, err := buildSnapshot(definitionsFromMap(nextDefinitions), s.executor)
	if err != nil {
		return err
	}
	if err := s.store.Delete(ctx, name); err != nil {
		return guardrailServiceError("delete guardrail", err)
	}
	s.mu.Lock()
	s.snapshot = next
	s.mu.Unlock()
	return nil
}

// TypeDefinitions returns the supported guardrail type schemas.
func (s *Service) TypeDefinitions() []TypeDefinition {
	return TypeDefinitions()
}

// Len returns the number of loaded guardrails.
func (s *Service) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.snapshot.order)
}

// Names returns the loaded guardrail names in sorted order.
func (s *Service) Names() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]string(nil), s.snapshot.order...)
}

// BuildPipeline resolves named steps through the current in-memory guardrail registry.
func (s *Service) BuildPipeline(steps []StepReference) (*Pipeline, string, error) {
	if len(steps) == 0 {
		return nil, "", nil
	}

	s.mu.RLock()
	registry := s.snapshot.registry
	s.mu.RUnlock()
	if registry == nil {
		return nil, "", core.NewProviderError("", http.StatusBadGateway, "guardrail catalog is not loaded", nil)
	}
	return registry.BuildPipeline(steps)
}

func buildSnapshot(definitions []Definition, executor ChatCompletionExecutor) (serviceSnapshot, error) {
	next := serviceSnapshot{
		definitions: make(map[string]Definition, len(definitions)),
		order:       make([]string, 0, len(definitions)),
		registry:    NewRegistry(),
	}
	for _, definition := range definitions {
		normalized, err := normalizeDefinition(definition)
		if err != nil {
			return serviceSnapshot{}, fmt.Errorf("load guardrail %q: %w", definition.Name, err)
		}
		instance, descriptor, err := buildDefinition(normalized, executor)
		if err != nil {
			return serviceSnapshot{}, fmt.Errorf("load guardrail %q: %w", normalized.Name, err)
		}
		if err := next.registry.Register(instance, descriptor); err != nil {
			return serviceSnapshot{}, fmt.Errorf("register guardrail %q: %w", normalized.Name, err)
		}
		next.definitions[normalized.Name] = normalized
		next.order = append(next.order, normalized.Name)
	}
	sort.Strings(next.order)
	return next, nil
}

func definitionMap(definitions []Definition) map[string]Definition {
	next := make(map[string]Definition, len(definitions))
	for _, definition := range definitions {
		next[definition.Name] = cloneDefinition(definition)
	}
	return next
}

func definitionsFromMap(definitions map[string]Definition) []Definition {
	result := make([]Definition, 0, len(definitions))
	for _, definition := range definitions {
		result = append(result, definition)
	}
	return result
}

func guardrailServiceError(message string, err error) error {
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
