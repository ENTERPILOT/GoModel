package jev

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
)

var (
	_ core.Provider            = (*Provider)(nil)
	_ core.PassthroughProvider = (*Provider)(nil)
)

// The hosted API has a default origin and needs a key; a local Kev server has
// no authentication, so a base URL alone must configure the provider.
func TestRegistration_DescribesHostedAndKeylessServers(t *testing.T) {
	assert.Equal(t, "jev", Registration.Type)
	require.NotNil(t, Registration.New)
	assert.Equal(t, "https://api.typesafe.ai", Registration.Discovery.DefaultBaseURL)
	assert.True(t, Registration.Discovery.AllowAPIKeyless)
	assert.False(t, Registration.Discovery.RequireBaseURL)

	// Zero options are what a keyless provider is built with outside the
	// factory: no keyring, no resilience settings, and no test transport.
	provider := Registration.New(providers.ProviderConfig{BaseURL: "http://localhost:8009"}, providers.ProviderOptions{})
	assert.NotNil(t, provider)
}

// The inference surfaces System One does not implement must fail as typed
// invalid-request errors that point at the native route, rather than 404s
// from an upstream that never had those endpoints.
func TestUnsupportedCapabilities_ReturnInvalidRequestErrors(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, `{}`)
	provider := newTestProvider("", server.URL, server.Client(), llmclient.Hooks{})

	_, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{Model: "jev-latest"})
	providertest.AssertUnsupported(t, err)
	assert.Contains(t, err.Error(), "/v1/systemone")
	_, err = provider.StreamChatCompletion(context.Background(), &core.ChatRequest{Model: "jev-latest"})
	providertest.AssertUnsupported(t, err)
	_, err = provider.Responses(context.Background(), &core.ResponsesRequest{Model: "jev-latest"})
	providertest.AssertUnsupported(t, err)
	_, err = provider.StreamResponses(context.Background(), &core.ResponsesRequest{Model: "jev-latest"})
	providertest.AssertUnsupported(t, err)
	_, err = provider.Embeddings(context.Background(), &core.EmbeddingRequest{Model: "jev-latest"})
	providertest.AssertUnsupported(t, err)

	assert.Zero(t, capture.Count(), "unsupported surfaces must not reach the upstream")
}

// TypeSafe's SDKs are configured with the origin and add /v1 per request;
// every other provider here is configured with the /v1 suffix. Both address
// the same server, so a configured suffix is trimmed rather than doubled.
func TestBaseURL_AcceptsOriginAndV1Suffix(t *testing.T) {
	for _, suffix := range []string{"", "/", "/v1", "/v1/"} {
		t.Run("suffix "+suffix, func(t *testing.T) {
			server, capture := providertest.JSONServer(t, http.StatusOK, `{"models":[]}`)
			provider := newTestProvider("", server.URL+suffix, server.Client(), llmclient.Hooks{})

			_, err := provider.ListModels(context.Background())
			require.NoError(t, err)
			assert.Equal(t, "/v1/models", capture.Last(t).Path)
		})
	}
}

func TestSetBaseURL_ChangesRequestTarget(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, `{"models":[]}`)
	provider := newTestProvider("", "http://unused.invalid/v1", server.Client(), llmclient.Hooks{})
	provider.SetBaseURL(server.URL + "/v1")

	_, err := provider.ListModels(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "/v1/models", capture.Last(t).Path)
}

