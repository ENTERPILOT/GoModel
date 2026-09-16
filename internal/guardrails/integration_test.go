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
	"github.com/stretchr/testify/require"
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
	require.NoError(t, err)

	return chains
}

func decodeChatRequest(t *testing.T, body string) *core.ChatRequest {
	t.Helper()
	req, err := core.DecodeChatRequest([]byte(body), nil)
	require.NoError(t, err)

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
	require.NoError(t, err)
	require.Len(t, got.Messages, 4)
	require.Equal(t, "system", got.Messages[0].Role)
	require.Equal(t, "be safe", core.ExtractTextContent(got.Messages[0].Content), "first message = %+v", got.Messages[0])

	body, err := json.Marshal(got.Messages[1])
	require.NoError(t, err)
	require.Contains(t, string(body), `"cache_control":{"type":"ephemeral"}`)
	require.Contains(t, string(body), `[|---|](PERSON_1)`)
	require.Contains(t, string(body), `image_url`)
	require.Contains(t, string(body), `"name":"alice"`, "user message lost structure: %s", body)
	require.Equal(t, "John was here", core.ExtractTextContent(got.Messages[3].Content), "tool message rewritten though tool role not selected: %+v", got.Messages[3])
	require.Nil(t, got.Messages[2].Content)
	require.Len(t, got.Messages[2].ToolCalls, 1, "assistant tool call message changed: %+v", got.Messages[2])
	require.NotNil(t, got.Temperature)
	require.Equal(t, 0.2, *got.Temperature)
	require.Equal(t, "gpt-4o", got.Model, "envelope changed: %+v", got)
	require.Equal(t, "Hi, I am John", core.ExtractTextContent(req.Messages[0].Content))

	records := state.Snapshot()
	require.Len(t, records, 2)
	require.True(t, records[0].Edited)
	require.Equal(t, "privacy", records[0].Instance)
	require.True(t, records[1].Edited)
}

func TestWorkflowRequestPatcherLeavesRequestUntouchedWithoutEdits(t *testing.T) {
	service := newService(t, newTestStore(systemPromptDefinition("safety", "be safe")), nil)
	patcher := NewWorkflowRequestPatcher(staticChains{chainsFor(t, service, StepReference{Ref: "safety", Step: 1})})
	req := decodeChatRequest(t, `{"model":"m","messages":[{"role":"system","content":"already"},{"role":"user","content":"hi"}]}`)
	got, err := patcher.PatchChatRequest(context.Background(), req)
	require.NoError(t, err)
	require.Same(t, req, got)
	got, err = NewWorkflowRequestPatcher(nil).PatchChatRequest(context.Background(), req)
	require.NoError(t, err)
	require.Same(t, req, got)
	got, err = NewWorkflowRequestPatcher(staticChains{}).PatchChatRequest(context.Background(), req)
	require.NoError(t, err)
	require.Same(t, req, got)
}

