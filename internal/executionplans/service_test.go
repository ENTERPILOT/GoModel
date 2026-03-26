package executionplans

import (
	"context"
	"testing"

	"gomodel/internal/core"
)

type staticStore struct {
	versions []Version
}

func (s *staticStore) ListActive(context.Context) ([]Version, error) { return s.versions, nil }
func (s *staticStore) Get(context.Context, string) (*Version, error) { return nil, ErrNotFound }
func (s *staticStore) Create(_ context.Context, input CreateInput) (*Version, error) {
	input, scopeKey, planHash, err := normalizeCreateInput(input)
	if err != nil {
		return nil, err
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
