package guardrails

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/plugins"
	"github.com/enterpilot/gomodel/pluginapi"
)

type staticChains struct{ chains *plugins.Chains }

func (s staticChains) ChainsForContext(context.Context) *plugins.Chains { return s.chains }

// decisionPlugin returns a fixed decision from the prompt phase.
type decisionPlugin struct{ decision pluginapi.Decision }

func (p *decisionPlugin) Manifest() pluginapi.Manifest {
	return pluginapi.Manifest{Name: "decide", Kinds: []pluginapi.Kind{pluginapi.KindPrompt}}
}
func (p *decisionPlugin) Init(context.Context, json.RawMessage, pluginapi.Host) error { return nil }
func (p *decisionPlugin) Close(context.Context) error                                 { return nil }
func (p *decisionPlugin) OnPrompt(context.Context, *pluginapi.Exchange) (pluginapi.Decision, error) {
	return p.decision, nil
}

func chainsFor(t *testing.T, service *Service, steps ...StepReference) *plugins.Chains {
	t.Helper()
	chains, err := service.BuildChains(steps)
	if err != nil {
		t.Fatalf("BuildChains() error = %v", err)
	}
	return chains
}

func decodeChatRequest(t *testing.T, body string) *core.ChatRequest {
	t.Helper()
	req, err := core.DecodeChatRequest([]byte(body), nil)
	if err != nil {
		t.Fatalf("DecodeChatRequest() error = %v", err)
	}
	return req
}