// Passthrough is how the evaluation endpoint is reached. The gateway strips
// the optional v1 alias before the provider sees the endpoint, so both
// spellings a client may use land on the same upstream route.
func TestPassthrough_ForwardsNativeEndpoints(t *testing.T) {
	tests := []struct {
		name      string
		endpoint  string
		wantPath  string
		wantQuery string
	}{
		{name: "evaluation", endpoint: "systemone", wantPath: "/v1/systemone"},
		{name: "evaluation with explicit v1 prefix", endpoint: "v1/systemone", wantPath: "/v1/systemone"},
		{name: "leading slash", endpoint: "/systemone", wantPath: "/v1/systemone"},
		{name: "kev permute", endpoint: "systemone/permute", wantPath: "/v1/systemone/permute"},
		{name: "kev separate", endpoint: "systemone/separate", wantPath: "/v1/systemone/separate"},
		{name: "models with query", endpoint: "models?limit=5", wantPath: "/v1/models", wantQuery: "limit=5"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, capture := providertest.JSONServer(t, http.StatusOK, `{"model":"jev-1.13.0","answers":{}}`)
			provider := newTestProvider("ts-key", server.URL, server.Client(), llmclient.Hooks{})

			resp, err := provider.Passthrough(context.Background(), &core.PassthroughRequest{
				Method:   http.MethodPost,
				Endpoint: tt.endpoint,
				Body:     io.NopCloser(strings.NewReader(`{"state":"hi","model":"jev-latest","questions":{}}`)),
				Headers:  http.Header{"Content-Type": []string{"application/json"}},
			})
			require.NoError(t, err)
			defer resp.Body.Close()

			req := capture.Last(t)
			assert.Equal(t, http.MethodPost, req.Method)
			assert.Equal(t, tt.wantPath, req.Path)
			assert.Equal(t, tt.wantQuery, req.Query.Encode())
			assert.Equal(t, "Bearer ts-key", req.Header.Get("Authorization"))
			assert.Equal(t, "application/json", req.Header.Get("Content-Type"))
			assert.JSONEq(t, `{"state":"hi","model":"jev-latest","questions":{}}`, string(req.Body))
			assert.Equal(t, http.StatusOK, resp.StatusCode)
		})
	}
}

// A local Kev server is the keyless deployment: nothing must be sent unless
// the operator configured a token for a proxy in front of it.
func TestPassthrough_SendsNoCredentialWhenNoneIsConfigured(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, `{"models":[]}`)
	provider := New(providers.ProviderConfig{BaseURL: server.URL}, providertest.Options(llmclient.Hooks{})).(*Provider)

	resp, err := provider.Passthrough(context.Background(), &core.PassthroughRequest{
		Method:   http.MethodGet,
		Endpoint: "models",
		Headers:  http.Header{},
	})
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Empty(t, capture.Last(t).Header.Get("Authorization"))
}

// Provider-native errors relay status and body verbatim: System One reports
// a malformed question as 422 with the offending field in the body, which the
// client needs as written.
func TestPassthrough_RelaysNativeErrors(t *testing.T) {
	server, _ := providertest.Server(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"detail":[{"loc":["body","questions","tone","criteria"],"msg":"field required"}]}`))
	})
	provider := newTestProvider("ts-key", server.URL, server.Client(), llmclient.Hooks{})

	resp, err := provider.Passthrough(context.Background(), &core.PassthroughRequest{
		Method:   http.MethodPost,
		Endpoint: "systemone",
		Headers:  http.Header{},
	})
	require.NoError(t, err)
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	assert.Equal(t, http.StatusUnprocessableEntity, resp.StatusCode)
	assert.Contains(t, string(body), "field required")
}

func TestPassthrough_RequiresRequest(t *testing.T) {
	provider := New(providers.ProviderConfig{BaseURL: "http://localhost:8009"}, providers.ProviderOptions{}).(*Provider)
	_, err := provider.Passthrough(context.Background(), nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "passthrough request is required")
}

// The enricher names the evaluation route so the audit log records what a
// passthrough call did rather than an opaque /p/jev/... path.
func TestPassthroughSemantics_NameNativeRoutes(t *testing.T) {
	enriched := passthroughSemanticEnricher.Enrich(nil, nil, &core.PassthroughRouteInfo{RawEndpoint: "/systemone"})
	require.NotNil(t, enriched)
	assert.Equal(t, "jev.systemone", enriched.SemanticOperation)
	assert.Empty(t, enriched.GenAIOperation)
	assert.Equal(t, "/v1/systemone", enriched.AuditPath)

	permute := passthroughSemanticEnricher.Enrich(nil, nil, &core.PassthroughRouteInfo{RawEndpoint: "systemone/permute"})
	require.NotNil(t, permute)
	assert.Equal(t, "jev.systemone_permute", permute.SemanticOperation)
	assert.Equal(t, "/v1/systemone/permute", permute.AuditPath)

	unknown := passthroughSemanticEnricher.Enrich(nil, nil, &core.PassthroughRouteInfo{RawEndpoint: "/models"})
	require.NotNil(t, unknown)
	assert.Equal(t, "/p/jev/models", unknown.AuditPath)
}
