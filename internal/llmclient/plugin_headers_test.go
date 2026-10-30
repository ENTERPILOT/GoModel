package llmclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
)

func TestBuildRequestAppliesPluginUpstreamHeaders(t *testing.T) {
	client := New(DefaultConfig("test", "http://upstream.test"), nil)
	ctx := core.WithPluginUpstreamHeaders(context.Background(), http.Header{
		"X-Edge-Proxy-Token": {"edge-token"},
	})

	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodDelete} {
		t.Run(method, func(t *testing.T) {
			req, err := client.buildRequest(ctx, Request{Method: method, Endpoint: "/v1/models"})
			require.NoError(t, err)
			assert.Equal(t, "edge-token", req.Header.Get("X-Edge-Proxy-Token"))
		})
	}
}

// TestBuildRequestPluginOriginDropsPluginUpstreamHeaders proves that inference a
// plugin issues through its own Host — a guardrail's LLM judge, for example —
// does not inherit the headers the prompt phase recorded. Those headers
// describe the client's provider call, not the model the gateway picks for
// itself, and the internal call shares the context that carries them.
func TestBuildRequestPluginOriginDropsPluginUpstreamHeaders(t *testing.T) {
	client := New(DefaultConfig("test", "http://upstream.test"), func(req *http.Request) {
		req.Header.Set("Authorization", "Bearer provider")
	})

	tests := []struct {
		name       string
		origin     core.RequestOrigin
		wantPlugin bool
	}{
		{name: "external", origin: core.RequestOriginExternal, wantPlugin: true},
		{name: "plugin", origin: core.RequestOriginPlugin},
		{name: "guardrail", origin: core.RequestOriginGuardrail},
		{name: "unset", wantPlugin: true}, // external traffic is the default
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			if tt.origin != "" {
				ctx = core.WithRequestOrigin(ctx, tt.origin)
			}
			ctx = core.WithPluginUpstreamHeaders(ctx, http.Header{
				"X-Edge-Proxy-Token": {"edge-token"},
				"X-Judge-Trace":      {"plugin"},
			})

			req, err := client.buildRequest(ctx, Request{
				Method:   http.MethodGet,
				Endpoint: "/v1/models",
				Headers:  http.Header{"X-Request-Scoped": {"scoped"}},
			})
			require.NoError(t, err)

			if tt.wantPlugin {
				assert.Equal(t, "edge-token", req.Header.Get("X-Edge-Proxy-Token"))
				assert.Equal(t, "plugin", req.Header.Get("X-Judge-Trace"))
			} else {
				assert.Empty(t, req.Header.Get("X-Edge-Proxy-Token"))
				assert.Empty(t, req.Header.Get("X-Judge-Trace"), "a plugin's own inference carries no plugin upstream header")
			}
			assert.Equal(t, "Bearer provider", req.Header.Get("Authorization"))
			assert.Equal(t, "scoped", req.Header.Get("X-Request-Scoped"), "request headers are left alone")
		})
	}
}

// TestBuildRequestDeniesPluginHeaders pins one case per denied header: transport
// and body-framing headers, provider-negotiated headers, and the credential list.
// The length checks tie the table to the two lists that live in this package, so
// a header added to one of them without a case here fails.
func TestBuildRequestDeniesPluginHeaders(t *testing.T) {
	transport := []string{
		"Host",
		"Content-Length",
		"Transfer-Encoding",
		"Connection",
		"Te",
		"Trailer",
		"Upgrade",
		"Expect",
		"Accept-Encoding",
		"Content-Type",
	}
	reserved := []string{
		"Anthropic-Beta",
	}
	credentials := []string{
		"Authorization",
		"Proxy-Authorization",
		"Cookie",
		"Set-Cookie",
		"X-Api-Key",
		"Api-Key",
		"X-Goog-Api-Key",
		"X-Anthropic-Api-Key",
		"X-Auth-Token",
		"X-Access-Token",
		"X-GoModel-Key",
		"Xi-Api-Key",
	}
	require.Len(t, pluginTransportHeaders, len(transport), "transport list and cases drifted apart")
	require.Len(t, pluginReservedProviderHeaders, len(reserved), "reserved list and cases drifted apart")

	groups := []struct {
		name    string
		headers []string
	}{
		{name: "transport", headers: transport},
		{name: "reserved", headers: reserved},
		{name: "credential", headers: credentials},
	}
	for _, group := range groups {
		for _, name := range group.headers {
			t.Run(group.name+"/"+name, func(t *testing.T) {
				// A setter that claims the header itself proves the plugin value
				// loses to the provider's, not merely that it was never written.
				client := New(DefaultConfig("test", "http://upstream.test"), func(req *http.Request) {
					req.Header.Set(name, "provider-value")
				})
				// The plugin spells the name in lower case on purpose: the denial
				// must survive the canonicalization the merge applies.
				ctx := core.WithPluginUpstreamHeaders(context.Background(), http.Header{strings.ToLower(name): {"plugin-value"}})

				req, err := client.buildRequest(ctx, Request{Method: http.MethodPost, Endpoint: "/v1/chat/completions", Body: []byte(`{}`)})
				require.NoError(t, err)
				assert.NotContains(t, req.Header.Values(name), "plugin-value", "a plugin may not set %s", name)
				assert.Equal(t, []string{"provider-value"}, req.Header.Values(name), "the provider's %s survives", name)
			})
		}
	}
}

