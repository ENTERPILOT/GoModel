package workflows

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/guardrails"
	"github.com/enterpilot/gomodel/internal/plugins"
	"github.com/enterpilot/gomodel/internal/plugins/builtin"
	"github.com/enterpilot/gomodel/pluginapi"
	"github.com/stretchr/testify/require"
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
	input, scopeKey, workflowHash, err := normalizeCreateInput(input)
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
		ID:           "created-global",
		Scope:        input.Scope,
		ScopeKey:     scopeKey,
		Version:      1,
		Active:       input.Activate,
		Managed:      input.Managed,
		Name:         input.Name,
		Description:  input.Description,
		Payload:      input.Payload,
		WorkflowHash: workflowHash,
	}
	s.versions = append(s.versions, version)
	return &version, nil
}

func (s *staticStore) EnsureManagedDefaultGlobal(ctx context.Context, input CreateInput, workflowHash string) (*Version, error) {
	for _, version := range s.versions {
		if !version.Active || version.ScopeKey != "global" {
			continue
		}
		if !version.Managed {
			return nil, nil
		}
		if version.Name == input.Name && version.Description == input.Description && version.WorkflowHash == workflowHash {
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
	input, scopeKey, workflowHash, err := normalizeCreateInput(input)
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
		ID:           "created-provider",
		Scope:        input.Scope,
		ScopeKey:     scopeKey,
		Version:      len(s.versions) + 1,
		Active:       input.Activate,
		Managed:      input.Managed,
		Name:         input.Name,
		Description:  input.Description,
		Payload:      input.Payload,
		WorkflowHash: workflowHash,
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

func (s *concurrentStore) EnsureManagedDefaultGlobal(_ context.Context, input CreateInput, workflowHash string) (*Version, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, version := range s.versions {
		if !version.Active || version.ScopeKey != "global" {
			continue
		}
		if !version.Managed {
			return nil, nil
		}
		if version.Name == input.Name && version.Description == input.Description && version.WorkflowHash == workflowHash {
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
	callCount atomic.Int32
	blocked   chan struct{}
	release   chan struct{}
}

func (c *blockingCompiler) Compile(version Version) (*CompiledWorkflow, error) {
	call := c.callCount.Add(1)
	if call == c.blockCall {
		close(c.blocked)
		<-c.release
	}
	return c.delegate.Compile(version)
}

type previewEmptyCompiler struct {
	delegate Compiler
}

func (c *previewEmptyCompiler) Compile(version Version) (*CompiledWorkflow, error) {
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

func (c *versionFailingCompiler) Compile(version Version) (*CompiledWorkflow, error) {
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

	service, err := NewService(store, NewCompilerWithFeatureCaps(nil, core.DefaultWorkflowFeatures()))
	require.NoError(t, err)
	err = service.Refresh(context.Background())
	require.NoError(t, err)

	assertMatch := func(name string, selector core.WorkflowSelector, wantVersionID string) {
		t.Helper()
		policy, err := service.Match(selector)
		require.NoError(t, err)
		require.NotNil(t, policy)
		require.Equal(t, wantVersionID, policy.VersionID, "%s", name)
	}

	assertMatch("provider+model+path", core.NewWorkflowSelector("openai", "gpt-5", "/team/a/user"), "provider-model-path")
	assertMatch("path beats provider+model", core.NewWorkflowSelector("openai", "gpt-5", "/team/user"), "path-team")
	assertMatch("provider+model", core.NewWorkflowSelector("openai", "gpt-5"), "provider-model")
	assertMatch("path", core.NewWorkflowSelector("anthropic", "claude-sonnet-4", "/team/a/user"), "path-team")
	assertMatch("provider", core.NewWorkflowSelector("openai", "gpt-4o"), "provider")
	assertMatch("global", core.NewWorkflowSelector("anthropic", "claude-sonnet-4"), "global")
}

func TestServiceRefresh_RejectsInvalidActiveSets(t *testing.T) {
	activeVersion := func(id string, scope Scope) Version {
		return Version{
			ID:      id,
			Scope:   scope,
			Version: 1,
			Active:  true,
			Name:    id,
			Payload: Payload{SchemaVersion: 1},
		}
	}

	tests := []struct {
		name     string
		versions []Version
		wantErr  string
	}{
		{
			name: "duplicate scope",
			versions: []Version{
				activeVersion("global", Scope{}),
				activeVersion("team-a", Scope{UserPath: "/team"}),
				activeVersion("team-b", Scope{UserPath: "/team"}),
			},
			wantErr: `duplicate active workflows for scope "path:/team": "team-a" and "team-b"`,
		},
		{
			name: "missing global",
			versions: []Version{
				activeVersion("team-a", Scope{UserPath: "/team"}),
			},
			wantErr: "missing active global workflow",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service, err := NewService(&staticStore{versions: tt.versions}, NewCompilerWithFeatureCaps(nil, core.DefaultWorkflowFeatures()))
			require.NoError(t, err)

			err = service.Refresh(context.Background())
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestServiceEnsureDefaultGlobal_CreatesWhenMissing(t *testing.T) {
	store := &staticStore{}
	service, err := NewService(store, NewCompilerWithFeatureCaps(nil, core.DefaultWorkflowFeatures()))
	require.NoError(t, err)

	err = service.EnsureDefaultGlobal(context.Background(), CreateInput{
		Activate: true,
		Name:     "default-global",
		Payload: Payload{
			SchemaVersion: 1,
			Features:      FeatureFlags{Cache: true, Audit: true, Usage: true, Guardrails: false},
		},
	})
	require.NoError(t, err)
	require.Len(t, store.versions, 1)
	got := store.versions[0].ScopeKey
	require.Equal(t, "global", got)
	require.True(t, store.versions[0].Managed)

	policy, err := service.Match(core.NewWorkflowSelector("openai", "gpt-5"))
	require.NoError(t, err)
	require.NotNil(t, policy)
	require.Equal(t, store.versions[0].ID, policy.VersionID)
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
				WorkflowHash: "stale-hash",
			},
		},
	}
	service, err := NewService(store, NewCompilerWithFeatureCaps(nil, core.DefaultWorkflowFeatures()))
	require.NoError(t, err)
	err = service.Refresh(context.Background())
	require.NoError(t, err)

	err = service.EnsureDefaultGlobal(context.Background(), CreateInput{
		Activate:    true,
		Name:        ManagedDefaultGlobalName,
		Description: ManagedDefaultGlobalDescription,
		Payload: Payload{
			SchemaVersion: 1,
			Features:      FeatureFlags{Cache: true, Audit: true, Usage: true, Guardrails: false},
		},
	})
	require.NoError(t, err)
	require.Len(t, store.versions, 2)
	require.False(t, store.versions[0].Active)
	require.True(t, store.versions[1].Active)
	require.True(t, store.versions[1].Managed)
	require.True(t, store.versions[1].Payload.Features.Cache)
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
				WorkflowHash: "custom-hash",
			},
		},
	}
	service, err := NewService(store, NewCompilerWithFeatureCaps(nil, core.DefaultWorkflowFeatures()))
	require.NoError(t, err)
	err = service.Refresh(context.Background())
	require.NoError(t, err)

	err = service.EnsureDefaultGlobal(context.Background(), CreateInput{
		Activate:    true,
		Name:        ManagedDefaultGlobalName,
		Description: ManagedDefaultGlobalDescription,
		Payload: Payload{
			SchemaVersion: 1,
			Features:      FeatureFlags{Cache: true, Audit: true, Usage: true, Guardrails: false},
		},
	})
	require.NoError(t, err)
	require.Len(t, store.versions, 1)
	require.Equal(t, "custom-global", store.versions[0].Name)
	require.True(t, store.versions[0].Active, "store.versions[0] = %#v, want unchanged active custom global", store.versions[0])
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
				WorkflowHash: "custom-hash",
			},
		},
	}
	service, err := NewService(store, NewCompilerWithFeatureCaps(nil, core.DefaultWorkflowFeatures()))
	require.NoError(t, err)

	err = service.EnsureDefaultGlobal(context.Background(), CreateInput{
		Activate:    true,
		Name:        ManagedDefaultGlobalName,
		Description: ManagedDefaultGlobalDescription,
		Payload: Payload{
			SchemaVersion: 1,
			Features:      FeatureFlags{Cache: true, Audit: true, Usage: true, Guardrails: false},
		},
	})
	require.NoError(t, err)

	policy, err := service.Match(core.NewWorkflowSelector("openai", "gpt-5"))
	require.NoError(t, err)
	require.NotNil(t, policy)
	require.Equal(t, "global-v1", policy.VersionID)
}

