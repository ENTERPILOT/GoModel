package llmclient

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
)

func providerHeadersConfig(baseURL string, headers map[string]string) Config {
	cfg := DefaultConfig("edge", baseURL)
	cfg.Headers = headers
	return cfg
}

// TestBuildRequestAppliesProviderHeadersLast pins the precedence of
// providers.<name>.headers: they replace a value of the same name whoever set
// it — the provider's header hook, a passthrough client, or a plugin.
func TestBuildRequestAppliesProviderHeadersLast(t *testing.T) {
	client := New(providerHeadersConfig("http://upstream.test", map[string]string{
		"X-Edge-Auth": "configured-token",
		"x-tenant":    "configured-tenant",
		"User-Agent":  "configured-agent",
	}), func(req *http.Request) {
		req.Header.Set("Authorization", "Bearer provider")
		req.Header.Set("User-Agent", "provider-agent")
	})
	ctx := core.WithPluginUpstreamHeaders(context.Background(), http.Header{
		"X-Tenant":  {"plugin-tenant"},
		"X-Feature": {"plugin-feature"},
	})

	req, err := client.buildRequest(ctx, Request{
		Method:   http.MethodPost,
		Endpoint: "/v1/chat/completions",
		Body:     map[string]string{"model": "m"},
		// As a passthrough client's headers arrive, in the client's spelling.
		Headers: http.Header{
			"x-edge-auth": {"client-token", "client-token-2"},
			"X-Tenant":    {"client-tenant"},
			"X-Client":    {"kept"},
		},
	})
	require.NoError(t, err)

	assert.Equal(t, []string{"configured-token"}, req.Header.Values("X-Edge-Auth"), "a passthrough client cannot override a configured header")
	assert.Equal(t, []string{"configured-tenant"}, req.Header.Values("X-Tenant"), "a plugin cannot override a configured header")
	assert.Equal(t, []string{"configured-agent"}, req.Header.Values("User-Agent"), "a configured header replaces the provider hook's")
	for name := range req.Header {
		assert.Equal(t, http.CanonicalHeaderKey(name), name, "no second spelling of a header may reach the wire")
	}

	assert.Equal(t, "Bearer provider", req.Header.Get("Authorization"), "the provider credential is untouched")
	assert.Equal(t, "application/json", req.Header.Get("Content-Type"))
	assert.Equal(t, "plugin-feature", req.Header.Get("X-Feature"), "other plugin headers still apply")
	assert.Equal(t, "kept", req.Header.Get("X-Client"), "other request headers still apply")
}

// TestBuildRequestProviderHeadersOnEveryOrigin proves the headers describe the
// provider, not the request: inference the gateway issues for itself (a
// guardrail's LLM judge) reaches the same proxy and needs the same token.
func TestBuildRequestProviderHeadersOnEveryOrigin(t *testing.T) {
	client := New(providerHeadersConfig("http://upstream.test", map[string]string{"X-Edge-Auth": "configured-token"}), nil)

	for _, origin := range []core.RequestOrigin{"", core.RequestOriginExternal, core.RequestOriginPlugin, core.RequestOriginGuardrail} {
		t.Run("origin "+string(origin), func(t *testing.T) {
			ctx := context.Background()
			if origin != "" {
				ctx = core.WithRequestOrigin(ctx, origin)
			}
			req, err := client.buildRequest(ctx, Request{Method: http.MethodGet, Endpoint: "/v1/models"})
			require.NoError(t, err)
			assert.Equal(t, "configured-token", req.Header.Get("X-Edge-Auth"))
		})
	}
}

func TestBuildRequestWithoutProviderHeadersIsUnchanged(t *testing.T) {
	client := New(DefaultConfig("test", "http://upstream.test"), nil)
	req, err := client.buildRequest(context.Background(), Request{Method: http.MethodGet, Endpoint: "/v1/models"})
	require.NoError(t, err)
	assert.Empty(t, req.Header)
}

// TestProviderHeadersReachEveryRequestPath sends one request through each way
// a provider calls its API and checks the wire, not the built request.
func TestProviderHeadersReachEveryRequestPath(t *testing.T) {
	const token = "configured-token-do-not-log"
	received := make(chan http.Header, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Hand the headers over through a channel: the handler runs in
		// another goroutine, and a shared variable would be a data race.
		received <- r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	var logs bytes.Buffer
	original := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(original) })

	client := New(providerHeadersConfig(server.URL, map[string]string{"X-Edge-Auth": token}), func(req *http.Request) {
		req.Header.Set("Authorization", "Bearer provider")
	})
	ctx := context.Background()
	get := Request{Method: http.MethodGet, Endpoint: "/v1/models"}
	post := Request{Method: http.MethodPost, Endpoint: "/v1/chat/completions", Body: map[string]string{"model": "m"}}

	paths := map[string]func() error{
		"Do": func() error {
			var result map[string]any
			return client.Do(ctx, get, &result)
		},
		"DoRaw": func() error {
			_, err := client.DoRaw(ctx, post)
			return err
		},
		"DoStream": func() error {
			body, err := client.DoStream(ctx, post)
			if err != nil {
				return err
			}
			_, _ = io.Copy(io.Discard, body)
			return body.Close()
		},
		"DoPassthrough": func() error {
			resp, err := client.DoPassthrough(ctx, Request{
				Method:   http.MethodPost,
				Endpoint: "/v1/anything",
				RawBody:  []byte(`{}`),
				Headers:  http.Header{"X-Edge-Auth": {"client-token"}},
			})
			if err != nil {
				return err
			}
			return resp.Body.Close()
		},
	}
	for name, send := range paths {
		t.Run(name, func(t *testing.T) {
			require.NoError(t, send())
			got := <-received
			assert.Equal(t, []string{token}, got.Values("X-Edge-Auth"))
			assert.Equal(t, "Bearer provider", got.Get("Authorization"))
		})
	}
	assert.NotContains(t, logs.String(), token, "a configured header value is never logged")
}

// TestProviderHeaderValuesStayOutOfTransportErrors covers a failed call: the
// error returned to the client and the warning logged for the operator name
// the provider, never a configured header value.
func TestProviderHeaderValuesStayOutOfTransportErrors(t *testing.T) {
	const token = "configured-token-do-not-log"
	server := httptest.NewServer(http.NotFoundHandler())
	server.Close() // refuse the connection

	var logs bytes.Buffer
	original := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(original) })

	cfg := providerHeadersConfig(server.URL, map[string]string{"X-Edge-Auth": token})
	cfg.Retry.MaxRetries = 0
	client := New(cfg, nil)

	_, err := client.DoRaw(context.Background(), Request{Method: http.MethodGet, Endpoint: "/v1/models"})
	require.Error(t, err)
	assert.NotContains(t, err.Error(), token)
	assert.NotContains(t, logs.String(), token)
	assert.Contains(t, logs.String(), "provider request failed")
}
