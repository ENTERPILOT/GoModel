package llmd

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers"
)

func TestRegistrationRequiresEndpointAndAllowsKeyless(t *testing.T) {
	if Registration.Type != "llmd" {
		t.Fatalf("Registration.Type = %q, want llmd", Registration.Type)
	}
	if !Registration.Discovery.RequireBaseURL {
		t.Fatal("llmd base URL must be required")
	}
	if !Registration.Discovery.AllowAPIKeyless {
		t.Fatal("llmd must allow a keyless Gateway")
	}
}

func TestChatCompletionInjectsTrustedLLMDHeaders(t *testing.T) {
	var got http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"chatcmpl-llmd",
			"created":1677652288,
			"model":"Qwen/Qwen2.5-0.5B-Instruct",
			"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]
		}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("router-token", server.URL, ControlConfig{
		InferenceObjective:   "premium-traffic",
		FairnessFromUserPath: true,
	}, server.Client(), llmclient.Hooks{})
	ctx := core.WithEffectiveUserPath(context.Background(), "/team/alpha")
	ctx = core.WithRequestID(ctx, "req-llmd-1")

	_, err := provider.ChatCompletion(ctx, &core.ChatRequest{
		Model:    "Qwen/Qwen2.5-0.5B-Instruct",
		Messages: []core.Message{{Role: "user", Content: "hello"}},
	})
	if err != nil {
		t.Fatalf("ChatCompletion() error = %v", err)
	}

	assertHeader(t, got, "Authorization", "Bearer router-token")
	assertHeader(t, got, "X-Request-Id", "req-llmd-1")
	assertHeader(t, got, canonicalObjectiveHeader, "premium-traffic")
	assertHeader(t, got, legacyObjectiveHeader, "premium-traffic")
	assertHeader(t, got, canonicalFairnessHeader, "/team/alpha")
	assertHeader(t, got, legacyFairnessHeader, "/team/alpha")
}

func TestPassthroughReplacesClientSuppliedControlHeaders(t *testing.T) {
	var got http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"tokens":[1,2,3]}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("", server.URL+"/v1", ControlConfig{
		InferenceObjective:   "trusted-objective",
		FairnessFromUserPath: true,
	}, server.Client(), llmclient.Hooks{})
	ctx := core.WithEffectiveUserPath(context.Background(), "/trusted/tenant")

	resp, err := provider.Passthrough(ctx, &core.PassthroughRequest{
		Method:   http.MethodPost,
		Endpoint: "tokenize",
		Body:     io.NopCloser(strings.NewReader(`{}`)),
		Headers: http.Header{
			"Content-Type":                          {"application/json"},
			canonicalObjectiveHeader:                {"attacker-objective"},
			legacyFairnessHeader:                    {"attacker-tenant"},
			"X-Llm-D-Slo-Ttft-Ms":                   {"1"},
			"X-Gateway-Model-Name-Rewrite":          {"other-model"},
			"X-Gateway-Destination-Endpoint-Served": {"10.0.0.1:8000"},
		},
	})
	if err != nil {
		t.Fatalf("Passthrough() error = %v", err)
	}
	defer resp.Body.Close()

	assertHeader(t, got, canonicalObjectiveHeader, "trusted-objective")
	assertHeader(t, got, legacyObjectiveHeader, "trusted-objective")
	assertHeader(t, got, canonicalFairnessHeader, "/trusted/tenant")
	assertHeader(t, got, legacyFairnessHeader, "/trusted/tenant")
	for _, key := range []string{
		"X-Llm-D-Slo-Ttft-Ms",
		"X-Gateway-Model-Name-Rewrite",
		"X-Gateway-Destination-Endpoint-Served",
	} {
		if value := got.Get(key); value != "" {
			t.Errorf("%s = %q, want stripped", key, value)
		}
	}
}