func TestWorkflowRequestPatcherRewritesChatPreservingStructure(t *testing.T) {
	store := newTestStore(
		systemPromptDefinition("safety", "be safe"),
		Definition{Name: "privacy", Type: "llm_based_altering", Config: rawConfig(t, map[string]any{"model": "openai/gpt-4o-mini", "roles": []string{"user"}})},
	)
	service := newService(t, store, chatFunc(func(_ context.Context, req *core.ChatRequest) (*core.ChatResponse, error) {
		text := core.ExtractTextContent(req.Messages[1].Content)
		inner := strings.TrimSuffix(strings.TrimPrefix(text, "<TEXT_TO_ALTER>\n"), "\n</TEXT_TO_ALTER>")
		return replyChat(strings.ReplaceAll(inner, "John", "[|---|](PERSON_1)"))(context.Background(), req)
	}))
	patcher := NewWorkflowRequestPatcher(staticChains{chainsFor(t, service,
		StepReference{Ref: "privacy", Step: 10},
		StepReference{Ref: "safety", Step: 20},
	)})

	req := decodeChatRequest(t, `{
		"model":"gpt-4o",
		"messages":[
			{"role":"user","content":[
				{"type":"text","text":"Hi, I am John","cache_control":{"type":"ephemeral"}},
				{"type":"image_url","image_url":{"url":"https://example.com/a.png"}}
			],"name":"alice"},
			{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"f","arguments":"{}"}}]},
			{"role":"tool","tool_call_id":"c1","content":"John was here"}
		],
		"temperature":0.2
	}`)
	ctx, state := plugins.WithRequestState(context.Background())
	got, err := patcher.PatchChatRequest(ctx, req)
	if err != nil {
		t.Fatalf("PatchChatRequest() error = %v", err)
	}
	if len(got.Messages) != 4 {
		t.Fatalf("messages = %d, want 4 (system injected)", len(got.Messages))
	}
	if got.Messages[0].Role != "system" || core.ExtractTextContent(got.Messages[0].Content) != "be safe" {
		t.Fatalf("first message = %+v", got.Messages[0])
	}
	body, err := json.Marshal(got.Messages[1])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"cache_control":{"type":"ephemeral"}`) || !strings.Contains(string(body), `[|---|](PERSON_1)`) || !strings.Contains(string(body), `image_url`) || !strings.Contains(string(body), `"name":"alice"`) {
		t.Fatalf("user message lost structure: %s", body)
	}
	if core.ExtractTextContent(got.Messages[3].Content) != "John was here" {
		t.Fatalf("tool message rewritten though tool role not selected: %+v", got.Messages[3])
	}
	if got.Messages[2].Content != nil || len(got.Messages[2].ToolCalls) != 1 {
		t.Fatalf("assistant tool call message changed: %+v", got.Messages[2])
	}
	if got.Temperature == nil || *got.Temperature != 0.2 || got.Model != "gpt-4o" {
		t.Fatalf("envelope changed: %+v", got)
	}
	if core.ExtractTextContent(req.Messages[0].Content) != "Hi, I am John" {
		t.Fatal("original request mutated")
	}
	records := state.Snapshot()
	if len(records) != 2 || !records[0].Edited || records[0].Instance != "privacy" || !records[1].Edited {
		t.Fatalf("records = %+v", records)
	}
}

func TestWorkflowRequestPatcherLeavesRequestUntouchedWithoutEdits(t *testing.T) {
	service := newService(t, newTestStore(systemPromptDefinition("safety", "be safe")), nil)
	patcher := NewWorkflowRequestPatcher(staticChains{chainsFor(t, service, StepReference{Ref: "safety", Step: 1})})
	req := decodeChatRequest(t, `{"model":"m","messages":[{"role":"system","content":"already"},{"role":"user","content":"hi"}]}`)
	got, err := patcher.PatchChatRequest(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if got != req {
		t.Fatal("request copied although nothing changed")
	}
	if got, err := NewWorkflowRequestPatcher(nil).PatchChatRequest(context.Background(), req); err != nil || got != req {
		t.Fatalf("nil resolver = %v, %v", got, err)
	}
	if got, err := NewWorkflowRequestPatcher(staticChains{}).PatchChatRequest(context.Background(), req); err != nil || got != req {
		t.Fatalf("nil chains = %v, %v", got, err)
	}
}

func TestWorkflowRequestPatcherResponses(t *testing.T) {
	service := newService(t, newTestStore(Definition{Name: "override", Type: "system_prompt", Config: json.RawMessage(`{"mode":"override","content":"new rules"}`)}), nil)
	patcher := NewWorkflowRequestPatcher(staticChains{chainsFor(t, service, StepReference{Ref: "override", Step: 1})})
	req, err := core.DecodeResponsesRequest([]byte(`{"model":"m","instructions":"old","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	got, err := patcher.PatchResponsesRequest(context.Background(), req)
	if err != nil {
		t.Fatalf("PatchResponsesRequest() error = %v", err)
	}
	if got.Instructions != "new rules" {
		t.Fatalf("instructions = %q", got.Instructions)
	}
	body, _ := json.Marshal(got.Input)
	if !strings.Contains(string(body), `"input_text"`) || strings.Contains(string(body), "new rules") {
		t.Fatalf("input = %s", body)
	}
}

func TestWorkflowRequestPatcherDecisions(t *testing.T) {
	tests := []struct {
		name       string
		decision   pluginapi.Decision
		failMode   string
		wantStatus int
		wantCode   string
		wantShort  bool
		wantHeader string
	}{
		{name: "block default status", decision: pluginapi.Block(0, "policy", "nope"), wantStatus: http.StatusBadRequest, wantCode: "policy"},
		{name: "block custom status", decision: pluginapi.Block(446, "policy", "nope"), wantStatus: 446, wantCode: "policy"},
		{name: "respond", decision: pluginapi.Respond("I cannot help"), wantShort: true},
		{name: "warn", decision: pluginapi.Warn("pii", "found pii", nil), wantHeader: "warn; code=pii"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			catalog := plugins.NewCatalog()
			decision := tt.decision
			if err := catalog.Register(func() pluginapi.Plugin { return &decisionPlugin{decision: decision} }, plugins.SourceBuiltin); err != nil {
				t.Fatal(err)
			}
			service, err := NewService(newTestStore(Definition{Name: "d", Type: "decide", FailMode: tt.failMode}), catalog, plugins.HostDeps{})
			if err != nil {
				t.Fatal(err)
			}
			if err := service.Refresh(context.Background()); err != nil {
				t.Fatal(err)
			}
			patcher := NewWorkflowRequestPatcher(staticChains{chainsFor(t, service, StepReference{Ref: "d", Step: 1})})
			ctx, state := plugins.WithRequestState(context.Background())
			got, err := patcher.PatchChatRequest(ctx, decodeChatRequest(t, `{"model":"m","messages":[{"role":"user","content":"hi"}]}`))
			switch {
			case tt.wantShort:
				var short *plugins.ShortCircuit
				if !errors.As(err, &short) || short.Completion == nil || short.Completion.Text(0) != "I cannot help" || short.Instance != "d" {
					t.Fatalf("error = %v, want short circuit", err)
				}
			case tt.wantStatus != 0:
				var gatewayErr *core.GatewayError
				if !errors.As(err, &gatewayErr) || gatewayErr.HTTPStatusCode() != tt.wantStatus || gatewayErr.Code == nil || *gatewayErr.Code != tt.wantCode || gatewayErr.Message != "nope" {
					t.Fatalf("error = %v, want %d/%s", err, tt.wantStatus, tt.wantCode)
				}
			default:
				if err != nil || got == nil {
					t.Fatalf("unexpected error %v", err)
				}
				if state.ResponseHeaders.Get(plugins.GuardrailHeader) != tt.wantHeader {
					t.Fatalf("header = %q, want %q", state.ResponseHeaders.Get(plugins.GuardrailHeader), tt.wantHeader)
				}
			}
			if records := state.Snapshot(); len(records) != 1 || records[0].Decision.Action != plugins.NormalizeDecision(tt.decision).Action {
				t.Fatalf("records = %+v", records)
			}
		})
	}
}

