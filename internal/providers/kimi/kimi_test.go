package kimi

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gomodel/internal/core"
	"gomodel/internal/llmclient"
	"gomodel/internal/providers"
	"gomodel/internal/providers/openai"
)

// forbiddenHeaders are headers that must never be present on outbound Kimi
// requests. Kimi's API does not require them and the gateway must not invent
// them on its behalf.
var forbiddenHeaders = []string{
	"X-Stainless-Arch",
	"X-Stainless-Os",
	"X-Stainless-Package-Version",
	"X-Stainless-Runtime",
	"X-Stainless-Raw-Response",
	"Http-Referer",
	"X-Title",
}

// TestRegistration_Type asserts the factory registration advertises the
// expected provider type string.
func TestRegistration_Type(t *testing.T) {
	if Registration.Type != "kimi" {
		t.Errorf("Registration.Type = %q, want %q", Registration.Type, "kimi")
	}
}

// TestRegistration_HasDefaultBaseURL asserts the registration carries the
// documented Kimi default base URL so the factory can use it when no override
// is supplied.
func TestRegistration_HasDefaultBaseURL(t *testing.T) {
	if Registration.Discovery.DefaultBaseURL != defaultBaseURL {
		t.Errorf("Registration.Discovery.DefaultBaseURL = %q, want %q",
			Registration.Discovery.DefaultBaseURL, defaultBaseURL)
	}
}

// TestNew_ReturnsNonNilProvider asserts the public constructor returns a
// usable core.Provider implementation.
func TestNew_ReturnsNonNilProvider(t *testing.T) {
	p := New(providers.ProviderConfig{APIKey: "test-api-key"}, providers.ProviderOptions{})
	if p == nil {
		t.Fatal("New() returned nil")
	}
}

// TestNew_DelegatesToCompatibleProvider asserts the provider wraps a
// *openai.CompatibleProvider. This is the structural contract that lets Kimi
// reuse the shared OpenAI-compatible transport.
func TestNew_DelegatesToCompatibleProvider(t *testing.T) {
	p := New(providers.ProviderConfig{APIKey: "test-api-key"}, providers.ProviderOptions{})

	kp, ok := p.(*Provider)
	if !ok {
		t.Fatalf("New() returned %T, want *kimi.Provider", p)
	}
	if kp.compat == nil {
		t.Fatal("Provider.compat should not be nil")
	}
}

// TestNewWithHTTPClient_DelegatesToCompatibleProvider asserts the
// HTTP-client-aware constructor also wires up the shared compatible provider.
func TestNewWithHTTPClient_DelegatesToCompatibleProvider(t *testing.T) {
	p := NewWithHTTPClient("test-api-key", nil, llmclient.Hooks{})
	if p == nil {
		t.Fatal("NewWithHTTPClient() returned nil")
	}
	if p.compat == nil {
		t.Fatal("Provider.compat should not be nil")
	}
	if _, ok := any(p).(core.Provider); !ok {
		t.Fatal("*Provider should satisfy core.Provider")
	}
}

// TestNew_DefaultBaseURL asserts that omitting BaseURL on the public
// constructor resolves to the package-level default base URL.
func TestNew_DefaultBaseURL(t *testing.T) {
	p := New(providers.ProviderConfig{APIKey: "test-api-key"}, providers.ProviderOptions{})
	kp, ok := p.(*Provider)
	if !ok {
		t.Fatalf("New() returned %T, want *kimi.Provider", p)
	}
	if got := kp.GetBaseURL(); got != defaultBaseURL {
		t.Errorf("GetBaseURL() = %q, want %q", got, defaultBaseURL)
	}
}

// TestNewWithHTTPClient_DefaultBaseURL asserts the same default for the
// HTTP-client-aware constructor.
func TestNewWithHTTPClient_DefaultBaseURL(t *testing.T) {
	p := NewWithHTTPClient("test-api-key", nil, llmclient.Hooks{})
	if got := p.GetBaseURL(); got != defaultBaseURL {
		t.Errorf("GetBaseURL() = %q, want %q", got, defaultBaseURL)
	}
}

// TestNew_CustomBaseURLOverridesDefault asserts a caller-supplied BaseURL
// takes precedence over the package default.
func TestNew_CustomBaseURLOverridesDefault(t *testing.T) {
	const custom = "https://example.test/v1"
	p := New(providers.ProviderConfig{
		APIKey:  "test-api-key",
		BaseURL: custom,
	}, providers.ProviderOptions{})

	kp, ok := p.(*Provider)
	if !ok {
		t.Fatalf("New() returned %T, want *kimi.Provider", p)
	}
	if got := kp.GetBaseURL(); got != custom {
		t.Errorf("GetBaseURL() = %q, want %q", got, custom)
	}
}

