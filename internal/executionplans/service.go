package executionplans

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
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
	global             *CompiledPlan
	paths              map[string]*CompiledPlan
	providers          map[string]*CompiledPlan
	providerPaths      map[string]map[string]*CompiledPlan
	providerModels     map[string]map[string]*CompiledPlan
	providerModelPaths map[string]map[string]map[string]*CompiledPlan
	byVersionID        map[string]*CompiledPlan
}

// Service keeps the active execution-plan set cached in memory.
type Service struct {
	store     Store
	compiler  Compiler
	current   atomic.Value
	refreshMu sync.Mutex
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
		paths:              map[string]*CompiledPlan{},
		providers:          map[string]*CompiledPlan{},
		providerPaths:      map[string]map[string]*CompiledPlan{},
		providerModels:     map[string]map[string]*CompiledPlan{},
		providerModelPaths: map[string]map[string]map[string]*CompiledPlan{},
		byVersionID:        map[string]*CompiledPlan{},
	})
	return service, nil
}

// Refresh reloads active plans from storage and atomically swaps the in-memory snapshot.
func (s *Service) Refresh(ctx context.Context) error {
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()
	return s.refreshLocked(ctx)
}

func (s *Service) refreshLocked(ctx context.Context) error {
	versions, err := s.store.ListActive(ctx)
	if err != nil {
		return fmt.Errorf("list active execution plans: %w", err)
	}

	next := snapshot{
		paths:              make(map[string]*CompiledPlan),
		providers:          make(map[string]*CompiledPlan),
		providerPaths:      make(map[string]map[string]*CompiledPlan),
		providerModels:     make(map[string]map[string]*CompiledPlan),
		providerModelPaths: make(map[string]map[string]map[string]*CompiledPlan),
		byVersionID:        make(map[string]*CompiledPlan),
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
		case scope.Provider == "" && scope.UserPath == "":
			if next.global != nil {
				return fmt.Errorf("duplicate active global execution plans: %q and %q", next.global.Version.ID, version.ID)
			}
			next.global = compiled
		case scope.Provider == "" && scope.UserPath != "":
			if existing := next.paths[scope.UserPath]; existing != nil {
				return fmt.Errorf("duplicate active path execution plans for %q: %q and %q", scope.UserPath, existing.Version.ID, version.ID)
			}
			next.paths[scope.UserPath] = compiled
		case scope.Model == "" && scope.UserPath == "":
			if existing := next.providers[scope.Provider]; existing != nil {
				return fmt.Errorf("duplicate active provider execution plans for %q: %q and %q", scope.Provider, existing.Version.ID, version.ID)
			}
			next.providers[scope.Provider] = compiled
		case scope.Model == "" && scope.UserPath != "":
			paths := next.providerPaths[scope.Provider]
			if paths == nil {
				paths = make(map[string]*CompiledPlan)
				next.providerPaths[scope.Provider] = paths
			}
			if existing := paths[scope.UserPath]; existing != nil {
				return fmt.Errorf("duplicate active provider-path execution plans for %q/%q: %q and %q", scope.Provider, scope.UserPath, existing.Version.ID, version.ID)
			}
			paths[scope.UserPath] = compiled
		case scope.UserPath == "":
			models := next.providerModels[scope.Provider]
			if models == nil {
				models = make(map[string]*CompiledPlan)
				next.providerModels[scope.Provider] = models
			}
			if existing := models[scope.Model]; existing != nil {
				return fmt.Errorf("duplicate active provider-model execution plans for %q/%q: %q and %q", scope.Provider, scope.Model, existing.Version.ID, version.ID)
			}
			models[scope.Model] = compiled
		default:
			providers := next.providerModelPaths[scope.Provider]
			if providers == nil {
				providers = make(map[string]map[string]*CompiledPlan)
				next.providerModelPaths[scope.Provider] = providers
			}
			paths := providers[scope.Model]
			if paths == nil {
				paths = make(map[string]*CompiledPlan)
				providers[scope.Model] = paths
			}
			if existing := paths[scope.UserPath]; existing != nil {
				return fmt.Errorf("duplicate active provider-model-path execution plans for %q/%q/%q: %q and %q", scope.Provider, scope.Model, scope.UserPath, existing.Version.ID, version.ID)
			}
			paths[scope.UserPath] = compiled
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
	if normalized.Scope.Provider != "" || normalized.Scope.Model != "" || normalized.Scope.UserPath != "" {
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
		if scope.Provider == "" && scope.Model == "" && scope.UserPath == "" {
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

// Create inserts a new immutable execution-plan version and refreshes the
// in-memory snapshot so future requests can match it immediately.
func (s *Service) Create(ctx context.Context, input CreateInput) (*Version, error) {
	if s == nil {
		return nil, fmt.Errorf("execution plan service is required")
	}

	normalized, scopeKey, planHash, err := normalizeCreateInput(input)
	if err != nil {
		return nil, err
	}
	previewCompiled, err := s.validateCreateCandidate(normalized, scopeKey, planHash)
	if err != nil {
		return nil, err
	}

	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()

	version, err := s.store.Create(ctx, normalized)
	if err != nil {
		return nil, fmt.Errorf("create execution plan: %w", err)
	}
	if version != nil && version.Active {
		s.storeActivatedCompiledLocked(compiledPlanForVersion(previewCompiled, *version))
	}
	return version, nil
}

// Deactivate turns off one active execution-plan version and refreshes the
// in-memory snapshot so future requests stop matching it immediately.
func (s *Service) Deactivate(ctx context.Context, id string) error {
	if s == nil {
		return fmt.Errorf("execution plan service is required")
	}

	version, err := s.store.Get(ctx, strings.TrimSpace(id))
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return err
		}
		return fmt.Errorf("load execution plan %q: %w", id, err)
	}
	if version == nil {
		return ErrNotFound
	}

	scope, scopeKey, err := normalizeScope(version.Scope)
	if err != nil {
		return fmt.Errorf("load execution plan %q: %w", id, err)
	}
	version.Scope = scope
	version.ScopeKey = scopeKey

	if scope.Provider == "" && scope.Model == "" && scope.UserPath == "" {
		return newValidationError("cannot deactivate the global workflow", nil)
	}
	if !version.Active {
		return newValidationError("workflow is already inactive", nil)
	}

	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()

	if err := s.store.Deactivate(ctx, version.ID); err != nil {
		if errors.Is(err, ErrNotFound) {
			return err
		}
		return fmt.Errorf("deactivate execution plan %q: %w", version.ID, err)
	}
	s.storeDeactivatedVersionLocked(*version)
	return nil
}

// GetView returns one execution plan version view, including inactive historical versions.
func (s *Service) GetView(ctx context.Context, id string) (View, error) {
	if s == nil {
		return View{}, fmt.Errorf("execution plan service is required")
	}

	version, err := s.store.Get(ctx, strings.TrimSpace(id))
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return View{}, err
		}
		return View{}, fmt.Errorf("load execution plan %q: %w", id, err)
	}
	if version == nil {
		return View{}, ErrNotFound
	}

	return s.viewForVersion(*version)
}

