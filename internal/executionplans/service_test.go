package executionplans

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gomodel/internal/core"
	"gomodel/internal/guardrails"
)

type staticStore struct {
	versions []Version
}

func (s *staticStore) ListActive(context.Context) ([]Version, error) {
	result := make([]Version, 0, len(s.versions))
	for _, version := range s.versions {
		if version.Active {
			result = append(result, version)
		}
	}
	return result, nil
}
func (s *staticStore) Get(_ context.Context, id string) (*Version, error) {
	for _, version := range s.versions {
		if version.ID == id {
			versionCopy := version
			return &versionCopy, nil
		}
	}
	return nil, ErrNotFound
}
func (s *staticStore) Create(_ context.Context, input CreateInput) (*Version, error) {
	input, scopeKey, planHash, err := normalizeCreateInput(input)
	if err != nil {
		return nil, err
	}
	if input.Activate {
		for i := range s.versions {
			if s.versions[i].ScopeKey == scopeKey {
				s.versions[i].Active = false
			}
		}
	}
	version := Version{
		ID:          "created-global",
		Scope:       input.Scope,
		ScopeKey:    scopeKey,
		Version:     1,
		Active:      input.Activate,
		Managed:     input.Managed,
		Name:        input.Name,
		Description: input.Description,
		Payload:     input.Payload,
		PlanHash:    planHash,
	}
	s.versions = append(s.versions, version)
	return &version, nil
}

func (s *staticStore) EnsureManagedDefaultGlobal(ctx context.Context, input CreateInput, planHash string) (*Version, error) {
	for _, version := range s.versions {
		if !version.Active || version.ScopeKey != "global" {
			continue
		}
		if !version.Managed {
			return nil, nil
		}
		if version.Name == input.Name && version.Description == input.Description && version.PlanHash == planHash {
			return nil, nil
		}
		break
	}
	return s.Create(ctx, input)
}

func (s *staticStore) Deactivate(_ context.Context, id string) error {
	for i := range s.versions {
		if s.versions[i].ID == id && s.versions[i].Active {
			s.versions[i].Active = false
			return nil
		}
	}
	return ErrNotFound
}
func (s *staticStore) Close() error { return nil }

type concurrentStore struct {
	mu           sync.Mutex
	versions     []Version
	createCalled chan struct{}
}

func (s *concurrentStore) ListActive(context.Context) ([]Version, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	result := make([]Version, 0, len(s.versions))
	for _, version := range s.versions {
		if version.Active {
			result = append(result, version)
		}
	}
	return result, nil
}

func (s *concurrentStore) Get(_ context.Context, id string) (*Version, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, version := range s.versions {
		if version.ID == id {
			versionCopy := version
			return &versionCopy, nil
		}
	}
	return nil, ErrNotFound
}

func (s *concurrentStore) Create(_ context.Context, input CreateInput) (*Version, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.createLocked(input)
}

func (s *concurrentStore) createLocked(input CreateInput) (*Version, error) {
	input, scopeKey, planHash, err := normalizeCreateInput(input)
	if err != nil {
		return nil, err
	}
	if input.Activate {
		for i := range s.versions {
			if s.versions[i].ScopeKey == scopeKey {
				s.versions[i].Active = false
			}
		}
	}
	version := Version{
		ID:          "created-provider",
		Scope:       input.Scope,
		ScopeKey:    scopeKey,
		Version:     len(s.versions) + 1,
		Active:      input.Activate,
		Managed:     input.Managed,
		Name:        input.Name,
		Description: input.Description,
		Payload:     input.Payload,
		PlanHash:    planHash,
	}
	s.versions = append(s.versions, version)
	if s.createCalled != nil {
		select {
		case s.createCalled <- struct{}{}:
		default:
		}
	}
	return &version, nil
}

func (s *concurrentStore) EnsureManagedDefaultGlobal(_ context.Context, input CreateInput, planHash string) (*Version, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, version := range s.versions {
		if !version.Active || version.ScopeKey != "global" {
			continue
		}
		if !version.Managed {
			return nil, nil
		}
		if version.Name == input.Name && version.Description == input.Description && version.PlanHash == planHash {
			return nil, nil
		}
		break
	}
	return s.createLocked(input)
}

func (s *concurrentStore) Deactivate(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i := range s.versions {
		if s.versions[i].ID == id && s.versions[i].Active {
			s.versions[i].Active = false
			return nil
		}
	}
	return ErrNotFound
}

func (s *concurrentStore) Close() error { return nil }

type blockingCompiler struct {
	delegate  Compiler
	blockCall int32
	callCount int32
	blocked   chan struct{}
	release   chan struct{}
}

func (c *blockingCompiler) Compile(version Version) (*CompiledPlan, error) {
	call := atomic.AddInt32(&c.callCount, 1)
	if call == c.blockCall {
		close(c.blocked)
		<-c.release
	}
	return c.delegate.Compile(version)
}

type previewEmptyCompiler struct {
	delegate Compiler
}

func (c *previewEmptyCompiler) Compile(version Version) (*CompiledPlan, error) {
	if version.ID == "preview" {
		return nil, nil
	}
	return c.delegate.Compile(version)
}

type versionFailingCompiler struct {
	delegate Compiler
	version  string
	err      error
}

