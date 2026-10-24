package guardrails

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/plugins"
	"github.com/enterpilot/gomodel/internal/plugins/builtin"
	"github.com/enterpilot/gomodel/pluginapi"
	"github.com/stretchr/testify/require"
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
	require.NoError(t, err)

	return raw
}

type chatFunc func(ctx context.Context, req *core.ChatRequest) (*core.ChatResponse, error)

func (f chatFunc) ChatCompletion(ctx context.Context, req *core.ChatRequest) (*core.ChatResponse, error) {
	return f(ctx, req)
}

func replyChat(text string) chatFunc {
	return func(_ context.Context, req *core.ChatRequest) (*core.ChatResponse, error) {
		return &core.ChatResponse{Model: req.Model, Choices: []core.Choice{{Message: core.ResponseMessage{Role: "assistant", Content: text}, FinishReason: "stop"}}}, nil
	}
}

// secretPlugin is a test plugin type with a secret field and a response hook.
type secretPlugin struct{ config json.RawMessage }

func (p *secretPlugin) Manifest() pluginapi.Manifest {
	return pluginapi.Manifest{
		Name:  "secret_check",
		Kinds: []pluginapi.Kind{pluginapi.KindResponse},
		ConfigSchema: []pluginapi.Field{
			{Key: "api_key", Input: pluginapi.InputSecret, Required: true},
			{Key: "threshold", Input: pluginapi.InputNumber, Default: 0.5},
		},
	}
}

func (p *secretPlugin) Init(_ context.Context, config json.RawMessage, _ pluginapi.Host) error {
	p.config = config
	return nil
}

func (p *secretPlugin) Close(context.Context) error { return nil }

func (p *secretPlugin) OnResponse(context.Context, *pluginapi.Exchange) (pluginapi.Decision, error) {
	return pluginapi.Allow(), nil
}

func testCatalog(t *testing.T) *plugins.Catalog {
	t.Helper()
	catalog := plugins.NewCatalog()
	for _, factory := range builtin.All() {
		err := catalog.Register(factory, plugins.SourceBuiltin)
		require.NoError(t, err)
	}
	err := catalog.Register(func() pluginapi.Plugin { return &secretPlugin{} }, plugins.SourceRegistered)
	require.NoError(t, err)

	return catalog
}

func newService(t *testing.T, store Store, chat plugins.ChatCompleter) *Service {
	t.Helper()
	service, err := NewService(store, testCatalog(t), plugins.HostDeps{Chat: chat})
	require.NoError(t, err)
	err = service.Refresh(context.Background())
	require.NoError(t, err)

	return service
}

func systemPromptDefinition(name, content string) Definition {
	return Definition{Name: name, Type: "system_prompt", Config: json.RawMessage(`{"mode":"inject","content":"` + content + `"}`)}
}

func runPrompt(t *testing.T, chain *plugins.Chain, text string) *pluginapi.Prompt {
	t.Helper()
	msg := pluginapi.TextMessage(pluginapi.RoleUser, text)
	msg.ID = "m0"
	prompt := &pluginapi.Prompt{Messages: []pluginapi.Message{msg}}
	prompt.Reset()
	x := plugins.NewRequestState().NewExchange(context.Background(), pluginapi.Meta{})
	x.Prompt = prompt
	_, err := chain.RunPrompt(context.Background(), x)
	require.NoError(t, err)

	return prompt
}

func TestServiceRefreshBuildsChainsFromDefinitions(t *testing.T) {
	service := newService(t, newTestStore(systemPromptDefinition("safety", "be safe")), nil)
	got := service.Names()
	require.Len(t, got, 1)
	require.Equal(t, "safety", got[0])

	chains, err := service.BuildChains([]StepReference{{Ref: "safety", Step: 10}})
	require.NoError(t, err)
	require.Equal(t, 1, chains.Prompt.Len())
	require.NotEmpty(t, chains.Prompt.Hash)
	require.True(t, chains.Response.Empty(), "chains = %+v", chains)

	prompt := runPrompt(t, chains.Prompt, "hi")
	require.Len(t, prompt.Messages, 2)
	require.Equal(t, pluginapi.RoleSystem, prompt.Messages[0].Role)
	require.Equal(t, "be safe", prompt.Messages[0].Text())
	chains, err = service.BuildChains(nil)
	require.NoError(t, err)
	require.Nil(t, chains)
}

