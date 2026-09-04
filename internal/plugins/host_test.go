package plugins

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/pluginapi"
)

type chatFunc func(ctx context.Context, req *core.ChatRequest) (*core.ChatResponse, error)

func (f chatFunc) ChatCompletion(ctx context.Context, req *core.ChatRequest) (*core.ChatResponse, error) {
	return f(ctx, req)
}

type recordingSink struct{ names []string }

func (r *recordingSink) Inc(name string, _ map[string]string) { r.names = append(r.names, name) }
func (r *recordingSink) Observe(name string, _ float64, _ map[string]string) {
	r.names = append(r.names, name)
}

func TestHostInference(t *testing.T) {
	var captured *core.ChatRequest
	var capturedCtx context.Context
	sink := &recordingSink{}
	h := NewHost(HostDeps{Chat: chatFunc(func(ctx context.Context, req *core.ChatRequest) (*core.ChatResponse, error) {
		captured, capturedCtx = req, ctx
		return &core.ChatResponse{Model: req.Model, Choices: []core.Choice{{Message: core.ResponseMessage{Role: "assistant", Content: "rewritten"}, FinishReason: "stop"}}}, nil
	}), Metrics: sink}, HostInfo{PluginName: "LLM Judge", InstanceName: "privacy", UserPath: "/team/alpha"})

	temp := 0.5
	out, err := h.Inference().Complete(core.WithEffectiveUserPath(context.Background(), "/ignored"), pluginapi.InferenceRequest{
		Model:       "openai/gpt-4o-mini",
		MaxTokens:   32,
		Temperature: &temp,
		Messages:    []pluginapi.Message{pluginapi.TextMessage(pluginapi.RoleUser, "hi")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.Text(0) != "rewritten" {
		t.Fatalf("text = %q", out.Text(0))
	}
	if captured.Model != "openai/gpt-4o-mini" || *captured.MaxTokens != 32 || *captured.Temperature != 0.5 || len(captured.Messages) != 1 {
		t.Fatalf("request = %+v", captured)
	}
	if core.GetRequestOrigin(capturedCtx) != core.RequestOriginPlugin {
		t.Fatalf("origin = %q", core.GetRequestOrigin(capturedCtx))
	}
	if got := core.UserPathFromContext(capturedCtx); got != "/team/alpha/guardrails/privacy" {
		t.Fatalf("user path = %q", got)
	}
	h.Metrics().Inc("Calls Total", nil)
	h.Metrics().Observe("latency", 1, nil)
	if len(sink.names) != 2 || sink.names[0] != "plugin_llm_judge_calls_total" || sink.names[1] != "plugin_llm_judge_latency" {
		t.Fatalf("metric names = %v", sink.names)
	}
	if _, err := h.History(context.Background(), pluginapi.Meta{}); !errors.Is(err, ErrHistoryUnavailable) {
		t.Fatalf("History error = %v", err)
	}
	if h.Logger() == nil {
		t.Fatal("Logger() = nil")
	}
}

func TestHostInferenceUserPathFallsBackToRequest(t *testing.T) {
	var capturedCtx context.Context
	h := NewHost(HostDeps{Chat: chatFunc(func(ctx context.Context, req *core.ChatRequest) (*core.ChatResponse, error) {
		capturedCtx = ctx
		return &core.ChatResponse{}, nil
	})}, HostInfo{PluginName: "p", InstanceName: "i"})
	if _, err := h.Inference().Complete(core.WithEffectiveUserPath(context.Background(), "/acme"), pluginapi.InferenceRequest{Model: "m"}); err != nil {
		t.Fatal(err)
	}
	if got := core.UserPathFromContext(capturedCtx); got != "/acme/guardrails/i" {
		t.Fatalf("user path = %q", got)
	}
	if _, err := h.Inference().Complete(context.Background(), pluginapi.InferenceRequest{Model: "m", UserPath: "/override"}); err != nil {
		t.Fatal(err)
	}
	if got := core.UserPathFromContext(capturedCtx); got != "/override/guardrails/i" {
		t.Fatalf("override user path = %q", got)
	}
}

func TestHostInferenceUnavailable(t *testing.T) {
	h := NewHost(HostDeps{}, HostInfo{PluginName: "p", InstanceName: "i"})
	if _, err := h.Inference().Complete(context.Background(), pluginapi.InferenceRequest{Model: "m"}); !errors.Is(err, ErrInferenceUnavailable) {
		t.Fatalf("error = %v", err)
	}
	// A no-op metrics sink never panics.
	h.Metrics().Inc("x", nil)
}

func TestMetaFromContextAndRequestState(t *testing.T) {
	ctx := core.WithRequestID(context.Background(), "req-9")
	ctx = core.WithRequestDialect(ctx, core.RequestDialectAnthropicMessages)
	ctx = core.WithEffectiveUserPath(ctx, "/team")
	ctx = core.WithAuthKeyID(ctx, "key-1")
	ctx = core.WithSessionID(ctx, "sess")
	snapshot := core.NewRequestSnapshot(http.MethodPost, "/v1/messages", nil, nil, map[string][]string{"Authorization": {"Bearer x"}, "X-Trace": {"t1"}}, "application/json", nil, false, "req-9", nil)
	ctx = core.WithRequestSnapshot(ctx, snapshot)
	workflow := &core.Workflow{
		Endpoint:     core.DescribeEndpoint(http.MethodPost, "/v1/chat/completions"),
		ProviderType: "anthropic",
		Resolution: &core.RequestModelResolution{
			Requested:        core.NewRequestedModelSelector("smart", ""),
			ResolvedSelector: core.ModelSelector{Provider: "anthropic", Model: "claude"},
			ProviderType:     "anthropic",
			ProviderName:     "anthropic-eu",
			AliasApplied:     true,
		},
		Policy: &core.ResolvedWorkflowPolicy{VersionID: "wf-1", Features: core.WorkflowFeatures{Cache: true}},
	}
	meta := MetaFromContext(ctx, workflow)
	if meta.RequestID != "req-9" || meta.Dialect != "anthropic_messages" || meta.Endpoint != "/v1/messages" || meta.Operation != string(core.OperationChatCompletions) {
		t.Fatalf("meta identity = %+v", meta)
	}
	if meta.UserPath != "/team" || meta.AuthKeyID != "key-1" || meta.SessionID != "sess" || meta.Origin != "external" {
		t.Fatalf("meta request = %+v", meta)
	}
	if meta.RequestedModel != "smart" || meta.Model != "claude" || meta.Provider != "anthropic" || meta.ProviderName != "anthropic-eu" || meta.VirtualModelSource != "smart" {
		t.Fatalf("meta routing = %+v", meta)
	}
	if meta.WorkflowVersionID != "wf-1" || !meta.Features["cache"] || meta.Features["usage"] {
		t.Fatalf("meta workflow = %+v", meta)
	}
	meta = WithAttempts(meta, []Attempt{{Seq: 1, Kind: "primary", ProviderType: "anthropic", Success: true}})
	if len(meta.Attempts) != 1 || meta.Attempts[0].Provider != "anthropic" {
		t.Fatalf("attempts = %+v", meta.Attempts)
	}
	if bare := MetaFromContext(context.Background(), nil); bare.Dialect != "openai" || bare.Endpoint != "" {
		t.Fatalf("bare meta = %+v", bare)
	}

	ctx, state := WithRequestState(ctx)
	ctx2, again := WithRequestState(ctx)
	if again != state || ctx2 != ctx {
		t.Fatal("WithRequestState must be idempotent")
	}
	x := state.NewExchange(ctx, meta)
	if x.Headers.Request.Get("Authorization") != "[redacted]" || x.Headers.Request.Get("X-Trace") != "t1" {
		t.Fatalf("request headers = %v", x.Headers.Request)
	}
	x.Values.Set("k", 1)
	x.Headers.Response.Set("X-GoModel-Guardrail", "warn; code=x")
	if v, _ := state.Values.Get("k"); v != 1 {
		t.Fatal("values not shared with state")
	}
	dst := http.Header{}
	state.ApplyResponseHeaders(dst)
	if dst.Get("X-GoModel-Guardrail") != "warn; code=x" {
		t.Fatalf("headers = %v", dst)
	}
	state.Record(DecisionRecord{Phase: pluginapi.KindPrompt, Instance: "a"})
	if got := state.Snapshot(); len(got) != 1 || got[0].Instance != "a" {
		t.Fatalf("snapshot = %+v", got)
	}
	x.Headers.Upstream = http.Header{"X-Up": {"1"}}
	state.Finish(x)
}