func (c *versionFailingCompiler) Compile(version Version) (*CompiledPlan, error) {
	if version.ID == c.version {
		return nil, c.err
	}
	return c.delegate.Compile(version)
}

type contextCancelingStore struct {
	staticStore
	cancelOnCreate     context.CancelFunc
	cancelOnDeactivate context.CancelFunc
}

type refreshFailingStore struct {
	staticStore
	failListActive error
}

func (s *contextCancelingStore) ListActive(ctx context.Context) ([]Version, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return s.staticStore.ListActive(ctx)
}

func (s *contextCancelingStore) Create(ctx context.Context, input CreateInput) (*Version, error) {
	version, err := s.staticStore.Create(ctx, input)
	if err == nil && s.cancelOnCreate != nil {
		s.cancelOnCreate()
	}
	return version, err
}

func (s *contextCancelingStore) Deactivate(ctx context.Context, id string) error {
	err := s.staticStore.Deactivate(ctx, id)
	if err == nil && s.cancelOnDeactivate != nil {
		s.cancelOnDeactivate()
	}
	return err
}

func (s *refreshFailingStore) ListActive(ctx context.Context) ([]Version, error) {
	if s.failListActive != nil {
		return nil, s.failListActive
	}
	return s.staticStore.ListActive(ctx)
}

func (s *refreshFailingStore) Create(ctx context.Context, input CreateInput) (*Version, error) {
	version, err := s.staticStore.Create(ctx, input)
	if err == nil {
		s.failListActive = errors.New("list active failed after create")
	}
	return version, err
}

func (s *refreshFailingStore) Deactivate(ctx context.Context, id string) error {
	err := s.staticStore.Deactivate(ctx, id)
	if err == nil {
		s.failListActive = errors.New("list active failed after deactivate")
	}
	return err
}

func TestServiceMatch_MostSpecificWins(t *testing.T) {
	store := &staticStore{
		versions: []Version{
			{
				ID:       "global",
				Scope:    Scope{},
				ScopeKey: "global",
				Version:  1,
				Active:   true,
				Name:     "global",
				Payload: Payload{
					SchemaVersion: 1,
					Features:      FeatureFlags{Cache: true, Audit: true, Usage: true, Guardrails: false},
				},
			},
			{
				ID:       "provider",
				Scope:    Scope{Provider: "openai"},
				ScopeKey: "provider:openai",
				Version:  1,
				Active:   true,
				Name:     "provider",
				Payload: Payload{
					SchemaVersion: 1,
					Features:      FeatureFlags{Cache: false, Audit: true, Usage: true, Guardrails: false},
				},
			},
			{
				ID:       "provider-model",
				Scope:    Scope{Provider: "openai", Model: "gpt-5"},
				ScopeKey: "provider_model:openai:gpt-5",
				Version:  1,
				Active:   true,
				Name:     "provider-model",
				Payload: Payload{
					SchemaVersion: 1,
					Features:      FeatureFlags{Cache: false, Audit: false, Usage: true, Guardrails: false},
				},
			},
			{
				ID:       "path-team",
				Scope:    Scope{UserPath: "/team"},
				ScopeKey: "path:/team",
				Version:  1,
				Active:   true,
				Name:     "path-team",
				Payload: Payload{
					SchemaVersion: 1,
					Features:      FeatureFlags{Cache: true, Audit: false, Usage: true, Guardrails: false},
				},
			},
			{
				ID:       "provider-model-path",
				Scope:    Scope{Provider: "openai", Model: "gpt-5", UserPath: "/team/a"},
				ScopeKey: "provider_model_path:openai:gpt-5:/team/a",
				Version:  1,
				Active:   true,
				Name:     "provider-model-path",
				Payload: Payload{
					SchemaVersion: 1,
					Features:      FeatureFlags{Cache: false, Audit: false, Usage: false, Guardrails: false},
				},
			},
		},
	}

	service, err := NewService(store, NewCompiler(nil))
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}

	assertMatch := func(name string, selector core.ExecutionPlanSelector, wantVersionID string) {
		t.Helper()
		policy, err := service.Match(selector)
		if err != nil {
			t.Fatalf("%s: Match() error = %v", name, err)
		}
		if policy == nil {
			t.Fatalf("%s: Match() returned nil policy", name)
		}
		if policy.VersionID != wantVersionID {
			t.Fatalf("%s: VersionID = %q, want %q", name, policy.VersionID, wantVersionID)
		}
	}

	assertMatch("provider+model+path", core.NewExecutionPlanSelector("openai", "gpt-5", "/team/a/user"), "provider-model-path")
	assertMatch("path beats provider+model", core.NewExecutionPlanSelector("openai", "gpt-5", "/team/user"), "path-team")
	assertMatch("provider+model", core.NewExecutionPlanSelector("openai", "gpt-5"), "provider-model")
	assertMatch("path", core.NewExecutionPlanSelector("anthropic", "claude-sonnet-4", "/team/a/user"), "path-team")
	assertMatch("provider", core.NewExecutionPlanSelector("openai", "gpt-4o"), "provider")
	assertMatch("global", core.NewExecutionPlanSelector("anthropic", "claude-sonnet-4"), "global")
}