func TestServiceBuildChainsErrors(t *testing.T) {
	service := newService(t, newTestStore(systemPromptDefinition("safety", "x")), nil)
	tests := []struct {
		name  string
		steps []StepReference
		want  string
	}{
		{"unknown ref", []StepReference{{Ref: "missing", Step: 1}}, "unknown guardrail ref"},
		{"unsupported phase", []StepReference{{Ref: "safety", Phase: pluginapi.KindResponse, Step: 1}}, "does not support the response phase"},
		{"invalid phase", []StepReference{{Ref: "safety", Phase: "route", Step: 1}}, "unknown guardrail phase"},
		{"empty ref", []StepReference{{Ref: " ", Step: 1}}, "ref is required"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := service.BuildChains(tt.steps)
			require.Error(t, err)
			require.Contains(t, err.Error(), tt.want)
		})
	}
}

func TestServiceLLMBasedAlteringUsesChatCompleter(t *testing.T) {
	store := newTestStore(Definition{
		Name:   "privacy",
		Type:   "llm_based_altering",
		Config: rawConfig(t, map[string]any{"model": "gpt-4o-mini", "provider": "openai", "roles": []string{"user"}}),
	})
	var captured *core.ChatRequest
	service := newService(t, store, chatFunc(func(ctx context.Context, req *core.ChatRequest) (*core.ChatResponse, error) {
		captured = req
		return replyChat("[|---|](PERSON_1)")(ctx, req)
	}))
	chains, err := service.BuildChains([]StepReference{{Ref: "privacy", Step: 10}, {Ref: "privacy", Phase: pluginapi.KindResponse, Step: 10}})
	require.NoError(t, err)
	require.Equal(t, 1, chains.Response.Len())

	prompt := runPrompt(t, chains.Prompt, "John Smith")
	require.Equal(t, "[|---|](PERSON_1)", prompt.Messages[0].Text())
	require.NotNil(t, captured)
	require.Equal(t, "openai/gpt-4o-mini", captured.Model)

	service.SetChatCompleter(replyChat("[|---|](PERSON_2)"))
	prompt = runPrompt(t, chains.Prompt, "Jane")
	require.Equal(t, "[|---|](PERSON_2)", prompt.Messages[0].Text())

	view, ok := service.GetView("privacy")
	require.True(t, ok)
	require.Equal(t, "openai/gpt-4o-mini • user • default prompt", view.Summary)
	require.Equal(t, "prompt,response", strings.Join(view.Phases, ","), "view = %+v", view)
}

func TestServiceRefreshReturnsGatewayErrorOnStoreFailure(t *testing.T) {
	store := newTestStore()
	store.listErr = errors.New("db down")
	service, err := NewService(store, testCatalog(t), plugins.HostDeps{})
	require.NoError(t, err)

	err = service.Refresh(context.Background())
	var gatewayErr *core.GatewayError
	require.ErrorAs(t, err, &gatewayErr)
	require.Equal(t, 502, gatewayErr.HTTPStatusCode())
}

func TestServiceUpsertValidation(t *testing.T) {
	tests := []struct {
		name string
		def  Definition
		want string
	}{
		{"invalid mode", Definition{Name: "p", Type: "system_prompt", Config: json.RawMessage(`{"mode":"weird","content":"x"}`)}, "not one of the allowed options"},
		{"missing content", Definition{Name: "p", Type: "system_prompt", Config: json.RawMessage(`{"mode":"inject"}`)}, "is required"},
		{"unknown type", Definition{Name: "p", Type: "nope", Config: json.RawMessage(`{}`)}, "unknown guardrail type"},
		{"unknown key", Definition{Name: "p", Type: "system_prompt", Config: json.RawMessage(`{"content":"x","extra":1}`)}, "unknown config key"},
		{"bad fail mode", Definition{Name: "p", Type: "system_prompt", FailMode: "maybe", Config: json.RawMessage(`{"content":"x"}`)}, "invalid fail_mode"},
		{"negative timeout", Definition{Name: "p", Type: "system_prompt", TimeoutMS: -1, Config: json.RawMessage(`{"content":"x"}`)}, "timeout_ms"},
		{"name with slash", Definition{Name: "a/b", Type: "system_prompt", Config: json.RawMessage(`{"content":"x"}`)}, "cannot contain"},
		{"init rejects provider conflict", Definition{Name: "p", Type: "llm_based_altering", Config: json.RawMessage(`{"model":"openai/x","provider":"azure"}`)}, "conflicts"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := newService(t, newTestStore(), nil)
			err := service.Upsert(context.Background(), tt.def)
			require.Error(t, err)
			require.Contains(t, err.Error(), tt.want)
			require.True(t, IsValidationError(err))
			require.Equal(t, 0, service.Len())
		})
	}
}

