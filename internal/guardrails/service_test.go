package guardrails

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"gomodel/internal/core"
)

type testStore struct {
	definitions   map[string]Definition
	listErr       error
	upsertErr     error
	upsertManyErr error
	deleteErr     error
}

func newTestStore(definitions ...Definition) *testStore {
	store := &testStore{definitions: make(map[string]Definition, len(definitions))}
	for _, definition := range definitions {
		store.definitions[definition.Name] = definition
	}
	return store
}

func (s *testStore) List(context.Context) ([]Definition, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	result := make([]Definition, 0, len(s.definitions))
	for _, definition := range s.definitions {
		result = append(result, definition)
	}
	return result, nil
}

func (s *testStore) Get(_ context.Context, name string) (*Definition, error) {
	definition, ok := s.definitions[name]
	if !ok {
		return nil, ErrNotFound
	}
	copy := definition
	return &copy, nil
}

func (s *testStore) Upsert(_ context.Context, definition Definition) error {
	if s.upsertErr != nil {
		return s.upsertErr
	}
	s.definitions[definition.Name] = definition
	return nil
}

func (s *testStore) UpsertMany(_ context.Context, definitions []Definition) error {
	if s.upsertManyErr != nil {
		return s.upsertManyErr
	}
	for _, definition := range definitions {
		s.definitions[definition.Name] = definition
	}
	return nil
}

func (s *testStore) Delete(_ context.Context, name string) error {
	if s.deleteErr != nil {
		return s.deleteErr
	}
	if _, ok := s.definitions[name]; !ok {
		return ErrNotFound
	}
	delete(s.definitions, name)
	return nil
}

func (s *testStore) Close() error { return nil }

func rawConfig(t *testing.T, value any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	return raw
}

func TestServiceRefreshBuildsPipelineFromDefinitions(t *testing.T) {
	store := newTestStore(
		Definition{
			Name: "safety",
			Type: "system_prompt",
			Config: rawConfig(t, map[string]any{
				"mode":    "inject",
				"content": "be safe",
			}),
		},
	)

	service, err := NewService(store)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}

	if got := service.Names(); len(got) != 1 || got[0] != "safety" {
		t.Fatalf("Names() = %v, want [safety]", got)
	}

	pipeline, hash, err := service.BuildPipeline([]StepReference{{Ref: "safety", Step: 10}})
	if err != nil {
		t.Fatalf("BuildPipeline() error = %v", err)
	}
	if pipeline == nil || pipeline.Len() != 1 {
		t.Fatalf("pipeline = %#v, want one entry", pipeline)
	}
	if hash == "" {
		t.Fatal("BuildPipeline() hash = empty, want non-empty")
	}

	msgs, err := pipeline.Process(context.Background(), []Message{{Role: "user", Content: "hi"}})
	if err != nil {
		t.Fatalf("Process() error = %v", err)
	}
	if len(msgs) != 2 || msgs[0].Role != "system" || msgs[0].Content != "be safe" {
		t.Fatalf("Process() messages = %#v, want injected system prompt", msgs)
	}
}

func TestServiceRefreshBuildsLLMBasedAlteringPipelineFromDefinitions(t *testing.T) {
	store := newTestStore(
		Definition{
			Name: "privacy",
			Type: "llm_based_altering",
			Config: rawConfig(t, map[string]any{
				"model": "gpt-4o-mini",
				"roles": []string{"user"},
			}),
		},
	)

	service, err := NewService(store, mockChatCompletionExecutor{
		chatFn: func(_ context.Context, req *core.ChatRequest) (*core.ChatResponse, error) {
			if req.Model != "gpt-4o-mini" {
				t.Fatalf("auxiliary model = %q, want gpt-4o-mini", req.Model)
			}
			return &core.ChatResponse{
				Choices: []core.Choice{
					{Message: core.ResponseMessage{Role: "assistant", Content: "[|---|](PERSON_1)"}},
				},
			}, nil
		},
	})
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}

	pipeline, _, err := service.BuildPipeline([]StepReference{{Ref: "privacy", Step: 10}})
	if err != nil {
		t.Fatalf("BuildPipeline() error = %v", err)
	}

	msgs, err := pipeline.Process(context.Background(), []Message{{Role: "user", Content: "John Smith"}})
	if err != nil {
		t.Fatalf("Process() error = %v", err)
	}
	if msgs[0].Content != "[|---|](PERSON_1)" {
		t.Fatalf("Process() messages = %#v, want rewritten content", msgs)
	}
}

func TestServiceRefreshNormalizesLLMBasedAlteringSelectorForViews(t *testing.T) {
	store := newTestStore(
		Definition{
			Name: "privacy",
			Type: "llm_based_altering",
			Config: rawConfig(t, map[string]any{
				"model":    "gpt-4o-mini",
				"provider": "openai",
				"roles":    []string{"user"},
			}),
		},
	)

	service, err := NewService(store)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}

	definition, ok := service.Get("privacy")
	if !ok || definition == nil {
		t.Fatal("Get(privacy) = missing, want normalized guardrail")
	}

	var cfg map[string]any
	if err := json.Unmarshal(definition.Config, &cfg); err != nil {
		t.Fatalf("json.Unmarshal(definition.Config) error = %v", err)
	}
	if cfg["model"] != "openai/gpt-4o-mini" {
		t.Fatalf("config.model = %#v, want openai/gpt-4o-mini", cfg["model"])
	}
	if _, ok := cfg["provider"]; ok {
		t.Fatalf("config.provider = %#v, want omitted after normalization", cfg["provider"])
	}
}

