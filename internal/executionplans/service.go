package executionplans

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"gomodel/internal/core"
	"gomodel/internal/guardrails"
)

// CompiledPlan is the immutable runtime projection cached in the hot-path snapshot.
type CompiledPlan struct {
	Version  Version
	Policy   *core.ResolvedExecutionPolicy
	Pipeline *guardrails.Pipeline
}

// Compiler turns one persisted execution-plan version into its runtime projection.
type Compiler interface {
	Compile(version Version) (*CompiledPlan, error)
}

type snapshot struct {
	global         *CompiledPlan
	providers      map[string]*CompiledPlan
	providerModels map[string]map[string]*CompiledPlan
	byVersionID    map[string]*CompiledPlan
}

// Service keeps the active execution-plan set cached in memory.
type Service struct {
	store    Store
	compiler Compiler
	current  atomic.Value
}

// NewService creates an execution-plan service backed by storage.
func NewService(store Store, compiler Compiler) (*Service, error) {
	if store == nil {
		return nil, fmt.Errorf("store is required")
	}
	if compiler == nil {
		return nil, fmt.Errorf("compiler is required")
	}

	service := &Service{
		store:    store,
		compiler: compiler,
	}
	service.current.Store(snapshot{
		providers:      map[string]*CompiledPlan{},
		providerModels: map[string]map[string]*CompiledPlan{},
		byVersionID:    map[string]*CompiledPlan{},
	})
	return service, nil
}

// Refresh reloads active plans from storage and atomically swaps the in-memory snapshot.
func (s *Service) Refresh(ctx context.Context) error {
	versions, err := s.store.ListActive(ctx)
	if err != nil {
		return fmt.Errorf("list active execution plans: %w", err)
	}

	next := snapshot{
		providers:      make(map[string]*CompiledPlan),
		providerModels: make(map[string]map[string]*CompiledPlan),
		byVersionID:    make(map[string]*CompiledPlan),
	}

	for _, version := range versions {
		scope, scopeKey, err := normalizeScope(version.Scope)
		if err != nil {
			return fmt.Errorf("load execution plan %q: %w", version.ID, err)
		}
		version.Scope = scope
		version.ScopeKey = scopeKey

		compiled, err := s.compiler.Compile(version)
		if err != nil {
			return fmt.Errorf("compile execution plan %q: %w", version.ID, err)
		}
		if compiled == nil || compiled.Policy == nil {
			return fmt.Errorf("compile execution plan %q: empty compiled plan", version.ID)
		}

		next.byVersionID[compiled.Version.ID] = compiled

		switch {
		case scope.Provider == "":
			if next.global != nil {
				return fmt.Errorf("duplicate active global execution plans: %q and %q", next.global.Version.ID, version.ID)
			}
			next.global = compiled
		case scope.Model == "":
			if existing := next.providers[scope.Provider]; existing != nil {
				return fmt.Errorf("duplicate active provider execution plans for %q: %q and %q", scope.Provider, existing.Version.ID, version.ID)
			}
			next.providers[scope.Provider] = compiled
		default:
			models := next.providerModels[scope.Provider]
			if models == nil {
				models = make(map[string]*CompiledPlan)
				next.providerModels[scope.Provider] = models
			}
			if existing := models[scope.Model]; existing != nil {
				return fmt.Errorf("duplicate active provider-model execution plans for %q/%q: %q and %q", scope.Provider, scope.Model, existing.Version.ID, version.ID)
			}
			models[scope.Model] = compiled
		}
	}

	if next.global == nil {
		return fmt.Errorf("missing active global execution plan")
	}

	s.current.Store(next)
	return nil
}

// EnsureDefaultGlobal seeds one active global execution plan when none exists.
func (s *Service) EnsureDefaultGlobal(ctx context.Context, input CreateInput) error {
	normalized, _, _, err := normalizeCreateInput(input)
	if err != nil {
		return err
	}
	if normalized.Scope.Provider != "" || normalized.Scope.Model != "" {
		return newValidationError("default execution plan must use global scope", nil)
	}

	versions, err := s.store.ListActive(ctx)
	if err != nil {
		return fmt.Errorf("list active execution plans: %w", err)
	}
	for _, version := range versions {
		scope, _, err := normalizeScope(version.Scope)
		if err != nil {
			return fmt.Errorf("load execution plan %q: %w", version.ID, err)
		}
		if scope.Provider == "" && scope.Model == "" {
			return nil
		}
	}

	if !normalized.Activate {
		normalized.Activate = true
	}
	if _, err := s.store.Create(ctx, normalized); err != nil {
		return fmt.Errorf("seed default global execution plan: %w", err)
	}
	return nil
}

// Match returns the most-specific compiled execution policy for one request.
func (s *Service) Match(selector core.ExecutionPlanSelector) (*core.ResolvedExecutionPolicy, error) {
	compiled, err := s.matchCompiled(selector)
	if err != nil || compiled == nil {
		return nil, err
	}
	policy := *compiled.Policy
	return &policy, nil
}

// PipelineForContext resolves the active guardrails pipeline for the request context.
func (s *Service) PipelineForContext(ctx context.Context) *guardrails.Pipeline {
	if s == nil || ctx == nil {
		return nil
	}
	plan := core.GetExecutionPlan(ctx)
	if plan == nil {
		return nil
	}
	return s.PipelineForExecutionPlan(plan)
}

// PipelineForExecutionPlan resolves the active guardrails pipeline for one request plan.
func (s *Service) PipelineForExecutionPlan(plan *core.ExecutionPlan) *guardrails.Pipeline {
	if s == nil || plan == nil || plan.Policy == nil || !plan.GuardrailsEnabled() {
		return nil
	}
	versionID := strings.TrimSpace(plan.Policy.VersionID)
	if versionID == "" {
		return nil
	}
	current := s.snapshot()
	compiled := current.byVersionID[versionID]
	if compiled == nil {
		return nil
	}
	return compiled.Pipeline
}

// StartBackgroundRefresh periodically reloads active execution plans until stopped.
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
					slog.Warn("execution plan refresh failed", "error", err)
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

func (s *Service) matchCompiled(selector core.ExecutionPlanSelector) (*CompiledPlan, error) {
	if s == nil {
		return nil, nil
	}
	selector = core.NewExecutionPlanSelector(selector.Provider, selector.Model)
	current := s.snapshot()

	if selector.Provider != "" && selector.Model != "" {
		if models := current.providerModels[selector.Provider]; models != nil {
			if compiled := models[selector.Model]; compiled != nil {
				return compiled, nil
			}
		}
	}
	if selector.Provider != "" {
		if compiled := current.providers[selector.Provider]; compiled != nil {
			return compiled, nil
		}
	}
	if current.global == nil {
		return nil, fmt.Errorf("missing active global execution plan")
	}
	return current.global, nil
}

func (s *Service) snapshot() snapshot {
	if s == nil {
		return snapshot{
			providers:      map[string]*CompiledPlan{},
			providerModels: map[string]map[string]*CompiledPlan{},
			byVersionID:    map[string]*CompiledPlan{},
		}
	}
	if current, ok := s.current.Load().(snapshot); ok {
		return current
	}
	return snapshot{
		providers:      map[string]*CompiledPlan{},
		providerModels: map[string]map[string]*CompiledPlan{},
		byVersionID:    map[string]*CompiledPlan{},
	}
}