func TestServiceUpsertNormalizesAndStoresFailModeAndTimeout(t *testing.T) {
	store := newTestStore()
	service := newService(t, store, nil)
	err := service.Upsert(context.Background(), Definition{
		Name:      " safety ",
		Type:      "System-Prompt",
		UserPath:  "team/alpha",
		FailMode:  "Open",
		TimeoutMS: 250,
		Config:    json.RawMessage(`{"content":" be safe ","mode":""}`),
	})
	require.NoError(t, err)

	stored := store.definitions["safety"]
	require.Equal(t, "system_prompt", stored.Type)
	require.Equal(t, "/team/alpha", stored.UserPath)
	require.Equal(t, "open", stored.FailMode)
	require.Equal(t, 250, stored.TimeoutMS, "stored = %+v", stored)
	require.Equal(t, `{"content":"be safe","mode":"inject"}`, string(stored.Config), "stored config = %s", stored.Config)

	chains, err := service.BuildChains([]StepReference{{Ref: "safety", Step: 5}})
	require.NoError(t, err)

	inst := chains.Prompt.Instances()[0]
	require.Equal(t, plugins.FailOpen, inst.FailMode)
	require.Equal(t, int64(250), inst.Timeout.Milliseconds(), "instance = %+v", inst)
}

func TestServiceSecretsRedactedMergedAndCleared(t *testing.T) {
	store := newTestStore()
	service := newService(t, store, nil)
	ctx := context.Background()
	err := service.Upsert(ctx, Definition{Name: "sec", Type: "secret_check", Config: json.RawMessage(`{"api_key":"s3cret"}`)})
	require.NoError(t, err)
	require.Equal(t, `{"api_key":"s3cret","threshold":0.5}`, string(store.definitions["sec"].Config), "stored = %s", store.definitions["sec"].Config)

	def, _ := service.Get("sec")
	require.Equal(t, `{"api_key":"********","threshold":0.5}`, string(def.Config), "Get() config = %s, want redacted", def.Config)
	views := service.ListViews()
	require.Equal(t, `{"api_key":"********","threshold":0.5}`, string(views[0].Config))
	require.Equal(t, "response", views[0].Phases[0], "ListViews() = %+v", views)
	err = // Masked secret keeps the stored value; other fields are replaced.
		service.Upsert(ctx, Definition{Name: "sec", Type: "secret_check", Config: json.RawMessage(`{"api_key":"********","threshold":1}`)})
	require.NoError(t, err)
	require.Equal(t, `{"api_key":"s3cret","threshold":1}`, string(store.definitions["sec"].Config), "stored after mask = %s", store.definitions["sec"].Config)
	err = // Omitted fail_mode resets to the default.
		service.Upsert(ctx, Definition{Name: "sec", Type: "secret_check", FailMode: "open", Config: json.RawMessage(`{"api_key":"********"}`)})
	require.NoError(t, err)
	err = service.Upsert(ctx, Definition{Name: "sec", Type: "secret_check", Config: json.RawMessage(`{"api_key":"********"}`)})
	require.NoError(t, err)
	require.Empty(t, store.definitions["sec"].FailMode)

	// An empty secret clears it, which the required check rejects.
	err = service.Upsert(ctx, Definition{Name: "sec", Type: "secret_check", Config: json.RawMessage(`{"api_key":""}`)})
	require.Error(t, err)
	require.Contains(t, err.Error(), "required")
}

