package plugins

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/pluginapi"
	"github.com/stretchr/testify/require"
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
	require.NoError(t, err)
	require.Equal(t, "rewritten", out.Text(0))
	require.Equal(t, "openai/gpt-4o-mini", captured.Model)
	require.Equal(t, 32, *captured.MaxTokens)
	require.Equal(t, 0.5, *captured.Temperature)
	require.Len(t, captured.Messages, 1)
	require.Equal(t, core.RequestOriginPlugin, core.GetRequestOrigin(capturedCtx))
	got := core.UserPathFromContext(capturedCtx)
	require.Equal(t, "/team/alpha/guardrails/privacy", got)

	h.Metrics().Inc("Calls Total", nil)
	h.Metrics().Observe("latency", 1, nil)
	require.Len(t, sink.names, 2)
	require.Equal(t, "plugin_llm_judge_calls_total", sink.names[0])
	require.Equal(t, "plugin_llm_judge_latency", sink.names[1])
	_, err = h.History(context.Background(), pluginapi.Meta{})
	require.ErrorIs(t, err, ErrHistoryUnavailable)
	require.NotNil(t, h.Logger())
}

func TestHostInferenceUserPathFallsBackToRequest(t *testing.T) {
	var capturedCtx context.Context
	h := NewHost(HostDeps{Chat: chatFunc(func(ctx context.Context, req *core.ChatRequest) (*core.ChatResponse, error) {
		capturedCtx = ctx
		return &core.ChatResponse{}, nil
	})}, HostInfo{PluginName: "p", InstanceName: "i"})
	_, err := h.Inference().Complete(core.WithEffectiveUserPath(context.Background(), "/acme"), pluginapi.InferenceRequest{Model: "m"})
	require.NoError(t, err)
	got := core.UserPathFromContext(capturedCtx)
	require.Equal(t, "/acme/guardrails/i", got)
	_, err = h.Inference().Complete(context.Background(), pluginapi.InferenceRequest{Model: "m", UserPath: "/override"})
	require.NoError(t, err)
	got = core.UserPathFromContext(capturedCtx)
	require.Equal(t, "/override/guardrails/i", got)
}

func TestHostInferenceUnavailable(t *testing.T) {
	h := NewHost(HostDeps{}, HostInfo{PluginName: "p", InstanceName: "i"})
	_, err := h.Inference().Complete(context.Background(), pluginapi.InferenceRequest{Model: "m"})
	require.ErrorIs(t, err, ErrInferenceUnavailable)

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
	require.Equal(t, "req-9", meta.RequestID)
	require.Equal(t, "anthropic_messages", meta.Dialect)
	require.Equal(t, "/v1/messages", meta.Endpoint)
	require.Equal(t, string(core.OperationChatCompletions), meta.Operation)
	require.Equal(t, "/team", meta.UserPath)
	require.Equal(t, "key-1", meta.AuthKeyID)
	require.Equal(t, "sess", meta.SessionID)
	require.Equal(t, "external", meta.Origin)
	require.Equal(t, "smart", meta.RequestedModel)
	require.Equal(t, "claude", meta.Model)
	require.Equal(t, "anthropic", meta.Provider)
	require.Equal(t, "anthropic-eu", meta.ProviderName)
	require.Equal(t, "smart", meta.VirtualModelSource)

	require.Equal(t, "wf-1", meta.WorkflowVersionID)
	require.True(t, meta.Features["cache"])
	require.False(t, meta.Features["usage"])
	meta = WithAttempts(meta, []Attempt{{Seq: 1, Kind: "primary", ProviderType: "anthropic", Success: true}})
	require.Len(t, meta.Attempts, 1)
	require.Equal(t, "anthropic", meta.Attempts[0].Provider)
	bare := MetaFromContext(context.Background(), nil)
	require.Equal(t, "openai", bare.Dialect)
	require.Empty(t, bare.Endpoint)

	ctx, state := WithRequestState(ctx)
	ctx2, again := WithRequestState(ctx)
	require.Equal(t, state, again)
	require.Equal(t, ctx, ctx2)

	x := state.NewExchange(ctx, meta)
	require.Equal(t, "[redacted]", x.Headers.Request.Get("Authorization"))
	require.Equal(t, "t1", x.Headers.Request.Get("X-Trace"), "request headers = %v", x.Headers.Request)

	x.Values.Set("k", 1)
	x.Headers.Response.Set("X-GoModel-Guardrail", "warn; code=x")
	v, _ := state.Values.Get("k")
	require.Equal(t, 1, v)

	dst := http.Header{}
	state.ApplyResponseHeaders(dst)
	require.Equal(t, "warn; code=x", dst.Get("X-GoModel-Guardrail"), "headers = %v", dst)

	state.Record(DecisionRecord{Phase: pluginapi.KindPrompt, Instance: "a"})
	got := state.Snapshot()
	require.Len(t, got, 1)
	require.Equal(t, "a", got[0].Instance)

	x.Headers.Upstream = http.Header{"X-Up": {"1"}}
	state.Finish(x)
}

func TestHostHTTPClient(t *testing.T) {
	h := NewHost(HostDeps{}, HostInfo{PluginName: "presidio", InstanceName: "pii"})
	client := h.HTTPClient()
	require.NotNil(t, client)
	require.Equal(t, PluginHTTPTimeout, client.Timeout)

	transport, ok := client.Transport.(*http.Transport)
	require.True(t, ok)
	require.NotNil(t, transport.Proxy, "default client transport = %T, want an *http.Transport with a proxy function", client.Transport)
	require.Equal(t, PluginHTTPTimeout, transport.ResponseHeaderTimeout)

	other := NewHost(HostDeps{}, HostInfo{PluginName: "other", InstanceName: "b"})
	require.Equal(t, client, other.HTTPClient())

	custom := &http.Client{Timeout: time.Second}
	h = NewHost(HostDeps{HTTP: custom}, HostInfo{PluginName: "presidio", InstanceName: "pii"})
	require.Equal(t, custom, h.HTTPClient())
}