func TestServiceRefresh_ReturnsGatewayErrorOnStoreFailure(t *testing.T) {
	store := &testStore{
		definitions: map[string]Definition{},
		listErr:     errors.New("boom"),
	}

	service, err := NewService(store)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	err = service.Refresh(context.Background())
	if err == nil {
		t.Fatal("Refresh() error = nil, want gateway error")
	}
	var gatewayErr *core.GatewayError
	if !errors.As(err, &gatewayErr) {
		t.Fatalf("Refresh() error = %T, want *core.GatewayError", err)
	}
}

func TestServiceBuildPipeline_ReturnsGatewayErrorWhenCatalogMissing(t *testing.T) {
	service := &Service{}

	_, _, err := service.BuildPipeline([]StepReference{{Ref: "policy", Step: 10}})
	if err == nil {
		t.Fatal("BuildPipeline() error = nil, want gateway error")
	}
	var gatewayErr *core.GatewayError
	if !errors.As(err, &gatewayErr) {
		t.Fatalf("BuildPipeline() error = %T, want *core.GatewayError", err)
	}
}

func TestServiceUpsertDefinitions_UpdatesConfiguredSubsetAndPreservesCustomEntries(t *testing.T) {
	store := newTestStore(
		Definition{
			Name: "policy",
			Type: "system_prompt",
			Config: rawConfig(t, map[string]any{
				"mode":    "inject",
				"content": "old policy text",
			}),
		},
		Definition{
			Name: "custom",
			Type: "system_prompt",
			Config: rawConfig(t, map[string]any{
				"mode":    "inject",
				"content": "custom",
			}),
		},
	)
	service, err := NewService(store)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	if err := service.UpsertDefinitions(context.Background(), []Definition{
		{
			Name: "policy",
			Type: "system_prompt",
			Config: rawConfig(t, map[string]any{
				"mode":    "override",
				"content": "policy text",
			}),
		},
		{
			Name: "policy-v2",
			Type: "system_prompt",
			Config: rawConfig(t, map[string]any{
				"mode":    "inject",
				"content": "new policy",
			}),
		},
	}); err != nil {
		t.Fatalf("UpsertDefinitions() error = %v", err)
	}

	if got := service.Names(); len(got) != 3 || got[0] != "custom" || got[1] != "policy" || got[2] != "policy-v2" {
		t.Fatalf("Names() after upsert = %v, want [custom policy policy-v2]", got)
	}

	definition, ok := service.Get("policy")
	if !ok || definition == nil {
		t.Fatal("Get(policy) = missing, want updated guardrail")
	}
	var gotConfig map[string]any
	if err := json.Unmarshal(definition.Config, &gotConfig); err != nil {
		t.Fatalf("json.Unmarshal(policy.Config) error = %v", err)
	}
	if gotConfig["mode"] != "override" || gotConfig["content"] != "policy text" {
		t.Fatalf("policy.Config = %#v, want updated config", gotConfig)
	}

	if _, ok := store.definitions["custom"]; !ok {
		t.Fatal("custom guardrail missing after UpsertDefinitions(), want preserved entry")
	}
}

func TestServiceUpsertDefinitions_LeavesSnapshotUnchangedWhenPersistenceFails(t *testing.T) {
	store := newTestStore(Definition{
		Name: "policy",
		Type: "system_prompt",
		Config: rawConfig(t, map[string]any{
			"mode":    "inject",
			"content": "policy text",
		}),
	})
	service, err := NewService(store)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}

	store.upsertManyErr = errors.New("boom")
	err = service.UpsertDefinitions(context.Background(), []Definition{
		{
			Name: "policy-v2",
			Type: "system_prompt",
			Config: rawConfig(t, map[string]any{
				"mode":    "inject",
				"content": "new policy",
			}),
		},
	})
	if err == nil {
		t.Fatal("UpsertDefinitions() error = nil, want persistence failure")
	}

	if got := service.Names(); len(got) != 1 || got[0] != "policy" {
		t.Fatalf("Names() after failed UpsertDefinitions = %v, want unchanged [policy]", got)
	}
}

func TestServiceUpsertRejectsInvalidSystemPromptMode(t *testing.T) {
	store := newTestStore()
	service, err := NewService(store)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	err = service.Upsert(context.Background(), Definition{
		Name: "policy",
		Type: "system_prompt",
		Config: rawConfig(t, map[string]any{
			"mode":    "invalid",
			"content": "policy text",
		}),
	})
	if err == nil {
		t.Fatal("Upsert() error = nil, want validation error")
	}
	if !IsValidationError(err) {
		t.Fatalf("Upsert() error = %v, want validation error", err)
	}
	if len(store.definitions) != 0 {
		t.Fatalf("len(store.definitions) = %d, want 0", len(store.definitions))
	}
}

func TestServiceUpsertNormalizesUserPath(t *testing.T) {
	store := newTestStore()
	service, err := NewService(store)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	err = service.Upsert(context.Background(), Definition{
		Name:     "policy",
		Type:     "system_prompt",
		UserPath: "team/alpha",
		Config: rawConfig(t, map[string]any{
			"mode":    "inject",
			"content": "policy text",
		}),
	})
	if err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}

	definition, ok := service.Get("policy")
	if !ok || definition == nil {
		t.Fatal("Get(policy) = missing, want stored guardrail")
	}
	if definition.UserPath != "/team/alpha" {
		t.Fatalf("definition.UserPath = %q, want /team/alpha", definition.UserPath)
	}
}