func TestServiceEnsureDefaultGlobal_CreatesWhenMissing(t *testing.T) {
	store := &staticStore{}
	service, err := NewService(store, NewCompiler(nil))
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	err = service.EnsureDefaultGlobal(context.Background(), CreateInput{
		Activate: true,
		Name:     "default-global",
		Payload: Payload{
			SchemaVersion: 1,
			Features:      FeatureFlags{Cache: true, Audit: true, Usage: true, Guardrails: false},
		},
	})
	if err != nil {
		t.Fatalf("EnsureDefaultGlobal() error = %v", err)
	}
	if len(store.versions) != 1 {
		t.Fatalf("len(store.versions) = %d, want 1", len(store.versions))
	}
	if got := store.versions[0].ScopeKey; got != "global" {
		t.Fatalf("ScopeKey = %q, want global", got)
	}
	if !store.versions[0].Managed {
		t.Fatal("Managed = false, want true for managed default global")
	}
	policy, err := service.Match(core.NewExecutionPlanSelector("openai", "gpt-5"))
	if err != nil {
		t.Fatalf("Match() error = %v", err)
	}
	if policy == nil {
		t.Fatal("Match() returned nil policy")
	}
	if policy.VersionID != store.versions[0].ID {
		t.Fatalf("Match().VersionID = %q, want %q", policy.VersionID, store.versions[0].ID)
	}
}

func TestServiceEnsureDefaultGlobal_ReconcilesManagedDefault(t *testing.T) {
	store := &staticStore{
		versions: []Version{
			{
				ID:          "global-v1",
				Scope:       Scope{},
				ScopeKey:    "global",
				Version:     1,
				Active:      true,
				Managed:     true,
				Name:        ManagedDefaultGlobalName,
				Description: ManagedDefaultGlobalDescription,
				Payload: Payload{
					SchemaVersion: 1,
					Features:      FeatureFlags{Cache: false, Audit: true, Usage: true, Guardrails: false},
				},
				PlanHash: "stale-hash",
			},
		},
	}
	service, err := NewService(store, NewCompiler(nil))
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}

	err = service.EnsureDefaultGlobal(context.Background(), CreateInput{
		Activate:    true,
		Name:        ManagedDefaultGlobalName,
		Description: ManagedDefaultGlobalDescription,
		Payload: Payload{
			SchemaVersion: 1,
			Features:      FeatureFlags{Cache: true, Audit: true, Usage: true, Guardrails: false},
		},
	})
	if err != nil {
		t.Fatalf("EnsureDefaultGlobal() error = %v", err)
	}
	if len(store.versions) != 2 {
		t.Fatalf("len(store.versions) = %d, want 2", len(store.versions))
	}
	if store.versions[0].Active {
		t.Fatal("store.versions[0].Active = true, want old managed default deactivated")
	}
	if !store.versions[1].Active {
		t.Fatal("store.versions[1].Active = false, want updated managed default active")
	}
	if !store.versions[1].Managed {
		t.Fatal("store.versions[1].Managed = false, want updated managed default marker")
	}
	if !store.versions[1].Payload.Features.Cache {
		t.Fatal("store.versions[1].Payload.Features.Cache = false, want updated payload")
	}
}

func TestServiceEnsureDefaultGlobal_PreservesCustomGlobal(t *testing.T) {
	store := &staticStore{
		versions: []Version{
			{
				ID:          "global-v1",
				Scope:       Scope{},
				ScopeKey:    "global",
				Version:     1,
				Active:      true,
				Name:        "custom-global",
				Description: "User managed",
				Payload: Payload{
					SchemaVersion: 1,
					Features:      FeatureFlags{Cache: false, Audit: true, Usage: true, Guardrails: false},
				},
				PlanHash: "custom-hash",
			},
		},
	}
	service, err := NewService(store, NewCompiler(nil))
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}

	err = service.EnsureDefaultGlobal(context.Background(), CreateInput{
		Activate:    true,
		Name:        ManagedDefaultGlobalName,
		Description: ManagedDefaultGlobalDescription,
		Payload: Payload{
			SchemaVersion: 1,
			Features:      FeatureFlags{Cache: true, Audit: true, Usage: true, Guardrails: false},
		},
	})
	if err != nil {
		t.Fatalf("EnsureDefaultGlobal() error = %v", err)
	}
	if len(store.versions) != 1 {
		t.Fatalf("len(store.versions) = %d, want 1", len(store.versions))
	}
	if store.versions[0].Name != "custom-global" || !store.versions[0].Active {
		t.Fatalf("store.versions[0] = %#v, want unchanged active custom global", store.versions[0])
	}
}