func TestWorkflowRequestPatcherResponses(t *testing.T) {
	service := newService(t, newTestStore(Definition{Name: "override", Type: "system_prompt", Config: json.RawMessage(`{"mode":"override","content":"new rules"}`)}), nil)
	patcher := NewWorkflowRequestPatcher(staticChains{chainsFor(t, service, StepReference{Ref: "override", Step: 1})})
	req, err := core.DecodeResponsesRequest([]byte(`{"model":"m","instructions":"old","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`), nil)
	require.NoError(t, err)

	got, err := patcher.PatchResponsesRequest(context.Background(), req)
	require.NoError(t, err)
	require.Equal(t, "new rules", got.Instructions)

	body, _ := json.Marshal(got.Input)
	require.Contains(t, string(body), `"input_text"`)
	require.NotContains(t, string(body), "new rules", "input = %s", body)
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
			err := catalog.Register(func() pluginapi.Plugin { return &decisionPlugin{decision: decision} }, plugins.SourceBuiltin)
			require.NoError(t, err)

			service, err := NewService(newTestStore(Definition{Name: "d", Type: "decide", FailMode: tt.failMode}), catalog, plugins.HostDeps{})
			require.NoError(t, err)
			err = service.Refresh(context.Background())
			require.NoError(t, err)

			patcher := NewWorkflowRequestPatcher(staticChains{chainsFor(t, service, StepReference{Ref: "d", Step: 1})})
			ctx, state := plugins.WithRequestState(context.Background())
			got, err := patcher.PatchChatRequest(ctx, decodeChatRequest(t, `{"model":"m","messages":[{"role":"user","content":"hi"}]}`))
			switch {
			case tt.wantShort:
				var short *plugins.ShortCircuit
				require.ErrorAs(t, err, &short)
				require.NotNil(t, short.Completion)
				require.Equal(t, "I cannot help", short.Completion.Text(0))
				require.Equal(t, "d", short.Instance)

			case tt.wantStatus != 0:
				var gatewayErr *core.GatewayError
				require.ErrorAs(t, err, &gatewayErr)
				require.Equal(t, tt.wantStatus, gatewayErr.HTTPStatusCode())
				require.NotNil(t, gatewayErr.Code)
				require.Equal(t, tt.wantCode, *gatewayErr.Code)
				require.Equal(t, "nope", gatewayErr.Message)

			default:
				require.NoError(t, err)
				require.NotNil(t, got)
				require.Equal(t, tt.wantHeader, state.ResponseHeaders.Get(plugins.GuardrailHeader))
			}
			records := state.Snapshot()
			require.Len(t, records, 1)
			require.Equal(t, plugins.NormalizeDecision(tt.decision).Action, records[0].Decision.Action)
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

// A chained Responses request only needs its stored history expanded into the
// input when a prompt plugin rewrites content; a classifier leaves the native
// passthrough (and the provider's own history) alone.
func TestWorkflowRequestPatcherEditsPromptContent(t *testing.T) {
	catalog := testCatalog(t)
	err := catalog.Register(func() pluginapi.Plugin { return &decisionPlugin{} }, plugins.SourceRegistered)
	require.NoError(t, err)

	store := newTestStore(
		Definition{Name: "classify", Type: "decide"},
		systemPromptDefinition("inject", "be safe"),
		Definition{Name: "replace", Type: "string_replace", Config: json.RawMessage(`{"rules":"secret => [redacted]"}`)},
		Definition{Name: "flag", Type: "string_replace", Config: json.RawMessage(`{"rules":"secret => [redacted]","on_match":"warn"}`)},
	)
	service, err := NewService(store, catalog, plugins.HostDeps{})
	require.NoError(t, err)
	err = service.Refresh(context.Background())
	require.NoError(t, err)

	tests := []struct {
		name  string
		steps []StepReference
		want  bool
	}{
		{name: "no chain", want: false},
		{name: "classifier only", steps: []StepReference{{Ref: "classify", Step: 1}}, want: false},
		{name: "rewriting plugin", steps: []StepReference{{Ref: "classify", Step: 1}, {Ref: "inject", Step: 2}}, want: true},
		{name: "rewriting instance", steps: []StepReference{{Ref: "replace", Step: 1}}, want: true},
		// The plugin can edit, but this instance only flags its matches.
		{name: "flagging instance", steps: []StepReference{{Ref: "flag", Step: 1}}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var chains *plugins.Chains
			if len(tt.steps) > 0 {
				chains = chainsFor(t, service, tt.steps...)
			}
			patcher := NewWorkflowRequestPatcher(staticChains{chains})
			got := patcher.EditsPromptContent(context.Background())
			require.Equal(t, tt.want, got)
		})
	}
}