// ListViews returns the active execution plans together with their effective
// runtime features after process-level caps are applied.
func (s *Service) ListViews(ctx context.Context) ([]View, error) {
	if s == nil {
		return []View{}, nil
	}

	versions, err := s.store.ListActive(ctx)
	if err != nil {
		return nil, fmt.Errorf("list active execution plans: %w", err)
	}

	views := make([]View, 0, len(versions))
	for _, version := range versions {
		view, err := s.viewForVersion(version)
		if err != nil {
			slog.Warn("execution plan view build failed", "version_id", strings.TrimSpace(version.ID), "error", err)
			views = append(views, viewWithError(version, err))
			continue
		}
		views = append(views, view)
	}

	sort.SliceStable(views, func(i, j int) bool {
		left, right := views[i], views[j]
		if leftSpecificity, rightSpecificity := viewScopeSpecificity(left.ScopeType), viewScopeSpecificity(right.ScopeType); leftSpecificity != rightSpecificity {
			return leftSpecificity < rightSpecificity
		}
		if left.ScopeDisplay != right.ScopeDisplay {
			return left.ScopeDisplay < right.ScopeDisplay
		}
		if !left.CreatedAt.Equal(right.CreatedAt) {
			return left.CreatedAt.After(right.CreatedAt)
		}
		return left.ID < right.ID
	})

	return views, nil
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
				func() {
					refreshCtx, refreshCancel := context.WithTimeout(ctx, 30*time.Second)
					defer refreshCancel()
					if err := s.Refresh(refreshCtx); err != nil {
						slog.Warn("execution plan refresh failed", "error", err)
					}
				}()
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
	selector = core.NewExecutionPlanSelector(selector.Provider, selector.Model, selector.UserPath)
	current := s.snapshot()
	ancestors := core.UserPathAncestors(selector.UserPath)

	if selector.Provider != "" && selector.Model != "" && len(ancestors) > 0 {
		if models := current.providerModelPaths[selector.Provider]; models != nil {
			if paths := models[selector.Model]; paths != nil {
				for _, userPath := range ancestors {
					if compiled := paths[userPath]; compiled != nil {
						return compiled, nil
					}
				}
			}
		}
	}
	if selector.Provider != "" && selector.Model != "" {
		if models := current.providerModels[selector.Provider]; models != nil {
			if compiled := models[selector.Model]; compiled != nil {
				return compiled, nil
			}
		}
	}
	if selector.Provider != "" && len(ancestors) > 0 {
		if paths := current.providerPaths[selector.Provider]; paths != nil {
			for _, userPath := range ancestors {
				if compiled := paths[userPath]; compiled != nil {
					return compiled, nil
				}
			}
		}
	}
	if len(ancestors) > 0 {
		for _, userPath := range ancestors {
			if compiled := current.paths[userPath]; compiled != nil {
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

func (s *Service) validateCreateCandidate(input CreateInput, scopeKey, planHash string) (*CompiledPlan, error) {
	version := Version{
		ID:          "preview",
		Scope:       input.Scope,
		ScopeKey:    scopeKey,
		Version:     1,
		Active:      input.Activate,
		Name:        input.Name,
		Description: input.Description,
		Payload:     input.Payload,
		PlanHash:    planHash,
		CreatedAt:   time.Unix(0, 0).UTC(),
	}
	compiled, err := s.compiler.Compile(version)
	if err != nil {
		return nil, newValidationError(err.Error(), err)
	}
	if compiled == nil || compiled.Policy == nil {
		return nil, newValidationError("compiled plan is empty or missing policy", nil)
	}
	return compiled, nil
}

func (s *Service) viewForVersion(version Version) (View, error) {
	scope, scopeKey, err := normalizeScope(version.Scope)
	if err != nil {
		return View{}, fmt.Errorf("load execution plan %q: %w", version.ID, err)
	}
	version.Scope = scope
	if strings.TrimSpace(version.ScopeKey) == "" {
		version.ScopeKey = scopeKey
	}

	compiled, err := s.compiler.Compile(version)
	if err != nil {
		return View{}, fmt.Errorf("compile execution plan %q: %w", version.ID, err)
	}
	if compiled == nil || compiled.Policy == nil {
		return View{}, fmt.Errorf("compile execution plan %q: empty compiled plan", version.ID)
	}

	return View{
		Version:           version,
		ScopeType:         scopeType(scope),
		ScopeDisplay:      scopeDisplay(scope),
		EffectiveFeatures: compiled.Policy.Features,
		GuardrailsHash:    compiled.Policy.GuardrailsHash,
	}, nil
}

func viewWithError(version Version, err error) View {
	scope := Scope{
		Provider: strings.TrimSpace(version.Scope.Provider),
		Model:    strings.TrimSpace(version.Scope.Model),
		UserPath: strings.TrimSpace(version.Scope.UserPath),
	}
	version.Scope = scope

	return View{
		Version:      version,
		ScopeType:    rawScopeType(scope),
		ScopeDisplay: rawScopeDisplay(scope),
		CompileError: err.Error(),
	}
}

func rawScopeType(scope Scope) string {
	switch {
	case strings.TrimSpace(scope.Provider) == "" && strings.TrimSpace(scope.Model) == "" && strings.TrimSpace(scope.UserPath) == "":
		return "global"
	case strings.TrimSpace(scope.Provider) == "" && strings.TrimSpace(scope.UserPath) != "":
		return "path"
	case strings.TrimSpace(scope.Provider) != "" && strings.TrimSpace(scope.Model) == "":
		if strings.TrimSpace(scope.UserPath) != "" {
			return "provider_path"
		}
		return "provider"
	case strings.TrimSpace(scope.UserPath) != "":
		return "provider_model_path"
	default:
		return "provider_model"
	}
}

func rawScopeDisplay(scope Scope) string {
	provider := strings.TrimSpace(scope.Provider)
	model := strings.TrimSpace(scope.Model)
	userPath := strings.TrimSpace(scope.UserPath)

	switch {
	case provider == "" && model == "" && userPath == "":
		return "global"
	case provider == "" && userPath != "":
		return userPath
	case provider != "" && model == "" && userPath == "":
		return provider
	case provider != "" && model == "" && userPath != "":
		return provider + " @ " + userPath
	case provider == "" && model != "":
		return model
	case userPath != "":
		return provider + "/" + model + " @ " + userPath
	default:
		return provider + "/" + model
	}
}

func scopeType(scope Scope) string {
	switch {
	case strings.TrimSpace(scope.Provider) == "" && strings.TrimSpace(scope.UserPath) == "":
		return "global"
	case strings.TrimSpace(scope.Provider) == "" && strings.TrimSpace(scope.UserPath) != "":
		return "path"
	case strings.TrimSpace(scope.Model) == "" && strings.TrimSpace(scope.UserPath) == "":
		return "provider"
	case strings.TrimSpace(scope.Model) == "" && strings.TrimSpace(scope.UserPath) != "":
		return "provider_path"
	case strings.TrimSpace(scope.UserPath) != "":
		return "provider_model_path"
	default:
		return "provider_model"
	}
}

func scopeDisplay(scope Scope) string {
	switch scopeType(scope) {
	case "global":
		return "global"
	case "path":
		return scope.UserPath
	case "provider_path":
		return scope.Provider + " @ " + scope.UserPath
	case "provider_model_path":
		return scope.Provider + "/" + scope.Model + " @ " + scope.UserPath
	case "provider":
		return scope.Provider
	default:
		return scope.Provider + "/" + scope.Model
	}
}

func viewScopeSpecificity(scopeType string) int {
	switch strings.TrimSpace(scopeType) {
	case "global":
		return 0
	case "path":
		return 2
	case "provider":
		return 1
	case "provider_path":
		return 3
	case "provider_model":
		return 4
	default:
		return 5
	}
}

func (s *Service) snapshot() snapshot {
	if s == nil {
		return snapshot{
			paths:              map[string]*CompiledPlan{},
			providers:          map[string]*CompiledPlan{},
			providerPaths:      map[string]map[string]*CompiledPlan{},
			providerModels:     map[string]map[string]*CompiledPlan{},
			providerModelPaths: map[string]map[string]map[string]*CompiledPlan{},
			byVersionID:        map[string]*CompiledPlan{},
		}
	}
	if current, ok := s.current.Load().(snapshot); ok {
		return current
	}
	return snapshot{
		paths:              map[string]*CompiledPlan{},
		providers:          map[string]*CompiledPlan{},
		providerPaths:      map[string]map[string]*CompiledPlan{},
		providerModels:     map[string]map[string]*CompiledPlan{},
		providerModelPaths: map[string]map[string]map[string]*CompiledPlan{},
		byVersionID:        map[string]*CompiledPlan{},
	}
}

func cloneSnapshot(current snapshot) snapshot {
	next := snapshot{
		global:             current.global,
		paths:              make(map[string]*CompiledPlan, len(current.paths)),
		providers:          make(map[string]*CompiledPlan, len(current.providers)),
		providerPaths:      make(map[string]map[string]*CompiledPlan, len(current.providerPaths)),
		providerModels:     make(map[string]map[string]*CompiledPlan, len(current.providerModels)),
		providerModelPaths: make(map[string]map[string]map[string]*CompiledPlan, len(current.providerModelPaths)),
		byVersionID:        make(map[string]*CompiledPlan, len(current.byVersionID)),
	}
	for userPath, compiled := range current.paths {
		next.paths[userPath] = compiled
	}
	for provider, compiled := range current.providers {
		next.providers[provider] = compiled
	}
	for provider, paths := range current.providerPaths {
		copied := make(map[string]*CompiledPlan, len(paths))
		for userPath, compiled := range paths {
			copied[userPath] = compiled
		}
		next.providerPaths[provider] = copied
	}
	for provider, models := range current.providerModels {
		copied := make(map[string]*CompiledPlan, len(models))
		for model, compiled := range models {
			copied[model] = compiled
		}
		next.providerModels[provider] = copied
	}
	for provider, models := range current.providerModelPaths {
		modelCopy := make(map[string]map[string]*CompiledPlan, len(models))
		for model, paths := range models {
			pathCopy := make(map[string]*CompiledPlan, len(paths))
			for userPath, compiled := range paths {
				pathCopy[userPath] = compiled
			}
			modelCopy[model] = pathCopy
		}
		next.providerModelPaths[provider] = modelCopy
	}
	for versionID, compiled := range current.byVersionID {
		next.byVersionID[versionID] = compiled
	}
	return next
}

func compiledPlanForVersion(compiled *CompiledPlan, version Version) *CompiledPlan {
	if compiled == nil {
		return nil
	}
	next := &CompiledPlan{
		Version:  version,
		Pipeline: compiled.Pipeline,
	}
	if compiled.Policy != nil {
		policy := *compiled.Policy
		policy.VersionID = version.ID
		policy.Version = version.Version
		policy.ScopeProvider = version.Scope.Provider
		policy.ScopeModel = version.Scope.Model
		policy.ScopeUserPath = version.Scope.UserPath
		policy.Name = version.Name
		policy.PlanHash = version.PlanHash
		next.Policy = &policy
	}
	return next
}

func (s *Service) storeActivatedCompiledLocked(compiled *CompiledPlan) {
	if s == nil || compiled == nil {
		return
	}
	next := cloneSnapshot(s.snapshot())
	scope := compiled.Version.Scope

	switch {
	case scope.Provider == "" && scope.UserPath == "":
		if next.global != nil {
			delete(next.byVersionID, next.global.Version.ID)
		}
		next.global = compiled
	case scope.Provider == "" && scope.UserPath != "":
		if existing := next.paths[scope.UserPath]; existing != nil {
			delete(next.byVersionID, existing.Version.ID)
		}
		next.paths[scope.UserPath] = compiled
	case scope.Model == "" && scope.UserPath == "":
		if existing := next.providers[scope.Provider]; existing != nil {
			delete(next.byVersionID, existing.Version.ID)
		}
		next.providers[scope.Provider] = compiled
	case scope.Model == "" && scope.UserPath != "":
		paths := next.providerPaths[scope.Provider]
		if paths == nil {
			paths = make(map[string]*CompiledPlan)
			next.providerPaths[scope.Provider] = paths
		}
		if existing := paths[scope.UserPath]; existing != nil {
			delete(next.byVersionID, existing.Version.ID)
		}
		paths[scope.UserPath] = compiled
	case scope.UserPath == "":
		models := next.providerModels[scope.Provider]
		if models == nil {
			models = make(map[string]*CompiledPlan)
			next.providerModels[scope.Provider] = models
		}
		if existing := models[scope.Model]; existing != nil {
			delete(next.byVersionID, existing.Version.ID)
		}
		models[scope.Model] = compiled
	default:
		providers := next.providerModelPaths[scope.Provider]
		if providers == nil {
			providers = make(map[string]map[string]*CompiledPlan)
			next.providerModelPaths[scope.Provider] = providers
		}
		paths := providers[scope.Model]
		if paths == nil {
			paths = make(map[string]*CompiledPlan)
			providers[scope.Model] = paths
		}
		if existing := paths[scope.UserPath]; existing != nil {
			delete(next.byVersionID, existing.Version.ID)
		}
		paths[scope.UserPath] = compiled
	}

	next.byVersionID[compiled.Version.ID] = compiled
	s.current.Store(next)
}

func (s *Service) storeDeactivatedVersionLocked(version Version) {
	if s == nil {
		return
	}
	next := cloneSnapshot(s.snapshot())
	scope := version.Scope

	delete(next.byVersionID, version.ID)

	switch {
	case scope.Provider == "" && scope.UserPath == "":
		next.global = nil
	case scope.Provider == "" && scope.UserPath != "":
		delete(next.paths, scope.UserPath)
	case scope.Model == "" && scope.UserPath == "":
		delete(next.providers, scope.Provider)
	case scope.Model == "" && scope.UserPath != "":
		paths := next.providerPaths[scope.Provider]
		if paths == nil {
			break
		}
		delete(paths, scope.UserPath)
		if len(paths) == 0 {
			delete(next.providerPaths, scope.Provider)
		}
	case scope.UserPath == "":
		models := next.providerModels[scope.Provider]
		if models == nil {
			break
		}
		delete(models, scope.Model)
		if len(models) == 0 {
			delete(next.providerModels, scope.Provider)
		}
	default:
		models := next.providerModelPaths[scope.Provider]
		if models == nil {
			break
		}
		paths := models[scope.Model]
		if paths == nil {
			break
		}
		delete(paths, scope.UserPath)
		if len(paths) == 0 {
			delete(models, scope.Model)
		}
		if len(models) == 0 {
			delete(next.providerModelPaths, scope.Provider)
		}
	}

	s.current.Store(next)
}