func TestServiceEnsureDefaultGlobal_LoadsPreservedCustomGlobalIntoSnapshot(t *testing.T) {
	store := &staticStore{
		versions: []Version{
			{
				ID:          "global-v1",
				Scope:       Scope{},
				ScopeKey:    "global",
				Version:     1,
				Active:      true,
				Name:        "custom-global",
				Description: "User managed",
				Payload: Payload{
					SchemaVersion: 1,
					Features:      FeatureFlags{Cache: false, Audit: true, Usage: true, Guardrails: false},
				},
				PlanHash: "custom-hash",
			},
		},
	}
	service, err := NewService(store, NewCompiler(nil))
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	err = service.EnsureDefaultGlobal(context.Background(), CreateInput{
		Activate:    true,
		Name:        ManagedDefaultGlobalName,
		Description: ManagedDefaultGlobalDescription,
		Payload: Payload{
			SchemaVersion: 1,
			Features:      FeatureFlags{Cache: true, Audit: true, Usage: true, Guardrails: false},
		},
	})
	if err != nil {
		t.Fatalf("EnsureDefaultGlobal() error = %v", err)
	}

	policy, err := service.Match(core.NewExecutionPlanSelector("openai", "gpt-5"))
	if err != nil {
		t.Fatalf("Match() error = %v", err)
	}
	if policy == nil {
		t.Fatal("Match() returned nil policy")
	}
	if policy.VersionID != "global-v1" {
		t.Fatalf("Match().VersionID = %q, want global-v1", policy.VersionID)
	}
}

func TestServiceEnsureDefaultGlobal_ValidatesBeforeStoreMutation(t *testing.T) {
	store := &concurrentStore{
		createCalled: make(chan struct{}, 1),
	}
	service, err := NewService(store, &previewEmptyCompiler{delegate: NewCompiler(nil)})
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	err = service.EnsureDefaultGlobal(context.Background(), CreateInput{
		Activate:    true,
		Name:        ManagedDefaultGlobalName,
		Description: ManagedDefaultGlobalDescription,
		Payload: Payload{
			SchemaVersion: 1,
			Features:      FeatureFlags{Cache: true, Audit: true, Usage: true, Guardrails: false},
		},
	})
	if err == nil {
		t.Fatal("EnsureDefaultGlobal() error = nil, want validation error")
	}
	if !IsValidationError(err) {
		t.Fatalf("EnsureDefaultGlobal() error = %v, want validation error", err)
	}
	if len(store.versions) != 0 {
		t.Fatalf("len(store.versions) = %d, want 0", len(store.versions))
	}
	select {
	case <-store.createCalled:
		t.Fatal("EnsureDefaultGlobal() mutated store before validation")
	default:
	}
}

func TestServiceRefresh_RebuildsCompiledGuardrailPipelinesAfterExecutorSwap(t *testing.T) {
	guardrailStore := &guardrailTestStore{
		definitions: map[string]guardrails.Definition{
			"privacy": {
				Name: "privacy",
				Type: "llm_based_altering",
				Config: mustMarshalJSON(t, struct {
					Model string   `json:"model"`
					Roles []string `json:"roles"`
				}{
					Model: "gpt-4o-mini",
					Roles: []string{"user"},
				}),
			},
		},
	}
	guardrailService, err := guardrails.NewService(guardrailStore, guardrailExecutorFunc(func(_ context.Context, _ *core.ChatRequest) (*core.ChatResponse, error) {
		return &core.ChatResponse{
			Choices: []core.Choice{
				{Message: core.ResponseMessage{Role: "assistant", Content: "[|---|](PERSON_1)"}},
			},
		}, nil
	}))
	if err != nil {
		t.Fatalf("guardrails.NewService() error = %v", err)
	}
	if err := guardrailService.Refresh(context.Background()); err != nil {
		t.Fatalf("guardrailService.Refresh() error = %v", err)
	}

	store := &staticStore{
		versions: []Version{
			{
				ID:       "global-v1",
				Scope:    Scope{},
				ScopeKey: "global",
				Version:  1,
				Active:   true,
				Name:     "global",
				Payload: Payload{
					SchemaVersion: 1,
					Features: FeatureFlags{
						Cache:      false,
						Audit:      true,
						Usage:      true,
						Guardrails: true,
					},
					Guardrails: []GuardrailStep{
						{Ref: "privacy", Step: 10},
					},
				},
			},
		},
	}
	service, err := NewService(store, NewCompilerWithFeatureCaps(guardrailService, core.DefaultExecutionFeatures()))
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("service.Refresh() error = %v", err)
	}

	selector := core.NewExecutionPlanSelector("", "", "/")
	policy, err := service.Match(selector)
	if err != nil {
		t.Fatalf("service.Match() error = %v", err)
	}
	plan := &core.ExecutionPlan{Policy: policy}

	assertPipelineRewrite(t, service.PipelineForExecutionPlan(plan), "[|---|](PERSON_1)")

	if err := guardrailService.SetExecutor(context.Background(), guardrailExecutorFunc(func(_ context.Context, _ *core.ChatRequest) (*core.ChatResponse, error) {
		return &core.ChatResponse{
			Choices: []core.Choice{
				{Message: core.ResponseMessage{Role: "assistant", Content: "[|---|](PERSON_2)"}},
			},
		}, nil
	})); err != nil {
		t.Fatalf("guardrailService.SetExecutor() error = %v", err)
	}

	assertPipelineRewrite(t, service.PipelineForExecutionPlan(plan), "[|---|](PERSON_1)")

	if err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("service.Refresh() after SetExecutor error = %v", err)
	}
	assertPipelineRewrite(t, service.PipelineForExecutionPlan(plan), "[|---|](PERSON_2)")
}

type guardrailTestStore struct {
	definitions map[string]guardrails.Definition
}