// TestChatCompletion_PlainBearerAuthOnly asserts that an outbound Kimi chat
// completion request carries exactly one Authorization header with the API key
// and no other provider-specific headers.
func TestChatCompletion_PlainBearerAuthOnly(t *testing.T) {
	var captured http.Header
	var capturedPath string
	var capturedMethod string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = r.Header.Clone()
		capturedPath = r.URL.Path
		capturedMethod = r.Method

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"id": "chatcmpl-kimi-1",
			"object": "chat.completion",
			"created": 1700000000,
			"model": "kimi-k2-0711-preview",
			"choices": [{
				"index": 0,
				"message": {"role": "assistant", "content": "hi"},
				"finish_reason": "stop"
			}],
			"usage": {"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2}
		}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("kimi-test-key", server.Client(), llmclient.Hooks{})
	provider.SetBaseURL(server.URL)

	resp, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
		Model: "kimi-k2-0711-preview",
		Messages: []core.Message{
			{Role: "user", Content: "Hello"},
		},
	})
	if err != nil {
		t.Fatalf("ChatCompletion() error = %v", err)
	}
	if resp == nil || len(resp.Choices) != 1 {
		t.Fatalf("ChatCompletion() returned unexpected response: %+v", resp)
	}

	if capturedMethod != http.MethodPost {
		t.Errorf("method = %q, want %q", capturedMethod, http.MethodPost)
	}
	if capturedPath != "/chat/completions" {
		t.Errorf("path = %q, want %q", capturedPath, "/chat/completions")
	}

	// Exactly one Authorization header, with "Bearer <api-key>" value.
	auth := captured.Values("Authorization")
	if len(auth) != 1 {
		t.Fatalf("Authorization header count = %d, want 1", len(auth))
	}
	if auth[0] != "Bearer kimi-test-key" {
		t.Errorf("Authorization = %q, want %q", auth[0], "Bearer kimi-test-key")
	}

	// No provider-specific headers must leak onto the outbound request.
	for _, h := range forbiddenHeaders {
		if v := captured.Get(h); v != "" {
			t.Errorf("unexpected %s header present: %q", h, v)
		}
	}

	// Content-Type must still be JSON (the only "non-auth" header we expect).
	if ct := captured.Get("Content-Type"); ct == "" {
		t.Error("Content-Type header missing")
	}
}

// TestListModels_PlainBearerAuthOnly asserts the same plain-Bearer contract
// for the ListModels endpoint, which is also routed through the compat layer.
func TestListModels_PlainBearerAuthOnly(t *testing.T) {
	var captured http.Header

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = r.Header.Clone()

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"object":"list","data":[]}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("kimi-test-key", server.Client(), llmclient.Hooks{})
	provider.SetBaseURL(server.URL)

	if _, err := provider.ListModels(context.Background()); err != nil {
		t.Fatalf("ListModels() error = %v", err)
	}

	auth := captured.Values("Authorization")
	if len(auth) != 1 {
		t.Fatalf("Authorization header count = %d, want 1", len(auth))
	}
	if !strings.HasPrefix(auth[0], "Bearer ") {
		t.Errorf("Authorization = %q, want Bearer prefix", auth[0])
	}

	for _, h := range forbiddenHeaders {
		if v := captured.Get(h); v != "" {
			t.Errorf("unexpected %s header present: %q", h, v)
		}
	}
}

// TestProvider_SatisfiesCoreProviderInterface is a compile-time check that
// *Provider continues to implement core.Provider. If a method is added to
// the interface and not delegated here, this test fails to build.
func TestProvider_SatisfiesCoreProviderInterface(t *testing.T) {
	var _ core.Provider = (*Provider)(nil)
	var _ core.Provider = New(providers.ProviderConfig{APIKey: "k"}, providers.ProviderOptions{})
}

// TestSetBaseURL_OverridesDefault asserts the SetBaseURL passthrough updates
// the underlying compatible provider.
func TestSetBaseURL_OverridesDefault(t *testing.T) {
	provider := NewWithHTTPClient("kimi-test-key", nil, llmclient.Hooks{})
	const custom = "https://kimi.example.test/v1"
	provider.SetBaseURL(custom)

	if got := provider.GetBaseURL(); got != custom {
		t.Errorf("GetBaseURL() = %q, want %q", got, custom)
	}
}

