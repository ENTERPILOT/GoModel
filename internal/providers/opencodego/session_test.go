package opencodego

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
	"github.com/enterpilot/gomodel/internal/version"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// headerServer records every request and answers with a minimal chat
// completion (or SSE stream) so both endpoint dialects succeed.
func headerServer(t *testing.T) (*httptest.Server, *providertest.Capture) {
	t.Helper()
	return providertest.Server(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") == "text/event-stream" {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: [DONE]\n\n"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/messages" {
			_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","role":"assistant","model":"qwen3.7-max","content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"chatcmpl-1","created":1,"model":"glm-5.1","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}]}`))
	})
}

func snapshotContext(ctx context.Context, headers map[string][]string) context.Context {
	snapshot := core.NewRequestSnapshot(http.MethodPost, "/v1/chat/completions", nil, nil, headers, "application/json", nil, false, "req-1", nil)
	return core.WithRequestSnapshot(ctx, snapshot)
}

func chatRequest(model string) *core.ChatRequest {
	return &core.ChatRequest{Model: model, Messages: []core.Message{{Role: "user", Content: "hi"}}}
}

func TestRequestHeaders_DetectedSessionForwarded(t *testing.T) {
	server, capture := headerServer(t)
	ctx := core.WithSessionID(context.Background(), "session-123")

	for _, model := range []string{"glm-5.1", "qwen3.7-max"} {
		t.Run(model, func(t *testing.T) {
			_, err := newTestProvider(server.URL, server.Client()).ChatCompletion(ctx, chatRequest(model))
			require.NoError(t, err)

			got := capture.Last(t).Header
			assert.Equal(t, "session-123", got.Get(sessionHeader))
			assert.Equal(t, defaultClient, got.Get(clientHeader))
			assert.Equal(t, "gomodel/"+version.Version, got.Get("User-Agent"))
		})
	}
}

func TestRequestHeaders_StreamCarriesSession(t *testing.T) {
	server, capture := headerServer(t)
	ctx := core.WithSessionID(context.Background(), "session-stream")

	for _, model := range []string{"glm-5.1", "qwen3.7-max"} {
		t.Run(model, func(t *testing.T) {
			body, err := newTestProvider(server.URL, server.Client()).StreamChatCompletion(ctx, chatRequest(model))
			require.NoError(t, err)

			_, _ = io.ReadAll(body)
			_ = body.Close()
			assert.Equal(t, "session-stream", capture.Last(t).Header.Get(sessionHeader))
		})
	}
}

func TestRequestHeaders_InboundOpenCodeHeadersWin(t *testing.T) {
	server, capture := headerServer(t)
	ctx := core.WithSessionID(context.Background(), "scoped-detected")
	ctx = snapshotContext(ctx, map[string][]string{
		"X-Opencode-Session": {"ses_client"},
		"X-Opencode-Client":  {"pi"},
	})
	_, err := newTestProvider(server.URL, server.Client()).ChatCompletion(ctx, chatRequest("glm-5.1"))
	require.NoError(t, err)

	got := capture.Last(t).Header
	assert.Equal(t, "ses_client", got.Get(sessionHeader))
	assert.Equal(t, "pi", got.Get(clientHeader))
}

func TestRequestHeaders_NoSessionSendsNoSessionHeader(t *testing.T) {
	server, capture := headerServer(t)
	_, err := newTestProvider(server.URL, server.Client()).ChatCompletion(context.Background(), chatRequest("glm-5.1"))
	require.NoError(t, err)

	got := capture.Last(t).Header
	assert.NotContains(t, got, http.CanonicalHeaderKey(sessionHeader), "%s should be absent without a session", sessionHeader)
	assert.Equal(t, defaultClient, got.Get(clientHeader))
}

func TestRequestHeaders_Disabled(t *testing.T) {
	t.Setenv(sessionHeaderEnvVar, "false")
	server, capture := headerServer(t)
	ctx := core.WithSessionID(context.Background(), "session-123")
	ctx = snapshotContext(ctx, map[string][]string{"X-Opencode-Session": {"ses_client"}})

	for _, model := range []string{"glm-5.1", "qwen3.7-max"} {
		t.Run(model, func(t *testing.T) {
			_, err := newTestProvider(server.URL, server.Client()).ChatCompletion(ctx, chatRequest(model))
			require.NoError(t, err)

			got := capture.Last(t).Header
			assert.NotContains(t, got, http.CanonicalHeaderKey(sessionHeader), "%s should be absent when disabled", sessionHeader)
			assert.Equal(t, defaultClient, got.Get(clientHeader), "identification is not gated")
		})
	}
}