// TestBuildRequestKeepsProviderOAuthBetaFromPlugins pins the finding the review
// raised: a plugin that replaced anthropic-beta would drop the oauth-2025-04-20
// flag the Anthropic provider adds in its own header hook, and subscription
// credentials need that flag. The merge refuses the header, so the provider's
// value is what reaches the wire.
func TestBuildRequestKeepsProviderOAuthBetaFromPlugins(t *testing.T) {
	client := New(DefaultConfig("test", "http://upstream.test"), func(req *http.Request) {
		req.Header.Set("Anthropic-Beta", "oauth-2025-04-20")
	})
	ctx := core.WithPluginUpstreamHeaders(context.Background(), http.Header{
		"anthropic-beta": {"prompt-caching-2024-07-31"},
	})

	req, err := client.buildRequest(ctx, Request{Method: http.MethodPost, Endpoint: "/v1/messages", Body: []byte(`{}`)})
	require.NoError(t, err)
	assert.Equal(t, []string{"oauth-2025-04-20"}, req.Header.Values("Anthropic-Beta"))
}

func TestBuildRequestPluginHeadersCannotOverrideCredentials(t *testing.T) {
	client := New(DefaultConfig("test", "http://upstream.test"), func(req *http.Request) {
		req.Header.Set("Authorization", "Bearer provider")
		req.Header.Set("X-Goog-Api-Key", "provider-key")
		req.Header.Set("api-key", "azure-provider-key")
	})
	ctx := core.WithPluginUpstreamHeaders(context.Background(), http.Header{
		"Authorization":       {"Bearer plugin"},
		"Proxy-Authorization": {"Basic plugin"},
		"X-Goog-Api-Key":      {"plugin-key"},
		"X-Api-Key":           {"plugin-key"},
		"Api-Key":             {"plugin-key"},
		"X-Anthropic-Api-Key": {"plugin-key"},
		"Cookie":              {"session=plugin"},
		"Set-Cookie":          {"session=plugin"},
		"Host":                {"evil.test"},
		"Content-Length":      {"999"},
		"Transfer-Encoding":   {"chunked"},
		"X-Allowed-Header":    {"allowed"},
	})

	req, err := client.buildRequest(ctx, Request{Method: http.MethodGet, Endpoint: "/v1/models"})
	require.NoError(t, err)

	assert.Equal(t, "Bearer provider", req.Header.Get("Authorization"))
	assert.Equal(t, "provider-key", req.Header.Get("X-Goog-Api-Key"))
	assert.Equal(t, "azure-provider-key", req.Header.Get("Api-Key"))
	assert.Empty(t, req.Header.Get("Proxy-Authorization"))
	assert.Empty(t, req.Header.Get("X-Api-Key"))
	assert.Empty(t, req.Header.Get("X-Anthropic-Api-Key"))
	assert.Empty(t, req.Header.Get("Cookie"))
	assert.Empty(t, req.Header.Get("Set-Cookie"))
	assert.Equal(t, "upstream.test", req.Host)
	assert.Empty(t, req.Header.Get("Host"), "a plugin Host is denied, not merely shadowed by the URL host")
	assert.Empty(t, req.Header.Get("Content-Length"))
	assert.Empty(t, req.Header.Get("Transfer-Encoding"))
	assert.Equal(t, "allowed", req.Header.Get("X-Allowed-Header"))
}

func TestBuildRequestPluginHeadersOverrideRequestHeaders(t *testing.T) {
	client := New(DefaultConfig("test", "http://upstream.test"), nil)
	ctx := core.WithPluginUpstreamHeaders(context.Background(), http.Header{
		"X-Edge-Proxy-Token": {"from-plugin"},
	})

	req, err := client.buildRequest(ctx, Request{
		Method:   http.MethodGet,
		Endpoint: "/v1/models",
		Headers:  http.Header{"X-Edge-Proxy-Token": {"from-request"}},
	})
	require.NoError(t, err)
	assert.Equal(t, "from-plugin", req.Header.Get("X-Edge-Proxy-Token"))
}

func TestBuildRequestWithoutPluginHeadersIsUnchanged(t *testing.T) {
	client := New(DefaultConfig("test", "http://upstream.test"), nil)
	req, err := client.buildRequest(context.Background(), Request{
		Method:   http.MethodGet,
		Endpoint: "/v1/models",
		Headers:  http.Header{"X-Test": {"value"}},
	})
	require.NoError(t, err)
	assert.Equal(t, "value", req.Header.Get("X-Test"))
}

func TestDoAppliesPluginUpstreamHeaders(t *testing.T) {
	received := make(chan http.Header, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Hand the headers over through a channel: the handler runs in
		// another goroutine, and a shared variable would be a data race.
		received <- r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	client := New(DefaultConfig("test", server.URL), func(req *http.Request) {
		req.Header.Set("Authorization", "Bearer provider")
	})
	ctx := core.WithPluginUpstreamHeaders(context.Background(), http.Header{
		"X-Edge-Proxy-Token": {"edge-token"},
		"Authorization":      {"Bearer plugin"},
	})

	var result map[string]any
	err := client.Do(ctx, Request{Method: http.MethodGet, Endpoint: "/v1/models"}, &result)
	require.NoError(t, err)
	got := <-received
	assert.Equal(t, "edge-token", got.Get("X-Edge-Proxy-Token"))
	assert.Equal(t, "Bearer provider", got.Get("Authorization"))
}