func TestServiceEnsureDefaultGlobal_ValidatesBeforeStoreMutation(t *testing.T) {
	store := &concurrentStore{
		createCalled: make(chan struct{}, 1),
	}
	service, err := NewService(store, &previewEmptyCompiler{delegate: NewCompilerWithFeatureCaps(nil, core.DefaultWorkflowFeatures())})
	require.NoError(t, err)

	err = service.EnsureDefaultGlobal(context.Background(), CreateInput{
		Activate:    true,
		Name:        ManagedDefaultGlobalName,
		Description: ManagedDefaultGlobalDescription,
		Payload: Payload{
			SchemaVersion: 1,
			Features:      FeatureFlags{Cache: true, Audit: true, Usage: true, Guardrails: false},
		},
	})
	require.Error(t, err)
	require.True(t, IsValidationError(err))
	require.Empty(t, store.versions)

	select {
	case <-store.createCalled:
		t.Fatal("EnsureDefaultGlobal() mutated store before validation")
	default:
	}
}

func TestServiceRefresh_CompiledChainsFollowChatCompleterSwap(t *testing.T) {
	guardrailService := newGuardrailService(t, guardrailExecutorFunc(func(_ context.Context, _ *core.ChatRequest) (*core.ChatResponse, error) {
		return &core.ChatResponse{
			Choices: []core.Choice{
				{Message: core.ResponseMessage{Role: "assistant", Content: "[|---|](PERSON_1)"}, FinishReason: "stop"},
			},
		}, nil
	}), guardrails.Definition{
		Name: "privacy",
		Type: "llm_based_altering",
		Config: mustMarshalJSON(t, struct {
			Model string   `json:"model"`
			Roles []string `json:"roles"`
		}{
			Model: "gpt-4o-mini",
			Roles: []string{"user"},
		}),
	})

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
					SchemaVersion: 2,
					Features: FeatureFlags{
						Cache:      false,
						Audit:      true,
						Usage:      true,
						Guardrails: true,
					},
					Steps: []Step{
						{Ref: "privacy", Phase: PhasePrompt, Step: 10},
						{Ref: "privacy", Phase: PhaseResponse, Step: 10},
					},
				},
			},
		},
	}
	service, err := NewService(store, NewCompilerWithFeatureCaps(guardrailService, core.DefaultWorkflowFeatures()))
	require.NoError(t, err)
	err = service.Refresh(context.Background())
	require.NoError(t, err)

	selector := core.NewWorkflowSelector("", "", "/")
	policy, err := service.Match(selector)
	require.NoError(t, err)

	// With a response chain the cache key covers every phase, so it differs
	// from the prompt hash alone.
	require.NotEmpty(t, policy.GuardrailsHash)
	require.NotEqual(t, policy.GuardrailsHash, policy.ChainHashes["prompt"])
	require.NotEmpty(t, policy.ChainHashes["response"], "policy hashes = %q / %v", policy.GuardrailsHash, policy.ChainHashes)

	workflow := &core.Workflow{Policy: policy}
	chains := service.ChainsForWorkflow(workflow)
	require.NotNil(t, chains)
	require.Equal(t, 1, chains.Prompt.Len())
	require.Equal(t, 1, chains.Response.Len())
	require.Same(t, chains, service.ChainsForContext(core.WithWorkflow(context.Background(), workflow)))
	require.Nil(t, service.ChainsForWorkflow(&core.Workflow{Policy: &core.ResolvedWorkflowPolicy{VersionID: "missing"}}))

	assertChainRewrite(t, chains.Prompt, "[|---|](PERSON_1)")

	guardrailService.SetChatCompleter(guardrailExecutorFunc(func(_ context.Context, _ *core.ChatRequest) (*core.ChatResponse, error) {
		return &core.ChatResponse{
			Choices: []core.Choice{
				{Message: core.ResponseMessage{Role: "assistant", Content: "[|---|](PERSON_2)"}, FinishReason: "stop"},
			},
		}, nil
	}))
	// Instances see the swapped executor without a workflow refresh.
	assertChainRewrite(t, service.ChainsForWorkflow(workflow).Prompt, "[|---|](PERSON_2)")
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
	require.NoError(t, err)

	return raw
}