func TestNew_FactoryConstructorWiresHeadersOnBothPaths(t *testing.T) {
	// Pin the env-driven defaults so an inherited override cannot disable the
	// header or reroute the /messages model through /chat/completions.
	t.Setenv(sessionHeaderEnvVar, "")
	t.Setenv(messagesModelsEnvVar, "qwen3.7-max")
	server, capture := headerServer(t)
	ctx := core.WithSessionID(context.Background(), "session-factory")
	provider := New(providers.ProviderConfig{APIKey: "sk-opencode", BaseURL: server.URL}, providers.ProviderOptions{})

	tests := []struct {
		model    string
		wantPath string
	}{
		{model: "glm-5.1", wantPath: "/chat/completions"},
		{model: "qwen3.7-max", wantPath: "/messages"},
	}
	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			_, err := provider.ChatCompletion(ctx, chatRequest(tt.model))
			require.NoError(t, err)

			got := capture.Last(t)
			assert.Equal(t, tt.wantPath, got.Path)
			assert.Equal(t, "session-factory", got.Header.Get(sessionHeader))
		})
	}
}

func TestPassthrough_FillsMissingIdentificationHeaders(t *testing.T) {
	server, capture := headerServer(t)
	ctx := core.WithSessionID(context.Background(), "session-pass")

	resp, err := newTestProvider(server.URL, server.Client()).Passthrough(ctx, &core.PassthroughRequest{
		Method:   http.MethodPost,
		Endpoint: "chat/completions",
		Body:     io.NopCloser(strings.NewReader(`{"model":"glm-5.1","messages":[{"role":"user","content":"hi"}]}`)),
		Headers:  http.Header{"Content-Type": {"application/json"}, "X-Opencode-Client": {"curl-script"}},
	})
	require.NoError(t, err)

	_ = resp.Body.Close()
	got := capture.Last(t).Header
	assert.Equal(t, "session-pass", got.Get(sessionHeader))
	assert.Equal(t, "curl-script", got.Get(clientHeader))
	assert.Equal(t, "gomodel/"+version.Version, got.Get("User-Agent"))
}

func TestWithDefaultHeaders(t *testing.T) {
	defaults := http.Header{"X-Opencode-Session": {"gw"}, "User-Agent": {"gomodel/dev"}}
	merged := withDefaultHeaders(nil, defaults)
	assert.Equal(t, "gw", merged.Get("X-Opencode-Session"))
	assert.Equal(t, "gomodel/dev", merged.Get("User-Agent"), "nil headers should take every default")

	// A non-canonical caller key still counts as present and is not duplicated.
	caller := http.Header{}
	caller[strings.ToLower("X-Opencode-Session")] = []string{"mine"}
	merged = withDefaultHeaders(caller, defaults)
	var sessionValues []string
	for key, values := range merged {
		if strings.EqualFold(key, "X-Opencode-Session") {
			sessionValues = append(sessionValues, values...)
		}
	}
	assert.Equal(t, []string{"mine"}, sessionValues, "caller value should win without duplication")
	assert.Equal(t, "gomodel/dev", merged.Get("User-Agent"), "missing default should be added")
	assert.Len(t, caller, 1)
}

func TestInboundHeader_RejectsLineBreaks(t *testing.T) {
	ctx := snapshotContext(context.Background(), map[string][]string{
		"X-Opencode-Session": {"bad\r\nvalue", "  good  "},
	})
	assert.Equal(t, "good", inboundHeader(ctx, sessionHeader))
	assert.Empty(t, inboundHeader(context.Background(), sessionHeader))
}

func TestLoadSessionHeaderEnabled(t *testing.T) {
	tests := []struct {
		name  string
		value string
		unset bool
		want  bool
	}{
		{name: "unset defaults on", unset: true, want: true},
		{name: "empty defaults on", value: "  ", want: true},
		{name: "false", value: "false", want: false},
		{name: "zero", value: "0", want: false},
		{name: "true", value: "true", want: true},
		{name: "garbage defaults on", value: "maybe", want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.unset {
				t.Setenv(sessionHeaderEnvVar, "") // registers cleanup, then unset for real
				_ = os.Unsetenv(sessionHeaderEnvVar)
			} else {
				t.Setenv(sessionHeaderEnvVar, tt.value)
			}
			assert.Equal(t, tt.want, loadSessionHeaderEnabled())
		})
	}
}