func (s *guardrailTestStore) List(context.Context) ([]guardrails.Definition, error) {
	result := make([]guardrails.Definition, 0, len(s.definitions))
	for _, definition := range s.definitions {
		result = append(result, definition)
	}
	return result, nil
}

func (s *guardrailTestStore) Get(_ context.Context, name string) (*guardrails.Definition, error) {
	definition, ok := s.definitions[name]
	if !ok {
		return nil, guardrails.ErrNotFound
	}
	copy := definition
	return &copy, nil
}

func (s *guardrailTestStore) Upsert(_ context.Context, definition guardrails.Definition) error {
	if s.definitions == nil {
		s.definitions = make(map[string]guardrails.Definition)
	}
	s.definitions[definition.Name] = definition
	return nil
}

func (s *guardrailTestStore) UpsertMany(_ context.Context, definitions []guardrails.Definition) error {
	if s.definitions == nil {
		s.definitions = make(map[string]guardrails.Definition)
	}
	for _, definition := range definitions {
		s.definitions[definition.Name] = definition
	}
	return nil
}

func (s *guardrailTestStore) Delete(_ context.Context, name string) error {
	delete(s.definitions, name)
	return nil
}

func (s *guardrailTestStore) Close() error { return nil }

type guardrailExecutorFunc func(context.Context, *core.ChatRequest) (*core.ChatResponse, error)

func (f guardrailExecutorFunc) ChatCompletion(ctx context.Context, req *core.ChatRequest) (*core.ChatResponse, error) {
	return f(ctx, req)
}

func mustMarshalJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	return raw
}

