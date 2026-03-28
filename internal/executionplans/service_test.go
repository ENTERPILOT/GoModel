package executionplans

import (
	"context"
	"testing"

	"gomodel/internal/core"
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
			copy := version
			return &copy, nil
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
		Name:        input.Name,
		Description: input.Description,
		Payload:     input.Payload,
		PlanHash:    planHash,
	}
	s.versions = append(s.versions, version)
	return &version, nil
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

	assertMatch("provider+model", core.NewExecutionPlanSelector("openai", "gpt-5"), "provider-model")
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
