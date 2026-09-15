package llmd

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRegistrationRequiresEndpointAndAllowsKeyless(t *testing.T) {
	require.Equal(t, "llmd", Registration.Type)
	require.True(t, Registration.Discovery.RequireBaseURL)
	require.True(t, Registration.Discovery.AllowAPIKeyless)
}

func TestChatCompletionInjectsTrustedLLMDHeaders(t *testing.T) {
	trustedCtx := core.WithEffectiveUserPath(context.Background(), "/team/alpha")
	trustedCtx = core.WithRequestID(trustedCtx, "req-llmd-1")
	tests := []struct {
		name        string
		apiKey      string
		controls    ControlConfig
		ctx         context.Context
		wantHeaders map[string]string
	}{
		{
			name:   "configured controls",
			apiKey: "router-token",
			controls: ControlConfig{
				InferenceObjective:   "premium-traffic",
				FairnessFromUserPath: true,
			},
			ctx: trustedCtx,
			wantHeaders: map[string]string{
				"Authorization":          "Bearer router-token",
				"X-Request-Id":           "req-llmd-1",
				canonicalObjectiveHeader: "premium-traffic",
				legacyObjectiveHeader:    "premium-traffic",
				canonicalFairnessHeader:  "/team/alpha",
				legacyFairnessHeader:     "/team/alpha",
			},
		},
		{
			name:     "keyless without controls",
			ctx:      context.Background(),
			controls: ControlConfig{},
			wantHeaders: map[string]string{
				"Authorization":          "",
				canonicalObjectiveHeader: "",
				legacyObjectiveHeader:    "",
				canonicalFairnessHeader:  "",
				legacyFairnessHeader:     "",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, capture := providertest.JSONServer(t, http.StatusOK, `{
				"id":"chatcmpl-llmd",
				"created":1677652288,
				"model":"Qwen/Qwen2.5-0.5B-Instruct",
				"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]
			}`)

			provider := NewWithHTTPClient(tt.apiKey, server.URL, tt.controls, server.Client(), llmclient.Hooks{})
			resp, err := provider.ChatCompletion(tt.ctx, &core.ChatRequest{
				Model:    "Qwen/Qwen2.5-0.5B-Instruct",
				Messages: []core.Message{{Role: "user", Content: "hello"}},
			})
			require.NoError(t, err)
			assert.Equal(t, "chatcmpl-llmd", resp.ID)
			assert.Equal(t, "Qwen/Qwen2.5-0.5B-Instruct", resp.Model)
			require.Len(t, resp.Choices, 1)
			assert.Equal(t, "assistant", resp.Choices[0].Message.Role)
			assert.Equal(t, "ok", resp.Choices[0].Message.Content)

			got := capture.Last(t).Header
			for key, want := range tt.wantHeaders {
				assert.Equal(t, want, got.Get(key), "header %s", key)
			}
		})
	}
}

func TestPassthroughReplacesClientSuppliedControlHeaders(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, `{"tokens":[1,2,3]}`)

	provider := NewWithHTTPClient("router-token", server.URL+"/v1", ControlConfig{
		InferenceObjective:   "trusted-objective",
		FairnessFromUserPath: true,
	}, server.Client(), llmclient.Hooks{})
	ctx := core.WithEffectiveUserPath(context.Background(), "/trusted/tenant")

	for _, endpoint := range []string{"tokenize", "chat/completions"} {
		resp, err := provider.Passthrough(ctx, &core.PassthroughRequest{
			Method:   http.MethodPost,
			Endpoint: endpoint,
			Body:     io.NopCloser(strings.NewReader(`{}`)),
			Headers: http.Header{
				"Content-Type":                          {"application/json"},
				"Authorization":                         {"Bearer client-token"},
				"X-Api-Key":                             {"client-api-key"},
				"Api-Key":                               {"client-azure-key"},
				"X-Goog-Api-Key":                        {"client-google-key"},
				canonicalObjectiveHeader:                {"attacker-objective"},
				legacyFairnessHeader:                    {"attacker-tenant"},
				"X-Llm-D-Slo-Ttft-Ms":                   {"1"},
				"X-Gateway-Model-Name-Rewrite":          {"other-model"},
				"X-Gateway-Destination-Endpoint-Served": {"10.0.0.1:8000"},
			},
		})
		require.NoError(t, err)

		_ = resp.Body.Close()
	}

	got := capture.All()
	require.Len(t, got, 2)

	for i, req := range got {
		headers := req.Header
		assert.Equal(t, "Bearer router-token", headers.Get("Authorization"), "request %d", i)
		assert.Equal(t, "trusted-objective", headers.Get(canonicalObjectiveHeader), "request %d", i)
		assert.Equal(t, "trusted-objective", headers.Get(legacyObjectiveHeader), "request %d", i)
		assert.Equal(t, "/trusted/tenant", headers.Get(canonicalFairnessHeader), "request %d", i)
		assert.Equal(t, "/trusted/tenant", headers.Get(legacyFairnessHeader), "request %d", i)
		for _, key := range []string{
			"X-Api-Key",
			"Api-Key",
			"X-Goog-Api-Key",
			"X-Llm-D-Slo-Ttft-Ms",
			"X-Gateway-Model-Name-Rewrite",
			"X-Gateway-Destination-Endpoint-Served",
		} {
			assert.Empty(t, headers.Get(key), "request %d: %s should be stripped", i, key)
		}
	}
}