func TestServiceTypeDefinitions(t *testing.T) {
	service := newService(t, newTestStore(), nil)
	defs := service.TypeDefinitions()
	byType := map[string]TypeDefinition{}
	for _, def := range defs {
		byType[def.Type] = def
	}
	sys, ok := byType["system_prompt"]
	require.True(t, ok, "system_prompt missing from %+v", defs)
	require.Equal(t, "System Prompt", sys.Label)
	require.Equal(t, "builtin", sys.Source)
	require.True(t, sys.Mutates)
	require.Equal(t, "prompt", strings.Join(sys.Phases, ","), "system_prompt = %+v", sys)
	require.Equal(t, `{"content":"","mode":"inject"}`, string(sys.Defaults), "defaults = %s", sys.Defaults)
	require.Len(t, sys.Fields, 2)
	require.Equal(t, "mode", sys.Fields[0].Key)
	require.Equal(t, "inject", sys.Fields[0].Default)
	require.Len(t, sys.Fields[0].Options, 3)

	llm := byType["llm_based_altering"]
	require.Equal(t, "LLM Based Altering", llm.Label)
	require.Equal(t, "prompt,response", strings.Join(llm.Phases, ","), "llm = %+v", llm)

	var defaults map[string]any
	err := json.Unmarshal(llm.Defaults, &defaults)
	require.NoError(t, err)
	require.Equal(t, float64(4096), defaults["max_tokens"], "llm defaults = %s (%v)", llm.Defaults, err)
	sec := byType["secret_check"]
	require.Equal(t, "registered", sec.Source)
	require.Equal(t, "secret", sec.Fields[0].Input, "secret_check = %+v", sec)
}

func TestServiceUpsertDefinitionsUpdatesSubsetAndPreservesCustomEntries(t *testing.T) {
	store := newTestStore(systemPromptDefinition("custom", "keep me"), systemPromptDefinition("seeded", "old"))
	service := newService(t, store, nil)
	err := service.UpsertDefinitions(context.Background(), []Definition{systemPromptDefinition("seeded", "new")})
	require.NoError(t, err)
	got := service.Names()
	require.Equal(t, "custom,seeded", strings.Join(got, ","), "Names() = %v", got)
	def, _ := service.Get("seeded")
	require.Contains(t, string(def.Config), "new", "seeded config = %s", def.Config)
	err = service.UpsertDefinitions(context.Background(), nil)
	require.NoError(t, err)
}

func TestServiceMutationsLeaveSnapshotUnchangedWhenPersistenceFails(t *testing.T) {
	store := newTestStore(systemPromptDefinition("a", "x"))
	service := newService(t, store, nil)
	store.upsertManyErr = errors.New("disk full")
	store.upsertErr = errors.New("disk full")
	store.deleteErr = errors.New("disk full")
	require.Error(t, service.UpsertDefinitions(context.Background(), []Definition{systemPromptDefinition("b", "y")}))
	require.Error(t, service.Upsert(context.Background(), systemPromptDefinition("c", "z")))
	require.Error(t, service.Delete(context.Background(), "a"))
	got := service.Names()
	require.Equal(t, "a", strings.Join(got, ","), "Names() = %v, want [a]", got)

	store.deleteErr = nil
	err := service.Delete(context.Background(), "a")
	require.NoError(t, err)
	require.Equal(t, 0, service.Len())
	require.Error(t, service.Delete(context.Background(), " "))
}

func TestServiceRejectsSecondInstanceOfSingleInstancePlugin(t *testing.T) {
	catalog := plugins.NewCatalog()
	shared := &secretPlugin{}
	err := catalog.Register(func() pluginapi.Plugin { return shared }, plugins.Source("/opt/plugins/secret.so"), plugins.RegisterOptions{SingleInstance: true})
	require.NoError(t, err)

	store := newTestStore(
		Definition{Name: "one", Type: "secret_check", Config: json.RawMessage(`{"api_key":"a"}`)},
		Definition{Name: "two", Type: "secret_check", Config: json.RawMessage(`{"api_key":"b"}`)},
	)
	service, err := NewService(store, catalog, plugins.HostDeps{})
	require.NoError(t, err)

	err = service.Refresh(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "single configured instance")
}

func TestServiceInstanceConfigIsUnredacted(t *testing.T) {
	service := newService(t, newTestStore(), nil)
	err := service.Upsert(context.Background(), Definition{Name: "sec", Type: "secret_check", Config: json.RawMessage(`{"api_key":"s3cret"}`)})
	require.NoError(t, err)

	config, pluginType, ok := service.InstanceConfig(" sec ")
	require.True(t, ok)
	require.Equal(t, "secret_check", pluginType)
	require.Equal(t, `{"api_key":"s3cret","threshold":0.5}`, string(config), "InstanceConfig() = %s, %q, %v", config, pluginType, ok)
	_, _, ok = service.InstanceConfig("missing")
	require.False(t, ok)
}
