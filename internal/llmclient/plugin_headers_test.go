package llmclient

import (
	"context"
	"net/http"
	"net/http/httptest"
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
