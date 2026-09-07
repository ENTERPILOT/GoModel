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
		if err := catalog.Register(factory, plugins.SourceBuiltin); err != nil {
			t.Fatalf("Register() error = %v", err)
		}
	}
	if err := catalog.Register(func() pluginapi.Plugin { return &secretPlugin{} }, plugins.SourceRegistered); err != nil {
		t.Fatalf("Register(secret) error = %v", err)
	}
	return catalog
}

func newService(t *testing.T, store Store, chat plugins.ChatCompleter) *Service {
	t.Helper()
	service, err := NewService(store, testCatalog(t), plugins.HostDeps{Chat: chat})
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
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
	if _, err := chain.RunPrompt(context.Background(), x); err != nil {
		t.Fatalf("RunPrompt() error = %v", err)
	}
	return prompt
}

func TestServiceRefreshBuildsChainsFromDefinitions(t *testing.T) {
	service := newService(t, newTestStore(systemPromptDefinition("safety", "be safe")), nil)

	if got := service.Names(); len(got) != 1 || got[0] != "safety" {
		t.Fatalf("Names() = %v, want [safety]", got)
	}
	chains, err := service.BuildChains([]StepReference{{Ref: "safety", Step: 10}})
	if err != nil {
		t.Fatalf("BuildChains() error = %v", err)
	}
	if chains.Prompt.Len() != 1 || chains.Prompt.Hash == "" || !chains.Response.Empty() {
		t.Fatalf("chains = %+v", chains)
	}
	prompt := runPrompt(t, chains.Prompt, "hi")
	if len(prompt.Messages) != 2 || prompt.Messages[0].Role != pluginapi.RoleSystem || prompt.Messages[0].Text() != "be safe" {
		t.Fatalf("messages = %+v, want injected system prompt", prompt.Messages)
	}
	if chains, err := service.BuildChains(nil); err != nil || chains != nil {
		t.Fatalf("BuildChains(nil) = %v, %v", chains, err)
	}
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
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("BuildChains() error = %v, want %q", err, tt.want)
			}
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
	if err != nil {
		t.Fatalf("BuildChains() error = %v", err)
	}
	if chains.Response.Len() != 1 {
		t.Fatal("response chain missing")
	}
	prompt := runPrompt(t, chains.Prompt, "John Smith")
	if prompt.Messages[0].Text() != "[|---|](PERSON_1)" {
		t.Fatalf("rewritten = %q", prompt.Messages[0].Text())
	}
	if captured == nil || captured.Model != "openai/gpt-4o-mini" {
		t.Fatalf("captured request = %+v", captured)
	}

	service.SetChatCompleter(replyChat("[|---|](PERSON_2)"))
	if prompt := runPrompt(t, chains.Prompt, "Jane"); prompt.Messages[0].Text() != "[|---|](PERSON_2)" {
		t.Fatalf("after SetChatCompleter rewritten = %q", prompt.Messages[0].Text())
	}
	view, ok := service.GetView("privacy")
	if !ok || view.Summary != "openai/gpt-4o-mini • user • default prompt" || strings.Join(view.Phases, ",") != "prompt,response" {
		t.Fatalf("view = %+v", view)
	}
}

func TestServiceRefreshReturnsGatewayErrorOnStoreFailure(t *testing.T) {
	store := newTestStore()
	store.listErr = errors.New("db down")
	service, err := NewService(store, testCatalog(t), plugins.HostDeps{})
	if err != nil {
		t.Fatal(err)
	}
	err = service.Refresh(context.Background())
	var gatewayErr *core.GatewayError
	if !errors.As(err, &gatewayErr) || gatewayErr.HTTPStatusCode() != 502 {
		t.Fatalf("Refresh() error = %v, want 502 gateway error", err)
	}
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
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Upsert() error = %v, want %q", err, tt.want)
			}
			if !IsValidationError(err) {
				t.Fatalf("Upsert() error %T is not a validation error", err)
			}
			if service.Len() != 0 {
				t.Fatal("snapshot changed after a rejected upsert")
			}
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
	if err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}
	stored := store.definitions["safety"]
	if stored.Type != "system_prompt" || stored.UserPath != "/team/alpha" || stored.FailMode != "open" || stored.TimeoutMS != 250 {
		t.Fatalf("stored = %+v", stored)
	}
	if string(stored.Config) != `{"content":"be safe","mode":"inject"}` {
		t.Fatalf("stored config = %s", stored.Config)
	}
	chains, err := service.BuildChains([]StepReference{{Ref: "safety", Step: 5}})
	if err != nil {
		t.Fatal(err)
	}
	inst := chains.Prompt.Instances()[0]
	if inst.FailMode != plugins.FailOpen || inst.Timeout.Milliseconds() != 250 {
		t.Fatalf("instance = %+v", inst)
	}
}