func assertPipelineRewrite(t *testing.T, pipeline *guardrails.Pipeline, want string) {
	t.Helper()
	if pipeline == nil {
		t.Fatal("pipeline = nil, want non-nil")
	}

	msgs, err := pipeline.Process(context.Background(), []guardrails.Message{{Role: "user", Content: "John Smith"}})
	if err != nil {
		t.Fatalf("pipeline.Process() error = %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("len(msgs) = %d, want 1", len(msgs))
	}
	if msgs[0].Role != "user" {
		t.Fatalf("msgs[0].Role = %q, want user", msgs[0].Role)
	}
	if msgs[0].Content != want {
		t.Fatalf("msgs[0].Content = %q, want %q", msgs[0].Content, want)
	}
}

func TestServiceCreate_RefreshesSnapshot(t *testing.T) {
	store := &staticStore{
		versions: []Version{
			{
				ID:       "global-v1",
				Scope:    Scope{},
				ScopeKey: "global",
				Version:  1,
				Active:   true,
				Name:     "global",
				Payload: Payload{
					SchemaVersion: 1,
					Features:      FeatureFlags{Cache: true, Audit: true, Usage: true, Guardrails: false},
				},
			},
		},
	}
	service, err := NewService(store, NewCompiler(nil))
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}

	created, err := service.Create(context.Background(), CreateInput{
		Scope:    Scope{Provider: "openai"},
		Activate: true,
		Name:     "openai",
		Payload: Payload{
			SchemaVersion: 1,
			Features:      FeatureFlags{Cache: false, Audit: true, Usage: true, Guardrails: false},
		},
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if created == nil {
		t.Fatal("Create() returned nil version")
	}

	policy, err := service.Match(core.NewExecutionPlanSelector("openai", "gpt-5"))
	if err != nil {
		t.Fatalf("Match() error = %v", err)
	}
	if policy == nil {
		t.Fatal("Match() returned nil policy")
	}
	if policy.VersionID != created.ID {
		t.Fatalf("VersionID = %q, want %q", policy.VersionID, created.ID)
	}
}

func TestServiceListViews_IncludesEffectiveFeatures(t *testing.T) {
	store := &staticStore{
		versions: []Version{
			{
				ID:       "global-v1",
				Scope:    Scope{},
				ScopeKey: "global",
				Version:  1,
				Active:   true,
				Name:     "global",
				Payload: Payload{
					SchemaVersion: 1,
					Features:      FeatureFlags{Cache: true, Audit: true, Usage: true, Guardrails: true},
				},
			},
		},
	}
	service, err := NewService(store, NewCompilerWithFeatureCaps(nil, core.ExecutionFeatures{
		Cache:      false,
		Audit:      true,
		Usage:      true,
		Guardrails: false,
	}))
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}

	views, err := service.ListViews(context.Background())
	if err != nil {
		t.Fatalf("ListViews() error = %v", err)
	}
	if len(views) != 1 {
		t.Fatalf("len(views) = %d, want 1", len(views))
	}
	if views[0].ScopeType != "global" {
		t.Fatalf("ScopeType = %q, want global", views[0].ScopeType)
	}
	if views[0].EffectiveFeatures.Cache {
		t.Fatal("EffectiveFeatures.Cache = true, want false")
	}
	if views[0].EffectiveFeatures.Guardrails {
		t.Fatal("EffectiveFeatures.Guardrails = true, want false")
	}
}

func TestServiceListViews_AnnotatesCompileFailuresPerRow(t *testing.T) {
	store := &staticStore{
		versions: []Version{
			{
				ID:       "global-v1",
				Scope:    Scope{},
				ScopeKey: "global",
				Version:  1,
				Active:   true,
				Name:     "global",
				Payload: Payload{
					SchemaVersion: 1,
					Features:      FeatureFlags{Cache: true, Audit: true, Usage: true, Guardrails: false},
				},
			},
			{
				ID:       "provider-v1",
				Scope:    Scope{Provider: "openai"},
				ScopeKey: "provider:openai",
				Version:  1,
				Active:   true,
				Name:     "broken-provider",
				Payload: Payload{
					SchemaVersion: 1,
					Features:      FeatureFlags{Cache: false, Audit: true, Usage: true, Guardrails: false},
				},
			},
		},
	}
	service, err := NewService(store, &versionFailingCompiler{
		delegate: NewCompiler(nil),
		version:  "provider-v1",
		err:      errors.New("compile failed for provider-v1"),
	})
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	views, err := service.ListViews(context.Background())
	if err != nil {
		t.Fatalf("ListViews() error = %v, want nil", err)
	}
	if len(views) != 2 {
		t.Fatalf("len(views) = %d, want 2", len(views))
	}

	if views[0].ID != "global-v1" {
		t.Fatalf("views[0].ID = %q, want global-v1", views[0].ID)
	}
	if views[0].CompileError != "" {
		t.Fatalf("views[0].CompileError = %q, want empty", views[0].CompileError)
	}

	if views[1].ID != "provider-v1" {
		t.Fatalf("views[1].ID = %q, want provider-v1", views[1].ID)
	}
	if views[1].CompileError != "compile execution plan \"provider-v1\": compile failed for provider-v1" {
		t.Fatalf("views[1].CompileError = %q, want wrapped compile failure", views[1].CompileError)
	}
	if views[1].ScopeType != "provider" {
		t.Fatalf("views[1].ScopeType = %q, want provider", views[1].ScopeType)
	}
	if views[1].ScopeDisplay != "openai" {
		t.Fatalf("views[1].ScopeDisplay = %q, want openai", views[1].ScopeDisplay)
	}
}

func TestViewScopeSpecificity_PathExceedsProvider(t *testing.T) {
	if got, provider := viewScopeSpecificity("path"), viewScopeSpecificity("provider"); got <= provider {
		t.Fatalf("viewScopeSpecificity(path) = %d, want > provider specificity %d", got, provider)
	}
}

func TestServiceDeactivate_RefreshesSnapshot(t *testing.T) {
	store := &staticStore{
		versions: []Version{
			{
				ID:       "global-v1",
				Scope:    Scope{},
				ScopeKey: "global",
				Version:  1,
				Active:   true,
				Name:     "global",
				Payload: Payload{
					SchemaVersion: 1,
					Features:      FeatureFlags{Cache: true, Audit: true, Usage: true, Guardrails: false},
				},
			},
			{
				ID:       "provider-v1",
				Scope:    Scope{Provider: "openai"},
				ScopeKey: "provider:openai",
				Version:  1,
				Active:   true,
				Name:     "openai",
				Payload: Payload{
					SchemaVersion: 1,
					Features:      FeatureFlags{Cache: false, Audit: true, Usage: true, Guardrails: false},
				},
			},
		},
	}
	service, err := NewService(store, NewCompiler(nil))
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}

	if err := service.Deactivate(context.Background(), "provider-v1"); err != nil {
		t.Fatalf("Deactivate() error = %v", err)
	}

	policy, err := service.Match(core.NewExecutionPlanSelector("openai", "gpt-5"))
	if err != nil {
		t.Fatalf("Match() error = %v", err)
	}
	if policy == nil {
		t.Fatal("Match() returned nil policy")
	}
	if policy.VersionID != "global-v1" {
		t.Fatalf("VersionID = %q, want global-v1", policy.VersionID)
	}
}

func TestServiceDeactivate_RejectsGlobalWorkflow(t *testing.T) {
	store := &staticStore{
		versions: []Version{
			{
				ID:       "global-v1",
				Scope:    Scope{},
				ScopeKey: "global",
				Version:  1,
				Active:   true,
				Name:     "global",
				Payload: Payload{
					SchemaVersion: 1,
					Features:      FeatureFlags{Cache: true, Audit: true, Usage: true, Guardrails: false},
				},
			},
		},
	}
	service, err := NewService(store, NewCompiler(nil))
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}

	err = service.Deactivate(context.Background(), "global-v1")
	if err == nil {
		t.Fatal("Deactivate() error = nil, want validation error")
	}
	if !IsValidationError(err) {
		t.Fatalf("Deactivate() error = %v, want validation error", err)
	}
}

func TestServiceDeactivate_AllowsPathScopedWorkflow(t *testing.T) {
	store := &staticStore{
		versions: []Version{
			{
				ID:       "global-v1",
				Scope:    Scope{},
				ScopeKey: "global",
				Version:  1,
				Active:   true,
				Name:     "global",
				Payload: Payload{
					SchemaVersion: 1,
					Features:      FeatureFlags{Cache: true, Audit: true, Usage: true, Guardrails: false},
				},
			},
			{
				ID:       "path-v1",
				Scope:    Scope{UserPath: "/team/a"},
				ScopeKey: "path:/team/a",
				Version:  1,
				Active:   true,
				Name:     "path-only",
				Payload: Payload{
					SchemaVersion: 1,
					Features:      FeatureFlags{Cache: true, Audit: true, Usage: true, Guardrails: false},
				},
			},
		},
	}
	service, err := NewService(store, NewCompiler(nil))
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}

	if err := service.Deactivate(context.Background(), "path-v1"); err != nil {
		t.Fatalf("Deactivate() error = %v", err)
	}
	if store.versions[1].Active {
		t.Fatal("path-scoped workflow remained active after deactivation")
	}
}