// TestStreamChatCompletion_PlainBearerAuthOnly asserts streaming requests
// also use only Bearer auth.
func TestStreamChatCompletion_PlainBearerAuthOnly(t *testing.T) {
	var captured http.Header
	var bodyBytes []byte

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = r.Header.Clone()
		bodyBytes, _ = io.ReadAll(r.Body)

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: {\"id\":\"x\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"},\"finish_reason\":null}]}\n\ndata: [DONE]\n"))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("kimi-test-key", server.Client(), llmclient.Hooks{})
	provider.SetBaseURL(server.URL)

	body, err := provider.StreamChatCompletion(context.Background(), &core.ChatRequest{
		Model:    "kimi-k2-0711-preview",
		Messages: []core.Message{{Role: "user", Content: "Hello"}},
	})
	if err != nil {
		t.Fatalf("StreamChatCompletion() error = %v", err)
	}
	defer func() { _ = body.Close() }()

	auth := captured.Values("Authorization")
	if len(auth) != 1 || auth[0] != "Bearer kimi-test-key" {
		t.Errorf("Authorization headers = %v, want exactly [\"Bearer kimi-test-key\"]", auth)
	}

	for _, h := range forbiddenHeaders {
		if v := captured.Get(h); v != "" {
			t.Errorf("unexpected %s header present: %q", h, v)
		}
	}

	if len(bodyBytes) == 0 {
		t.Error("request body was empty")
	}
}

// TestCompatProviderType ensures the delegation target is exactly the
// shared OpenAI-compatible provider implementation. This guards against
// accidental swaps to a different transport type.
func TestCompatProviderType(t *testing.T) {
	p := NewWithHTTPClient("kimi-test-key", nil, llmclient.Hooks{})
	if p.compat == nil {
		t.Fatal("Provider.compat should not be nil")
	}
	var _ *openai.CompatibleProvider = p.compat
}