type failingPlugin struct{}

func (failingPlugin) Manifest() pluginapi.Manifest {
	return pluginapi.Manifest{Name: "fail", Kinds: []pluginapi.Kind{pluginapi.KindPrompt}}
}
func (failingPlugin) Init(context.Context, json.RawMessage, pluginapi.Host) error { return nil }
func (failingPlugin) Close(context.Context) error                                 { return nil }
func (failingPlugin) OnPrompt(context.Context, *pluginapi.Exchange) (pluginapi.Decision, error) {
	return pluginapi.Decision{}, errors.New("classifier unreachable")
}

func TestWorkflowRequestPatcherFailModes(t *testing.T) {
	for _, tt := range []struct {
		failMode string
		wantErr  bool
	}{{"", true}, {"closed", true}, {"open", false}} {
		t.Run("fail_mode="+tt.failMode, func(t *testing.T) {
			catalog := plugins.NewCatalog()
			if err := catalog.Register(func() pluginapi.Plugin { return failingPlugin{} }, plugins.SourceBuiltin); err != nil {
				t.Fatal(err)
			}
			service, err := NewService(newTestStore(Definition{Name: "f", Type: "fail", FailMode: tt.failMode}), catalog, plugins.HostDeps{})
			if err != nil {
				t.Fatal(err)
			}
			if err := service.Refresh(context.Background()); err != nil {
				t.Fatal(err)
			}
			patcher := NewWorkflowRequestPatcher(staticChains{chainsFor(t, service, StepReference{Ref: "f", Step: 1})})
			_, err = patcher.PatchChatRequest(context.Background(), decodeChatRequest(t, `{"model":"m","messages":[{"role":"user","content":"hi"}]}`))
			if !tt.wantErr {
				if err != nil {
					t.Fatalf("error = %v, want nil (fail open)", err)
				}
				return
			}
			var gatewayErr *core.GatewayError
			if !errors.As(err, &gatewayErr) || gatewayErr.HTTPStatusCode() != http.StatusInternalServerError || *gatewayErr.Code != plugins.CodePluginFailure {
				t.Fatalf("error = %v, want 500 plugin_failure", err)
			}
			if strings.Contains(gatewayErr.Message, "f") && strings.Contains(gatewayErr.Message, "classifier") {
				t.Fatalf("client message leaks plugin details: %q", gatewayErr.Message)
			}
		})
	}
}