func TestServiceSecretsRedactedMergedAndCleared(t *testing.T) {
	store := newTestStore()
	service := newService(t, store, nil)
	ctx := context.Background()
	if err := service.Upsert(ctx, Definition{Name: "sec", Type: "secret_check", Config: json.RawMessage(`{"api_key":"s3cret"}`)}); err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}
	if string(store.definitions["sec"].Config) != `{"api_key":"s3cret","threshold":0.5}` {
		t.Fatalf("stored = %s", store.definitions["sec"].Config)
	}
	def, _ := service.Get("sec")
	if string(def.Config) != `{"api_key":"********","threshold":0.5}` {
		t.Fatalf("Get() config = %s, want redacted", def.Config)
	}
	if views := service.ListViews(); string(views[0].Config) != `{"api_key":"********","threshold":0.5}` || views[0].Phases[0] != "response" {
		t.Fatalf("ListViews() = %+v", views)
	}
	// Masked secret keeps the stored value; other fields are replaced.
	if err := service.Upsert(ctx, Definition{Name: "sec", Type: "secret_check", Config: json.RawMessage(`{"api_key":"********","threshold":1}`)}); err != nil {
		t.Fatalf("Upsert(masked) error = %v", err)
	}
	if string(store.definitions["sec"].Config) != `{"api_key":"s3cret","threshold":1}` {
		t.Fatalf("stored after mask = %s", store.definitions["sec"].Config)
	}
	// Omitted fail_mode resets to the default.
	if err := service.Upsert(ctx, Definition{Name: "sec", Type: "secret_check", FailMode: "open", Config: json.RawMessage(`{"api_key":"********"}`)}); err != nil {
		t.Fatal(err)
	}
	if err := service.Upsert(ctx, Definition{Name: "sec", Type: "secret_check", Config: json.RawMessage(`{"api_key":"********"}`)}); err != nil {
		t.Fatal(err)
	}
	if store.definitions["sec"].FailMode != "" {
		t.Fatalf("fail_mode = %q, want reset", store.definitions["sec"].FailMode)
	}
	// An empty secret clears it, which the required check rejects.
	err := service.Upsert(ctx, Definition{Name: "sec", Type: "secret_check", Config: json.RawMessage(`{"api_key":""}`)})
	if err == nil || !strings.Contains(err.Error(), "required") {
		t.Fatalf("Upsert(cleared) error = %v, want required", err)
	}
}

func TestServiceTypeDefinitions(t *testing.T) {
	service := newService(t, newTestStore(), nil)
	defs := service.TypeDefinitions()
	byType := map[string]TypeDefinition{}
	for _, def := range defs {
		byType[def.Type] = def
	}
	sys, ok := byType["system_prompt"]
	if !ok {
		t.Fatalf("system_prompt missing from %+v", defs)
	}
	if sys.Label != "System Prompt" || sys.Source != "builtin" || !sys.Mutates || strings.Join(sys.Phases, ",") != "prompt" {
		t.Fatalf("system_prompt = %+v", sys)
	}
	if string(sys.Defaults) != `{"content":"","mode":"inject"}` {
		t.Fatalf("defaults = %s", sys.Defaults)
	}
	if len(sys.Fields) != 2 || sys.Fields[0].Key != "mode" || sys.Fields[0].Default != "inject" || len(sys.Fields[0].Options) != 3 {
		t.Fatalf("fields = %+v", sys.Fields)
	}
	llm := byType["llm_based_altering"]
	if llm.Label != "LLM Based Altering" || strings.Join(llm.Phases, ",") != "prompt,response" {
		t.Fatalf("llm = %+v", llm)
	}
	var defaults map[string]any
	if err := json.Unmarshal(llm.Defaults, &defaults); err != nil || defaults["max_tokens"] != float64(4096) {
		t.Fatalf("llm defaults = %s (%v)", llm.Defaults, err)
	}
	if sec := byType["secret_check"]; sec.Source != "registered" || sec.Fields[0].Input != "secret" {
		t.Fatalf("secret_check = %+v", sec)
	}
}