// newGuardrailService builds a guardrails service over the built-in plugins.
// globalVersionV1 is the active v1 global workflow the service tests start
// from, with the given feature flags.
func globalVersionV1(features FeatureFlags) Version {
	return Version{
		ID:       "global-v1",
		Scope:    Scope{},
		ScopeKey: "global",
		Version:  1,
		Active:   true,
		Name:     "global",
		Payload:  Payload{SchemaVersion: 1, Features: features},
	}
}

func newGuardrailService(t *testing.T, chat plugins.ChatCompleter, definitions ...guardrails.Definition) *guardrails.Service {
	t.Helper()
	catalog := plugins.NewCatalog()
	for _, factory := range builtin.All() {
		err := catalog.Register(factory, plugins.SourceBuiltin)
		require.NoError(t, err)
	}
	store := &guardrailTestStore{definitions: map[string]guardrails.Definition{}}
	for _, definition := range definitions {
		store.definitions[definition.Name] = definition
	}
	service, err := guardrails.NewService(store, catalog, plugins.HostDeps{Chat: chat})
	require.NoError(t, err)
	err = service.Refresh(context.Background())
	require.NoError(t, err)

	return service
}

func assertChainRewrite(t *testing.T, chain *plugins.Chain, want string) {
	t.Helper()
	require.False(t, chain.Empty())

	msg := pluginapi.TextMessage(pluginapi.RoleUser, "John Smith")
	msg.ID = "m0"
	prompt := &pluginapi.Prompt{Messages: []pluginapi.Message{msg}}
	prompt.Reset()
	x := plugins.NewRequestState().NewExchange(context.Background(), pluginapi.Meta{})
	x.Prompt = prompt
	_, err := chain.RunPrompt(context.Background(), x)
	require.NoError(t, err)
	require.Len(t, prompt.Messages, 1)
	require.Equal(t, pluginapi.RoleUser, prompt.Messages[0].Role)
	got := prompt.Messages[0].Text()
	require.Equal(t, want, got)
}