func TestServiceCreateWaitsForInFlightRefreshBeforePersisting(t *testing.T) {
	store := &concurrentStore{
		versions: []Version{
			{
				ID:       "global-v1",
				Scope:    Scope{},
				ScopeKey: "global",
				Version:  1,
				Active:   true,
				Name:     "global",
				Payload: Payload{
					SchemaVersion: 1,
					Features:      FeatureFlags{Cache: true, Audit: true, Usage: true, Guardrails: false},
				},
			},
		},
		createCalled: make(chan struct{}, 1),
	}
	compiler := &blockingCompiler{
		delegate:  NewCompiler(nil),
		blockCall: 2,
		blocked:   make(chan struct{}),
		release:   make(chan struct{}),
	}
	service, err := NewService(store, compiler)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}

	refreshDone := make(chan error, 1)
	go func() {
		refreshDone <- service.Refresh(context.Background())
	}()

	<-compiler.blocked

	type createResult struct {
		version *Version
		err     error
	}
	createDone := make(chan createResult, 1)
	go func() {
		version, err := service.Create(context.Background(), CreateInput{
			Scope:    Scope{Provider: "openai"},
			Activate: true,
			Name:     "openai",
			Payload: Payload{
				SchemaVersion: 1,
				Features:      FeatureFlags{Cache: false, Audit: true, Usage: true, Guardrails: false},
			},
		})
		createDone <- createResult{version: version, err: err}
	}()

	select {
	case <-store.createCalled:
		t.Fatal("Create() persisted a new version while an older refresh was still rebuilding the snapshot")
	case <-time.After(50 * time.Millisecond):
	}

	close(compiler.release)

	if err := <-refreshDone; err != nil {
		t.Fatalf("background Refresh() error = %v", err)
	}
	result := <-createDone
	if result.err != nil {
		t.Fatalf("Create() error = %v", result.err)
	}
	if result.version == nil {
		t.Fatal("Create() returned nil version")
	}

	policy, err := service.Match(core.NewExecutionPlanSelector("openai", "gpt-5"))
	if err != nil {
		t.Fatalf("Match() error = %v", err)
	}
	if policy == nil {
		t.Fatal("Match() returned nil policy")
	}
	if policy.VersionID != result.version.ID {
		t.Fatalf("VersionID = %q, want %q", policy.VersionID, result.version.ID)
	}
}

func TestServiceCreateRejectsEmptyCompiledPreviewBeforePersisting(t *testing.T) {
	store := &concurrentStore{
		versions: []Version{
			{
				ID:       "global-v1",
				Scope:    Scope{},
				ScopeKey: "global",
				Version:  1,
				Active:   true,
				Name:     "global",
				Payload: Payload{
					SchemaVersion: 1,
					Features:      FeatureFlags{Cache: true, Audit: true, Usage: true, Guardrails: false},
				},
			},
		},
		createCalled: make(chan struct{}, 1),
	}
	service, err := NewService(store, &previewEmptyCompiler{delegate: NewCompiler(nil)})
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}

	created, err := service.Create(context.Background(), CreateInput{
		Scope:    Scope{Provider: "openai"},
		Activate: true,
		Name:     "openai",
		Payload: Payload{
			SchemaVersion: 1,
			Features:      FeatureFlags{Cache: false, Audit: true, Usage: true, Guardrails: false},
		},
	})
	if err == nil {
		t.Fatal("Create() error = nil, want validation error")
	}
	if !IsValidationError(err) {
		t.Fatalf("Create() error = %v, want validation error", err)
	}
	if err.Error() != "compiled plan is empty or missing policy" {
		t.Fatalf("Create() error = %q, want compiled plan is empty or missing policy", err.Error())
	}
	if created != nil {
		t.Fatalf("Create() version = %#v, want nil", created)
	}
	select {
	case <-store.createCalled:
		t.Fatal("Create() persisted a version even though preview compilation was empty")
	default:
	}
}