func TestWorkflowBatchPreparerRewritesInlineItems(t *testing.T) {
	service := newService(t, newTestStore(systemPromptDefinition("safety", "guardrail system")), nil)
	preparer := NewWorkflowBatchPreparer(nil, staticChains{chainsFor(t, service, StepReference{Ref: "safety", Step: 1})})
	req := &core.BatchRequest{
		CompletionWindow: "24h",
		Requests: []core.BatchRequestItem{
			{CustomID: "chat-1", Method: "POST", URL: "/v1/chat/completions", Body: json.RawMessage(`{"model":"m","messages":[{"role":"user","content":"Hi","extra":1}],"seed":7}`)},
			{CustomID: "resp-1", Method: "POST", URL: "/v1/responses", Body: json.RawMessage(`{"model":"m","input":"Hi"}`)},
			{CustomID: "emb-1", Method: "POST", URL: "/v1/embeddings", Body: json.RawMessage(`{"model":"m","input":"Hi"}`)},
		},
	}
	result, err := preparer.PrepareBatchRequest(context.Background(), "openai", req)
	if err != nil {
		t.Fatalf("PrepareBatchRequest() error = %v", err)
	}
	chat := string(result.Request.Requests[0].Body)
	if !strings.Contains(chat, `"guardrail system"`) || !strings.Contains(chat, `"extra":1`) || !strings.Contains(chat, `"seed":7`) {
		t.Fatalf("chat item = %s", chat)
	}
	var chatReq core.ChatRequest
	if err := json.Unmarshal(result.Request.Requests[0].Body, &chatReq); err != nil || len(chatReq.Messages) != 2 || chatReq.Messages[0].Role != "system" {
		t.Fatalf("chat item decode = %+v, %v", chatReq, err)
	}
	if resp := string(result.Request.Requests[1].Body); !strings.Contains(resp, `"instructions":"guardrail system"`) {
		t.Fatalf("responses item = %s", resp)
	}
	if string(result.Request.Requests[2].Body) != `{"model":"m","input":"Hi"}` {
		t.Fatalf("embeddings item changed: %s", result.Request.Requests[2].Body)
	}
	if string(req.Requests[0].Body) != `{"model":"m","messages":[{"role":"user","content":"Hi","extra":1}],"seed":7}` {
		t.Fatal("original batch mutated")
	}
	if result, err := NewWorkflowBatchPreparer(nil, staticChains{}).PrepareBatchRequest(context.Background(), "openai", req); err != nil || result.Request != req {
		t.Fatalf("empty chain result = %+v, %v", result, err)
	}
}

func TestWorkflowBatchPreparerRejectsRespondDecisions(t *testing.T) {
	catalog := plugins.NewCatalog()
	if err := catalog.Register(func() pluginapi.Plugin { return &decisionPlugin{decision: pluginapi.Respond("no")} }, plugins.SourceBuiltin); err != nil {
		t.Fatal(err)
	}
	service, err := NewService(newTestStore(Definition{Name: "d", Type: "decide"}), catalog, plugins.HostDeps{})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	preparer := NewWorkflowBatchPreparer(nil, staticChains{chainsFor(t, service, StepReference{Ref: "d", Step: 1})})
	_, err = preparer.PrepareBatchRequest(context.Background(), "openai", &core.BatchRequest{Requests: []core.BatchRequestItem{
		{CustomID: "chat-1", Method: "POST", URL: "/v1/chat/completions", Body: json.RawMessage(`{"model":"m","messages":[{"role":"user","content":"Hi"}]}`)},
	}})
	var gatewayErr *core.GatewayError
	if !errors.As(err, &gatewayErr) || gatewayErr.HTTPStatusCode() != http.StatusBadRequest {
		t.Fatalf("error = %v, want 400", err)
	}
}