func TestServiceCreate_RefreshesSnapshot(t *testing.T) {
	store := &staticStore{
		versions: []Version{
			globalVersionV1(FeatureFlags{Cache: true, Audit: true, Usage: true, Guardrails: false}),
		},
	}
	service, err := NewService(store, NewCompilerWithFeatureCaps(nil, core.DefaultWorkflowFeatures()))
	require.NoError(t, err)
	err = service.Refresh(context.Background())
	require.NoError(t, err)

	created, err := service.Create(context.Background(), CreateInput{
		Scope:    Scope{Provider: "openai"},
		Activate: true,
		Name:     "openai",
		Payload: Payload{
			SchemaVersion: 1,
			Features:      FeatureFlags{Cache: false, Audit: true, Usage: true, Guardrails: false},
		},
	})
	require.NoError(t, err)
	require.NotNil(t, created)

	policy, err := service.Match(core.NewWorkflowSelector("openai", "gpt-5"))
	require.NoError(t, err)
	require.NotNil(t, policy)
	require.Equal(t, created.ID, policy.VersionID)
}

func TestServiceListViews_IncludesEffectiveFeatures(t *testing.T) {
	store := &staticStore{
		versions: []Version{
			globalVersionV1(FeatureFlags{Cache: true, Audit: true, Usage: true, Guardrails: true}),
		},
	}
	service, err := NewService(store, NewCompilerWithFeatureCaps(nil, core.WorkflowFeatures{
		Cache:      false,
		Audit:      true,
		Usage:      true,
		Guardrails: false,
	}))
	require.NoError(t, err)
	err = service.Refresh(context.Background())
	require.NoError(t, err)

	views, err := service.ListViews(context.Background())
	require.NoError(t, err)
	require.Len(t, views, 1)
	require.Equal(t, "global", views[0].ScopeType)
	require.False(t, views[0].EffectiveFeatures.Cache)
	require.False(t, views[0].EffectiveFeatures.Guardrails)

	rawView, err := json.Marshal(views[0])
	require.NoError(t, err)

	var response map[string]any
	err = json.Unmarshal(rawView, &response)
	require.NoError(t, err)
	_, ok := response["scope_key"]
	require.False(t, ok, "view JSON exposed storage-only scope_key: %s", rawView)
}