func TestServiceCreateRefreshIgnoresRequestContextCancellationAfterPersist(t *testing.T) {
	store := &contextCancelingStore{
		staticStore: staticStore{
			versions: []Version{
				{
					ID:       "global-v1",
					Scope:    Scope{},
					ScopeKey: "global",
					Version:  1,
					Active:   true,
					Name:     "global",
					Payload: Payload{
						SchemaVersion: 1,
						Features:      FeatureFlags{Cache: true, Audit: true, Usage: true, Guardrails: false},
					},
				},
			},
		},
	}
	service, err := NewService(store, NewCompiler(nil))
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	store.cancelOnCreate = cancel

	created, err := service.Create(ctx, CreateInput{
		Scope:    Scope{Provider: "openai"},
		Activate: true,
		Name:     "openai",
		Payload: Payload{
			SchemaVersion: 1,
			Features:      FeatureFlags{Cache: false, Audit: true, Usage: true, Guardrails: false},
		},
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if created == nil {
		t.Fatal("Create() returned nil version")
	}

	policy, err := service.Match(core.NewExecutionPlanSelector("openai", "gpt-5"))
	if err != nil {
		t.Fatalf("Match() error = %v", err)
	}
	if policy == nil {
		t.Fatal("Match() returned nil policy")
	}
	if policy.VersionID != created.ID {
		t.Fatalf("VersionID = %q, want %q", policy.VersionID, created.ID)
	}
}

func TestServiceCreateReturnsSuccessWhenReloadRefreshFailsAfterPersist(t *testing.T) {
	store := &refreshFailingStore{
		staticStore: staticStore{
			versions: []Version{
				{
					ID:       "global-v1",
					Scope:    Scope{},
					ScopeKey: "global",
					Version:  1,
					Active:   true,
					Name:     "global",
					Payload: Payload{
						SchemaVersion: 1,
						Features:      FeatureFlags{Cache: true, Audit: true, Usage: true, Guardrails: false},
					},
				},
			},
		},
	}
	service, err := NewService(store, NewCompiler(nil))
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}

	created, err := service.Create(context.Background(), CreateInput{
		Scope:    Scope{Provider: "openai"},
		Activate: true,
		Name:     "openai",
		Payload: Payload{
			SchemaVersion: 1,
			Features:      FeatureFlags{Cache: false, Audit: true, Usage: true, Guardrails: false},
		},
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if created == nil {
		t.Fatal("Create() returned nil version")
	}

	policy, err := service.Match(core.NewExecutionPlanSelector("openai", "gpt-5"))
	if err != nil {
		t.Fatalf("Match() error = %v", err)
	}
	if policy == nil {
		t.Fatal("Match() returned nil policy")
	}
	if policy.VersionID != created.ID {
		t.Fatalf("VersionID = %q, want %q", policy.VersionID, created.ID)
	}
}

func TestServiceDeactivateRefreshIgnoresRequestContextCancellationAfterPersist(t *testing.T) {
	store := &contextCancelingStore{
		staticStore: staticStore{
			versions: []Version{
				{
					ID:       "global-v1",
					Scope:    Scope{},
					ScopeKey: "global",
					Version:  1,
					Active:   true,
					Name:     "global",
					Payload: Payload{
						SchemaVersion: 1,
						Features:      FeatureFlags{Cache: true, Audit: true, Usage: true, Guardrails: false},
					},
				},
				{
					ID:       "provider-v1",
					Scope:    Scope{Provider: "openai"},
					ScopeKey: "provider:openai",
					Version:  1,
					Active:   true,
					Name:     "openai",
					Payload: Payload{
						SchemaVersion: 1,
						Features:      FeatureFlags{Cache: false, Audit: true, Usage: true, Guardrails: false},
					},
				},
			},
		},
	}
	service, err := NewService(store, NewCompiler(nil))
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	store.cancelOnDeactivate = cancel

	if err := service.Deactivate(ctx, "provider-v1"); err != nil {
		t.Fatalf("Deactivate() error = %v", err)
	}

	policy, err := service.Match(core.NewExecutionPlanSelector("openai", "gpt-5"))
	if err != nil {
		t.Fatalf("Match() error = %v", err)
	}
	if policy == nil {
		t.Fatal("Match() returned nil policy")
	}
	if policy.VersionID != "global-v1" {
		t.Fatalf("VersionID = %q, want global-v1", policy.VersionID)
	}
}

func TestServiceDeactivateReturnsSuccessWhenReloadRefreshFailsAfterPersist(t *testing.T) {
	store := &refreshFailingStore{
		staticStore: staticStore{
			versions: []Version{
				{
					ID:       "global-v1",
					Scope:    Scope{},
					ScopeKey: "global",
					Version:  1,
					Active:   true,
					Name:     "global",
					Payload: Payload{
						SchemaVersion: 1,
						Features:      FeatureFlags{Cache: true, Audit: true, Usage: true, Guardrails: false},
					},
				},
				{
					ID:       "provider-v1",
					Scope:    Scope{Provider: "openai"},
					ScopeKey: "provider:openai",
					Version:  1,
					Active:   true,
					Name:     "openai",
					Payload: Payload{
						SchemaVersion: 1,
						Features:      FeatureFlags{Cache: false, Audit: true, Usage: true, Guardrails: false},
					},
				},
			},
		},
	}
	service, err := NewService(store, NewCompiler(nil))
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}

	if err := service.Deactivate(context.Background(), "provider-v1"); err != nil {
		t.Fatalf("Deactivate() error = %v", err)
	}

	policy, err := service.Match(core.NewExecutionPlanSelector("openai", "gpt-5"))
	if err != nil {
		t.Fatalf("Match() error = %v", err)
	}
	if policy == nil {
		t.Fatal("Match() returned nil policy")
	}
	if policy.VersionID != "global-v1" {
		t.Fatalf("VersionID = %q, want global-v1", policy.VersionID)
	}
}