func TestWorkflowRequestPatcherFailModes(t *testing.T) {
	for _, tt := range []struct {
		failMode string
		wantErr  bool
	}{{"", true}, {"closed", true}, {"open", false}} {
		t.Run("fail_mode="+tt.failMode, func(t *testing.T) {
			catalog := plugins.NewCatalog()
			err := catalog.Register(func() pluginapi.Plugin { return failingPlugin{} }, plugins.SourceBuiltin)
			require.NoError(t, err)

			service, err := NewService(newTestStore(Definition{Name: "f", Type: "fail", FailMode: tt.failMode}), catalog, plugins.HostDeps{})
			require.NoError(t, err)
			err = service.Refresh(context.Background())
			require.NoError(t, err)

			patcher := NewWorkflowRequestPatcher(staticChains{chainsFor(t, service, StepReference{Ref: "f", Step: 1})})
			_, err = patcher.PatchChatRequest(context.Background(), decodeChatRequest(t, `{"model":"m","messages":[{"role":"user","content":"hi"}]}`))
			if !tt.wantErr {
				require.NoError(t, err)

				return
			}
			var gatewayErr *core.GatewayError
			require.ErrorAs(t, err, &gatewayErr)
			require.Equal(t, http.StatusInternalServerError, gatewayErr.HTTPStatusCode())
			require.NotNil(t, gatewayErr.Code)
			require.Equal(t, plugins.CodePluginFailure, *gatewayErr.Code)

			leaks := strings.Contains(gatewayErr.Message, "f") && strings.Contains(gatewayErr.Message, "classifier")
			require.False(t, leaks, "client message leaks plugin details: %q", gatewayErr.Message)
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
	require.NoError(t, err)

	chat := string(result.Request.Requests[0].Body)
	require.Contains(t, chat, `"guardrail system"`)
	require.Contains(t, chat, `"extra":1`)
	require.Contains(t, chat, `"seed":7`)

	var chatReq core.ChatRequest
	err = json.Unmarshal(result.Request.Requests[0].Body, &chatReq)
	require.NoError(t, err)
	require.Len(t, chatReq.Messages, 2)
	require.Equal(t, "system", chatReq.Messages[0].Role, "chat item decode = %+v, %v", chatReq, err)
	resp := string(result.Request.Requests[1].Body)
	require.Contains(t, resp, `"instructions":"guardrail system"`)
	require.Equal(t, `{"model":"m","input":"Hi"}`, string(result.Request.Requests[2].Body), "embeddings item changed: %s", result.Request.Requests[2].Body)
	require.Equal(t, `{"model":"m","messages":[{"role":"user","content":"Hi","extra":1}],"seed":7}`, string(req.Requests[0].Body))
	result, err = NewWorkflowBatchPreparer(nil, staticChains{}).PrepareBatchRequest(context.Background(), "openai", req)
	require.NoError(t, err)
	require.Same(t, req, result.Request, "empty chain result = %+v, %v", result, err)
}

func TestWorkflowBatchPreparerRejectsRespondDecisions(t *testing.T) {
	catalog := plugins.NewCatalog()
	err := catalog.Register(func() pluginapi.Plugin { return &decisionPlugin{decision: pluginapi.Respond("no")} }, plugins.SourceBuiltin)
	require.NoError(t, err)

	service, err := NewService(newTestStore(Definition{Name: "d", Type: "decide"}), catalog, plugins.HostDeps{})
	require.NoError(t, err)
	err = service.Refresh(context.Background())
	require.NoError(t, err)

	preparer := NewWorkflowBatchPreparer(nil, staticChains{chainsFor(t, service, StepReference{Ref: "d", Step: 1})})
	_, err = preparer.PrepareBatchRequest(context.Background(), "openai", &core.BatchRequest{Requests: []core.BatchRequestItem{
		{CustomID: "chat-1", Method: "POST", URL: "/v1/chat/completions", Body: json.RawMessage(`{"model":"m","messages":[{"role":"user","content":"Hi"}]}`)},
	}})
	var gatewayErr *core.GatewayError
	require.ErrorAs(t, err, &gatewayErr)
	require.Equal(t, http.StatusBadRequest, gatewayErr.HTTPStatusCode())
}