func TestServiceListViews_AnnotatesCompileFailuresPerRow(t *testing.T) {
	store := &staticStore{
		versions: []Version{
			globalVersionV1(FeatureFlags{Cache: true, Audit: true, Usage: true, Guardrails: false}),
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
		delegate: NewCompilerWithFeatureCaps(nil, core.DefaultWorkflowFeatures()),
		version:  "provider-v1",
		err:      errors.New("compile failed for provider-v1"),
	})
	require.NoError(t, err)

	views, err := service.ListViews(context.Background())
	require.NoError(t, err)
	require.Len(t, views, 2)
	require.Equal(t, "global-v1", views[0].ID)
	require.Empty(t, views[0].CompileError)
	require.Equal(t, "provider-v1", views[1].ID)
	require.Equal(t, "compile workflow \"provider-v1\": compile failed for provider-v1", views[1].CompileError)
	require.Equal(t, "provider", views[1].ScopeType)
	require.Equal(t, "openai", views[1].ScopeDisplay)
}

func TestViewScopeSpecificity_PathExceedsProvider(t *testing.T) {
	require.Greater(t, viewScopeSpecificity("path"), viewScopeSpecificity("provider"))
}

func TestServiceDeactivate_RefreshesSnapshot(t *testing.T) {
	store := &staticStore{
		versions: []Version{
			globalVersionV1(FeatureFlags{Cache: true, Audit: true, Usage: true, Guardrails: false}),
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
	service, err := NewService(store, NewCompilerWithFeatureCaps(nil, core.DefaultWorkflowFeatures()))
	require.NoError(t, err)
	err = service.Refresh(context.Background())
	require.NoError(t, err)
	err = service.Deactivate(context.Background(), "provider-v1")
	require.NoError(t, err)

	policy, err := service.Match(core.NewWorkflowSelector("openai", "gpt-5"))
	require.NoError(t, err)
	require.NotNil(t, policy)
	require.Equal(t, "global-v1", policy.VersionID)
}

func TestServiceDeactivate_RejectsGlobalWorkflow(t *testing.T) {
	store := &staticStore{
		versions: []Version{
			globalVersionV1(FeatureFlags{Cache: true, Audit: true, Usage: true, Guardrails: false}),
		},
	}
	service, err := NewService(store, NewCompilerWithFeatureCaps(nil, core.DefaultWorkflowFeatures()))
	require.NoError(t, err)
	err = service.Refresh(context.Background())
	require.NoError(t, err)

	err = service.Deactivate(context.Background(), "global-v1")
	require.Error(t, err)
	require.True(t, IsValidationError(err))
}

func TestServiceDeactivate_AllowsPathScopedWorkflow(t *testing.T) {
	store := &staticStore{
		versions: []Version{
			globalVersionV1(FeatureFlags{Cache: true, Audit: true, Usage: true, Guardrails: false}),
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
	service, err := NewService(store, NewCompilerWithFeatureCaps(nil, core.DefaultWorkflowFeatures()))
	require.NoError(t, err)
	err = service.Refresh(context.Background())
	require.NoError(t, err)
	err = service.Deactivate(context.Background(), "path-v1")
	require.NoError(t, err)
	require.False(t, store.versions[1].Active)
}

func TestServiceCreateWaitsForInFlightRefreshBeforePersisting(t *testing.T) {
	store := &concurrentStore{
		versions: []Version{
			globalVersionV1(FeatureFlags{Cache: true, Audit: true, Usage: true, Guardrails: false}),
		},
		createCalled: make(chan struct{}, 1),
	}
	compiler := &blockingCompiler{
		delegate:  NewCompilerWithFeatureCaps(nil, core.DefaultWorkflowFeatures()),
		blockCall: 2,
		blocked:   make(chan struct{}),
		release:   make(chan struct{}),
	}
	service, err := NewService(store, compiler)
	require.NoError(t, err)
	err = service.Refresh(context.Background())
	require.NoError(t, err)

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
	err = <-refreshDone
	require.NoError(t, err)

	result := <-createDone
	require.NoError(t, result.err)
	require.NotNil(t, result.version)

	policy, err := service.Match(core.NewWorkflowSelector("openai", "gpt-5"))
	require.NoError(t, err)
	require.NotNil(t, policy)
	require.Equal(t, result.version.ID, policy.VersionID)
}

func TestServiceCreateRejectsEmptyCompiledPreviewBeforePersisting(t *testing.T) {
	store := &concurrentStore{
		versions: []Version{
			globalVersionV1(FeatureFlags{Cache: true, Audit: true, Usage: true, Guardrails: false}),
		},
		createCalled: make(chan struct{}, 1),
	}
	service, err := NewService(store, &previewEmptyCompiler{delegate: NewCompilerWithFeatureCaps(nil, core.DefaultWorkflowFeatures())})
	require.NoError(t, err)
	err = service.Refresh(context.Background())
	require.NoError(t, err)

	created, err := service.Create(context.Background(), CreateInput{
		Scope:    Scope{Provider: "openai"},
		Activate: true,
		Name:     "openai",
		Payload: Payload{
			SchemaVersion: 1,
			Features:      FeatureFlags{Cache: false, Audit: true, Usage: true, Guardrails: false},
		},
	})
	require.Error(t, err)
	require.True(t, IsValidationError(err))
	require.Equal(t, "compiled workflow is empty or missing policy", err.Error())
	require.Nil(t, created)

	select {
	case <-store.createCalled:
		t.Fatal("Create() persisted a version even though preview compilation was empty")
	default:
	}
}

func TestServiceCreateRefreshIgnoresRequestContextCancellationAfterPersist(t *testing.T) {
	store := &contextCancelingStore{
		versions: []Version{
			globalVersionV1(FeatureFlags{Cache: true, Audit: true, Usage: true, Guardrails: false}),
		},
	}
	service, err := NewService(store, NewCompilerWithFeatureCaps(nil, core.DefaultWorkflowFeatures()))
	require.NoError(t, err)
	err = service.Refresh(context.Background())
	require.NoError(t, err)

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
	require.NoError(t, err)
	require.NotNil(t, created)

	policy, err := service.Match(core.NewWorkflowSelector("openai", "gpt-5"))
	require.NoError(t, err)
	require.NotNil(t, policy)
	require.Equal(t, created.ID, policy.VersionID)
}

func TestServiceCreateReturnsSuccessWhenReloadRefreshFailsAfterPersist(t *testing.T) {
	store := &refreshFailingStore{
		versions: []Version{
			globalVersionV1(FeatureFlags{Cache: true, Audit: true, Usage: true, Guardrails: false}),
		},
	}
	service, err := NewService(store, NewCompilerWithFeatureCaps(nil, core.DefaultWorkflowFeatures()))
	require.NoError(t, err)
	err = service.Refresh(context.Background())
	require.NoError(t, err)

	created, err := service.Create(context.Background(), CreateInput{
		Scope:    Scope{Provider: "openai"},
		Activate: true,
		Name:     "openai",
		Payload: Payload{
			SchemaVersion: 1,
			Features:      FeatureFlags{Cache: false, Audit: true, Usage: true, Guardrails: false},
		},
	})
	require.NoError(t, err)
	require.NotNil(t, created)

	policy, err := service.Match(core.NewWorkflowSelector("openai", "gpt-5"))
	require.NoError(t, err)
	require.NotNil(t, policy)
	require.Equal(t, created.ID, policy.VersionID)
}

func TestServiceDeactivateRefreshIgnoresRequestContextCancellationAfterPersist(t *testing.T) {
	store := &contextCancelingStore{
		versions: []Version{
			globalVersionV1(FeatureFlags{Cache: true, Audit: true, Usage: true, Guardrails: false}),
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
	service, err := NewService(store, NewCompilerWithFeatureCaps(nil, core.DefaultWorkflowFeatures()))
	require.NoError(t, err)
	err = service.Refresh(context.Background())
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	store.cancelOnDeactivate = cancel
	err = service.Deactivate(ctx, "provider-v1")
	require.NoError(t, err)

	policy, err := service.Match(core.NewWorkflowSelector("openai", "gpt-5"))
	require.NoError(t, err)
	require.NotNil(t, policy)
	require.Equal(t, "global-v1", policy.VersionID)
}

func TestServiceDeactivateReturnsSuccessWhenReloadRefreshFailsAfterPersist(t *testing.T) {
	store := &refreshFailingStore{
		versions: []Version{
			globalVersionV1(FeatureFlags{Cache: true, Audit: true, Usage: true, Guardrails: false}),
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
	service, err := NewService(store, NewCompilerWithFeatureCaps(nil, core.DefaultWorkflowFeatures()))
	require.NoError(t, err)
	err = service.Refresh(context.Background())
	require.NoError(t, err)
	err = service.Deactivate(context.Background(), "provider-v1")
	require.NoError(t, err)

	policy, err := service.Match(core.NewWorkflowSelector("openai", "gpt-5"))
	require.NoError(t, err)
	require.NotNil(t, policy)
	require.Equal(t, "global-v1", policy.VersionID)
}