func TestServiceUpsertDefinitionsUpdatesSubsetAndPreservesCustomEntries(t *testing.T) {
	store := newTestStore(systemPromptDefinition("custom", "keep me"), systemPromptDefinition("seeded", "old"))
	service := newService(t, store, nil)
	if err := service.UpsertDefinitions(context.Background(), []Definition{systemPromptDefinition("seeded", "new")}); err != nil {
		t.Fatalf("UpsertDefinitions() error = %v", err)
	}
	if got := service.Names(); strings.Join(got, ",") != "custom,seeded" {
		t.Fatalf("Names() = %v", got)
	}
	if def, _ := service.Get("seeded"); !strings.Contains(string(def.Config), "new") {
		t.Fatalf("seeded config = %s", def.Config)
	}
	if err := service.UpsertDefinitions(context.Background(), nil); err != nil {
		t.Fatalf("UpsertDefinitions(nil) error = %v", err)
	}
}

func TestServiceMutationsLeaveSnapshotUnchangedWhenPersistenceFails(t *testing.T) {
	store := newTestStore(systemPromptDefinition("a", "x"))
	service := newService(t, store, nil)
	store.upsertManyErr = errors.New("disk full")
	store.upsertErr = errors.New("disk full")
	store.deleteErr = errors.New("disk full")
	if err := service.UpsertDefinitions(context.Background(), []Definition{systemPromptDefinition("b", "y")}); err == nil {
		t.Fatal("UpsertDefinitions() error = nil")
	}
	if err := service.Upsert(context.Background(), systemPromptDefinition("c", "z")); err == nil {
		t.Fatal("Upsert() error = nil")
	}
	if err := service.Delete(context.Background(), "a"); err == nil {
		t.Fatal("Delete() error = nil")
	}
	if got := service.Names(); strings.Join(got, ",") != "a" {
		t.Fatalf("Names() = %v, want [a]", got)
	}
	store.deleteErr = nil
	if err := service.Delete(context.Background(), "a"); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if service.Len() != 0 {
		t.Fatal("definition not removed")
	}
	if err := service.Delete(context.Background(), " "); err == nil {
		t.Fatal("Delete(empty) error = nil")
	}
}

func TestServiceRejectsSecondInstanceOfSingleInstancePlugin(t *testing.T) {
	catalog := plugins.NewCatalog()
	shared := &secretPlugin{}
	if err := catalog.Register(func() pluginapi.Plugin { return shared }, plugins.Source("/opt/plugins/secret.so"), plugins.RegisterOptions{SingleInstance: true}); err != nil {
		t.Fatal(err)
	}
	store := newTestStore(
		Definition{Name: "one", Type: "secret_check", Config: json.RawMessage(`{"api_key":"a"}`)},
		Definition{Name: "two", Type: "secret_check", Config: json.RawMessage(`{"api_key":"b"}`)},
	)
	service, err := NewService(store, catalog, plugins.HostDeps{})
	if err != nil {
		t.Fatal(err)
	}
	err = service.Refresh(context.Background())
	if err == nil || !strings.Contains(err.Error(), "single configured instance") {
		t.Fatalf("Refresh() error = %v", err)
	}
}

func TestServiceInstanceConfigIsUnredacted(t *testing.T) {
	service := newService(t, newTestStore(), nil)
	if err := service.Upsert(context.Background(), Definition{Name: "sec", Type: "secret_check", Config: json.RawMessage(`{"api_key":"s3cret"}`)}); err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}
	config, pluginType, ok := service.InstanceConfig(" sec ")
	if !ok || pluginType != "secret_check" || string(config) != `{"api_key":"s3cret","threshold":0.5}` {
		t.Fatalf("InstanceConfig() = %s, %q, %v", config, pluginType, ok)
	}
	if _, _, ok := service.InstanceConfig("missing"); ok {
		t.Fatal("InstanceConfig(missing) reported ok")
	}
}