// kimiHeaderServer wraps an httptest.Server whose handler records the headers
// of the most recent inbound request. Tests inspect captures via the returned
// getter to keep assertions on the same goroutine that served the request.
func kimiHeaderServer(t *testing.T) (*httptest.Server, func() http.Header) {
	t.Helper()
	var last http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		last = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id": "chatcmpl-kimi-1",
			"object": "chat.completion",
			"created": 1700000000,
			"model": "kimi-k2-0711-preview",
			"choices": [{
				"index": 0,
				"message": {"role": "assistant", "content": "hi"},
				"finish_reason": "stop"
			}],
			"usage": {"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2}
		}`))
	}))
	t.Cleanup(server.Close)
	return server, func() http.Header { return last }
}

// kimiSnapshotContext builds a context carrying a RequestSnapshot populated
// with the supplied inbound headers, so the header applier observes them as
// if they had arrived at ingress.
func kimiSnapshotContext(t *testing.T, headers map[string][]string) context.Context {
	t.Helper()
	snapshot := core.NewRequestSnapshot(
		http.MethodPost,
		"/v1/chat/completions",
		nil,
		nil,
		headers,
		"",
		nil,
		false,
		"",
		nil,
	)
	return core.WithRequestSnapshot(context.Background(), snapshot)
}

// TestChatCompletion_PassthroughDefaultTrueForwardsInboundHeaders asserts that
// the kimi provider — when constructed with PassthroughUserHeaders: true, the
// resolved value of buildProviderConfig's default for "kimi" — forwards the
// non-skipped inbound headers onto the outbound chat completion request while
// still installing the bearer Authorization set by bearerSetHeaders. The
// default-on behavior itself is covered in providers/config_test.go; this
// test pins the wiring through kimi.New → CompatibleProviderConfig.
func TestChatCompletion_PassthroughDefaultTrueForwardsInboundHeaders(t *testing.T) {
	server, captured := kimiHeaderServer(t)

	provider := New(providers.ProviderConfig{
		APIKey:                 "kimi-test-key",
		BaseURL:                server.URL,
		PassthroughUserHeaders: true, // resolved value of kimi's default-true
	}, providers.ProviderOptions{})

	ctx := kimiSnapshotContext(t, map[string][]string{
		"X-Tenant-Id": {"acme"},
		"X-Trace-Id":  {"trace-abc"},
	})

	if _, err := provider.ChatCompletion(ctx, &core.ChatRequest{
		Model:    "kimi-k2-0711-preview",
		Messages: []core.Message{{Role: "user", Content: "Hello"}},
	}); err != nil {
		t.Fatalf("ChatCompletion() error = %v", err)
	}

	got := captured()
	if v := got.Get("X-Tenant-Id"); v != "acme" {
		t.Errorf("X-Tenant-Id = %q, want acme (default true must forward)", v)
	}
	if v := got.Get("X-Trace-Id"); v != "trace-abc" {
		t.Errorf("X-Trace-Id = %q, want trace-abc (default true must forward)", v)
	}
	if v := got.Get("Authorization"); v != "Bearer kimi-test-key" {
		t.Errorf("Authorization = %q, want Bearer kimi-test-key", v)
	}
}

// TestChatCompletion_PassthroughSkipsReservedInboundHeaders asserts that when
// passthrough is enabled, reserved hop-by-hop / credential / cookie headers
// from the inbound snapshot never reach the upstream Kimi call. In particular,
// Authorization must be preserved as the bearer credential installed by
// bearerSetHeaders, not overwritten by a hostile inbound value.
func TestChatCompletion_PassthroughSkipsReservedInboundHeaders(t *testing.T) {
	server, captured := kimiHeaderServer(t)

	provider := New(providers.ProviderConfig{
		APIKey:                 "kimi-test-key",
		BaseURL:                server.URL,
		PassthroughUserHeaders: true,
	}, providers.ProviderOptions{})

	ctx := kimiSnapshotContext(t, map[string][]string{
		"Authorization":   {"Bearer inbound-attacker"},
		"X-Api-Key":       {"inbound-key"},
		"Cookie":          {"session=abc"},
		"Connection":      {"close"},
		"Host":            {"inbound.example"},
		"Content-Length":  {"9999"},
		"X-Forwarded-For": {"1.2.3.4"},
		"X-Tenant-Id":     {"acme"},
	})

	if _, err := provider.ChatCompletion(ctx, &core.ChatRequest{
		Model:    "kimi-k2-0711-preview",
		Messages: []core.Message{{Role: "user", Content: "Hello"}},
	}); err != nil {
		t.Fatalf("ChatCompletion() error = %v", err)
	}

	got := captured()
	if v := got.Get("Authorization"); v != "Bearer kimi-test-key" {
		t.Errorf("Authorization = %q, want Bearer kimi-test-key (bearerSetHeaders must win)", v)
	}
	if v := got.Get("X-Api-Key"); v != "" {
		t.Errorf("X-Api-Key = %q, want empty (skipped passthrough)", v)
	}
	if v := got.Get("Cookie"); v != "" {
		t.Errorf("Cookie = %q, want empty (skipped passthrough)", v)
	}
	if v := got.Get("Connection"); v != "" {
		t.Errorf("Connection = %q, want empty (skipped passthrough)", v)
	}
	if v := got.Get("X-Forwarded-For"); v != "" {
		t.Errorf("X-Forwarded-For = %q, want empty (skipped X-Forwarded-* prefix)", v)
	}
	if v := got.Get("X-Tenant-Id"); v != "acme" {
		t.Errorf("X-Tenant-Id = %q, want acme (non-skipped headers must still flow)", v)
	}
}

// TestChatCompletion_PassthroughDisabledIgnoresInboundHeaders asserts that
// setting PassthroughUserHeaders: false on the resolved ProviderConfig blocks
// inbound header forwarding entirely, while bearerSetHeaders still installs
// Authorization on the outbound request. CustomUpstreamHeaders remain applicable when
// supplied alongside the explicit disable.
func TestChatCompletion_PassthroughDisabledIgnoresInboundHeaders(t *testing.T) {
	server, captured := kimiHeaderServer(t)

	provider := New(providers.ProviderConfig{
		APIKey:                 "kimi-test-key",
		BaseURL:                server.URL,
		PassthroughUserHeaders: false,
		CustomUpstreamHeaders: map[string]string{
			"X-Org": "custom-org",
		},
	}, providers.ProviderOptions{})

	ctx := kimiSnapshotContext(t, map[string][]string{
		"X-Tenant-Id": {"acme"},
		"X-Trace-Id":  {"trace-abc"},
	})

	if _, err := provider.ChatCompletion(ctx, &core.ChatRequest{
		Model:    "kimi-k2-0711-preview",
		Messages: []core.Message{{Role: "user", Content: "Hello"}},
	}); err != nil {
		t.Fatalf("ChatCompletion() error = %v", err)
	}

	got := captured()
	if v := got.Get("X-Tenant-Id"); v != "" {
		t.Errorf("X-Tenant-Id = %q, want empty (passthrough disabled)", v)
	}
	if v := got.Get("X-Trace-Id"); v != "" {
		t.Errorf("X-Trace-Id = %q, want empty (passthrough disabled)", v)
	}
	if v := got.Get("X-Org"); v != "custom-org" {
		t.Errorf("X-Org = %q, want custom-org (custom headers must still apply)", v)
	}
	if v := got.Get("Authorization"); v != "Bearer kimi-test-key" {
		t.Errorf("Authorization = %q, want Bearer kimi-test-key (bearerSetHeaders still installs auth)", v)
	}
}

// (mutex behavior covered by TestApplyRequestHeaderOverrides_BothPassthroughAndCustomPanics
// in internal/providers/request_header_overrides_test.go — the runtime mutex is the
// second line of defense, and the config-load validator in resolveProviders rejects
// the combination at startup)