func TestPassthroughSelectsV1AndRouterRootPaths(t *testing.T) {
	var gotPaths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPaths = append(gotPaths, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("", server.URL+"/v1", ControlConfig{}, server.Client(), llmclient.Hooks{})
	for _, endpoint := range []string{"completions", "messages", "inference/v1/generate", "tokenize"} {
		resp, err := provider.Passthrough(context.Background(), &core.PassthroughRequest{
			Method:   http.MethodPost,
			Endpoint: endpoint,
			Body:     io.NopCloser(strings.NewReader(`{}`)),
		})
		if err != nil {
			t.Fatalf("Passthrough(%q) error = %v", endpoint, err)
		}
		_ = resp.Body.Close()
	}

	want := []string{"/v1/completions", "/v1/messages", "/inference/v1/generate", "/tokenize"}
	if len(gotPaths) != len(want) {
		t.Fatalf("paths = %v, want %v", gotPaths, want)
	}
	for i := range want {
		if gotPaths[i] != want[i] {
			t.Errorf("path[%d] = %q, want %q", i, gotPaths[i], want[i])
		}
	}
}

func TestCompatibleAndRootClientsShareKeyRotation(t *testing.T) {
	var gotAuth []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = append(gotAuth, r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/chat/completions" {
			_, _ = w.Write([]byte(`{"id":"chatcmpl-1","model":"test","choices":[]}`))
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	provider := newProvider("", server.URL+"/v1", ControlConfig{}, providers.ProviderOptions{
		Keys: providers.NewKeyring("router-a", "router-b"),
	}, server.Client())
	passthrough, err := provider.Passthrough(context.Background(), &core.PassthroughRequest{
		Method:   http.MethodPost,
		Endpoint: "tokenize",
		Body:     io.NopCloser(strings.NewReader(`{}`)),
	})
	if err != nil {
		t.Fatalf("Passthrough() error = %v", err)
	}
	_ = passthrough.Body.Close()
	if _, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
		Model:    "test",
		Messages: []core.Message{{Role: "user", Content: "hello"}},
	}); err != nil {
		t.Fatalf("ChatCompletion() error = %v", err)
	}

	want := []string{"Bearer router-a", "Bearer router-b"}
	if len(gotAuth) != len(want) {
		t.Fatalf("Authorization headers = %v, want %v", gotAuth, want)
	}
	for i := range want {
		if gotAuth[i] != want[i] {
			t.Errorf("Authorization[%d] = %q, want %q", i, gotAuth[i], want[i])
		}
	}
}

func TestFairnessHeaderCanBeDisabled(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "http://llmd.invalid/v1/chat/completions", nil)
	request = request.WithContext(core.WithEffectiveUserPath(request.Context(), "/team/alpha"))
	provider := &Provider{controls: ControlConfig{FairnessFromUserPath: false}}

	provider.setHeaders(request, "")

	if value := request.Header.Get(canonicalFairnessHeader); value != "" {
		t.Fatalf("canonical fairness header = %q, want empty", value)
	}
	if value := request.Header.Get(legacyFairnessHeader); value != "" {
		t.Fatalf("legacy fairness header = %q, want empty", value)
	}
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

	if value := request.Header.Get(canonicalFairnessHeader); value != "" {
		t.Fatalf("canonical fairness header = %q, want empty", value)
	}
	if value := request.Header.Get(legacyFairnessHeader); value != "" {
		t.Fatalf("legacy fairness header = %q, want empty", value)
	}
}

func TestExposeDroppedReasonReturnsOnlySafeLLMDHeader(t *testing.T) {
	upstream := core.NewRateLimitError("llmd", "request dropped")
	upstream.ResponseHeaders = http.Header{
		droppedReasonHeader: {"rejected-saturated"},
		"Set-Cookie":        {"secret=value"},
	}

	wrapped := exposeDroppedReason(upstream)
	if !errors.Is(wrapped, upstream) {
		t.Fatal("wrapped error must preserve the upstream error chain")
	}
	headerErr, ok := wrapped.(interface{ ResponseHeaders() http.Header })
	if !ok {
		t.Fatalf("wrapped error type %T does not expose response headers", wrapped)
	}
	headers := headerErr.ResponseHeaders()
	assertHeader(t, headers, droppedReasonHeader, "rejected-saturated")
	if value := headers.Get("Set-Cookie"); value != "" {
		t.Fatalf("Set-Cookie = %q, want filtered", value)
	}
}

func TestProviderDoesNotAdvertiseUnsupportedNativeSurfaces(t *testing.T) {
	provider := NewWithHTTPClient("", "http://llmd.invalid/v1", ControlConfig{}, nil, llmclient.Hooks{})
	if _, ok := any(provider).(core.NativeBatchProvider); ok {
		t.Fatal("llmd provider must not advertise the separate llm-d Batch Gateway")
	}
	if _, ok := any(provider).(core.NativeFileProvider); ok {
		t.Fatal("llmd provider must not advertise native files")
	}
	if _, ok := any(provider).(core.NativeResponseLifecycleProvider); ok {
		t.Fatal("llmd provider must not advertise Responses lifecycle operations")
	}
}

func assertHeader(t *testing.T, headers http.Header, key, want string) {
	t.Helper()
	if got := headers.Get(key); got != want {
		t.Errorf("%s = %q, want %q", key, got, want)
	}
}