func TestPassthroughPreservesDroppedReasonOnRawErrorResponses(t *testing.T) {
	server, _ := providertest.Server(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set(droppedReasonHeader, "rejected-saturated")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"request dropped"}}`))
	})

	provider := NewWithHTTPClient("", server.URL+"/v1", ControlConfig{}, server.Client(), llmclient.Hooks{})
	for _, endpoint := range []string{"tokenize", "chat/completions"} {
		resp, err := provider.Passthrough(context.Background(), &core.PassthroughRequest{
			Method:   http.MethodPost,
			Endpoint: endpoint,
			Body:     io.NopCloser(strings.NewReader(`{}`)),
		})
		require.NoError(t, err)

		_ = resp.Body.Close()
		assert.Equal(t, http.StatusTooManyRequests, resp.StatusCode, "Passthrough(%q)", endpoint)
		assert.Equal(t, "rejected-saturated", http.Header(resp.Headers).Get(droppedReasonHeader), "Passthrough(%q)", endpoint)
	}
}

func TestPassthroughSelectsV1AndRouterRootPaths(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, `{}`)

	provider := NewWithHTTPClient("", server.URL+"/v1", ControlConfig{}, server.Client(), llmclient.Hooks{})
	for _, endpoint := range []string{"completions", "messages", "inference/v1/generate", "tokenize"} {
		resp, err := provider.Passthrough(context.Background(), &core.PassthroughRequest{
			Method:   http.MethodPost,
			Endpoint: endpoint,
			Body:     io.NopCloser(strings.NewReader(`{}`)),
		})
		require.NoError(t, err)

		_ = resp.Body.Close()
	}

	var gotPaths []string
	for _, req := range capture.All() {
		gotPaths = append(gotPaths, req.Path)
	}
	assert.Equal(t, []string{"/v1/completions", "/v1/messages", "/inference/v1/generate", "/tokenize"}, gotPaths)
}

func TestCompatibleAndRootClientsShareKeyRotation(t *testing.T) {
	server, capture := providertest.Server(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/chat/completions" {
			_, _ = w.Write([]byte(`{"id":"chatcmpl-1","model":"test","choices":[]}`))
			return
		}
		_, _ = w.Write([]byte(`{}`))
	})

	provider := newProvider("", server.URL+"/v1", ControlConfig{}, providers.ProviderOptions{
		Keys: providers.NewKeyring("router-a", "router-b"),
	}, server.Client())
	passthrough, err := provider.Passthrough(context.Background(), &core.PassthroughRequest{
		Method:   http.MethodPost,
		Endpoint: "tokenize",
		Body:     io.NopCloser(strings.NewReader(`{}`)),
	})
	require.NoError(t, err)

	_ = passthrough.Body.Close()
	_, err = provider.ChatCompletion(context.Background(), &core.ChatRequest{
		Model:    "test",
		Messages: []core.Message{{Role: "user", Content: "hello"}},
	})
	require.NoError(t, err)

	var gotAuth []string
	for _, req := range capture.All() {
		gotAuth = append(gotAuth, req.Header.Get("Authorization"))
	}
	assert.Equal(t, []string{"Bearer router-a", "Bearer router-b"}, gotAuth)
}

func TestFairnessHeaderCanBeDisabled(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "http://llmd.invalid/v1/chat/completions", nil)
	request = request.WithContext(core.WithEffectiveUserPath(request.Context(), "/team/alpha"))
	provider := &Provider{controls: ControlConfig{FairnessFromUserPath: false}}

	provider.setHeaders(request, "")
	assert.Empty(t, request.Header.Get(canonicalFairnessHeader))
	assert.Empty(t, request.Header.Get(legacyFairnessHeader))
}

func TestFairnessHeaderIgnoresClientAssertedSnapshotPath(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "http://llmd.invalid/v1/chat/completions", nil)
	snapshot := core.NewRequestSnapshot(
		http.MethodPost, "/v1/chat/completions", nil, nil, nil, "application/json", nil, false, "", nil,
		"/untrusted/client",
	)
	request = request.WithContext(core.WithRequestSnapshot(request.Context(), snapshot))
	provider := &Provider{controls: ControlConfig{FairnessFromUserPath: true}}

	provider.setHeaders(request, "")
	assert.Empty(t, request.Header.Get(canonicalFairnessHeader))
	assert.Empty(t, request.Header.Get(legacyFairnessHeader))
}

func TestExposeDroppedReasonReturnsOnlySafeLLMDHeader(t *testing.T) {
	upstream := core.NewRateLimitError("llmd", "request dropped")
	upstream.ResponseHeaders = http.Header{
		droppedReasonHeader: {"rejected-saturated"},
		"Set-Cookie":        {"secret=value"},
	}

	wrapped := exposeDroppedReason(upstream)
	require.ErrorIs(t, wrapped, upstream)

	headerErr, ok := wrapped.(interface{ ResponseHeaders() http.Header })
	require.True(t, ok, "wrapped error type %T does not expose response headers", wrapped)

	headers := headerErr.ResponseHeaders()
	assert.Equal(t, "rejected-saturated", headers.Get(droppedReasonHeader))
	assert.Empty(t, headers.Get("Set-Cookie"))
}

func TestProviderDoesNotAdvertiseUnsupportedNativeSurfaces(t *testing.T) {
	provider := NewWithHTTPClient("", "http://llmd.invalid/v1", ControlConfig{}, nil, llmclient.Hooks{})
	providertest.AssertNoNativeSurfaces(t, provider)
	_, ok := any(provider).(core.NativeResponseLifecycleProvider)
	require.False(t, ok)
}
