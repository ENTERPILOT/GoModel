package audiocpp

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
	_ core.AudioProvider       = (*Provider)(nil)
	_ core.PassthroughProvider = (*Provider)(nil)
)

// audio.cpp requires a configured base URL (its default port collides with the
// gateway's own) and has no authentication of its own.
func TestRegistration_DescribesALocalKeylessServer(t *testing.T) {
	assert.Equal(t, "audiocpp", Registration.Type)
	require.NotNil(t, Registration.New)
	assert.True(t, Registration.Discovery.RequireBaseURL)
	assert.True(t, Registration.Discovery.AllowAPIKeyless)
	assert.Empty(t, Registration.Discovery.DefaultBaseURL)

	// Zero options are what a keyless provider is built with outside the
	// factory: no keyring, no resilience settings, and no test transport, so
	// the client falls back to the shared pooled one.
	provider := Registration.New(providers.ProviderConfig{BaseURL: "http://localhost:8099"}, providers.ProviderOptions{})
	assert.NotNil(t, provider)
}

// The inference surfaces audio.cpp does not implement must fail as typed
// invalid-request errors rather than 404s from an upstream that never had
// those routes.
func TestUnsupportedCapabilities_ReturnInvalidRequestErrors(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, `{}`)
	provider := newTestProvider("", server.URL, server.Client(), llmclient.Hooks{})

	_, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{Model: "pocket-tts"})
	providertest.AssertUnsupported(t, err)
	_, err = provider.StreamChatCompletion(context.Background(), &core.ChatRequest{Model: "pocket-tts"})
	providertest.AssertUnsupported(t, err)
	_, err = provider.Responses(context.Background(), &core.ResponsesRequest{Model: "pocket-tts"})
	providertest.AssertUnsupported(t, err)
	_, err = provider.StreamResponses(context.Background(), &core.ResponsesRequest{Model: "pocket-tts"})
	providertest.AssertUnsupported(t, err)
	_, err = provider.Embeddings(context.Background(), &core.EmbeddingRequest{Model: "pocket-tts"})
	providertest.AssertUnsupported(t, err)

	assert.Zero(t, capture.Count(), "unsupported surfaces must not reach the upstream")
}

// Both base URL spellings address the same server: audio.cpp serves /health at
// the root and everything else under its own /v1 prefix, so a configured /v1
// suffix is trimmed rather than doubled.
func TestBaseURL_AcceptsServerRootAndV1Suffix(t *testing.T) {
	for _, suffix := range []string{"", "/v1", "/v1/"} {
		t.Run("suffix "+suffix, func(t *testing.T) {
			server, capture := providertest.JSONServer(t, http.StatusOK, `{"object":"list","data":[]}`)
			provider := newTestProvider("", server.URL+suffix, server.Client(), llmclient.Hooks{})

			_, err := provider.ListModels(context.Background())
			require.NoError(t, err)
			assert.Equal(t, "/v1/models", capture.Last(t).Path)
		})
	}
}

func TestSetBaseURL_ChangesRequestTarget(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, `{"object":"list","data":[]}`)
	provider := newTestProvider("", "http://unused.invalid/v1", server.Client(), llmclient.Hooks{})
	provider.SetBaseURL(server.URL + "/v1")

	_, err := provider.ListModels(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "/v1/models", capture.Last(t).Path)
}

// Passthrough is how the audio.cpp routes with no OpenAI equivalent are
// reached, including the live transcription endpoint that carries audio
// instead of a file part.
func TestPassthrough_ForwardsNativeEndpointsVerbatim(t *testing.T) {
	tests := []struct {
		name      string
		endpoint  string
		wantPath  string
		wantQuery string
	}{
		{name: "health at the server root", endpoint: "health", wantPath: "/health"},
		{name: "transcription details", endpoint: "audio/transcriptions/details", wantPath: "/v1/audio/transcriptions/details"},
		{name: "live transcription", endpoint: "/audio/transcriptions/live?model=moonshine-tiny&sample_rate=16000", wantPath: "/v1/audio/transcriptions/live", wantQuery: "model=moonshine-tiny&sample_rate=16000"},
		{name: "alignments", endpoint: "audio/alignments", wantPath: "/v1/audio/alignments"},
		{name: "voices", endpoint: "audio/voices?model=pocket-tts", wantPath: "/v1/audio/voices", wantQuery: "model=pocket-tts"},
		{name: "generic task run", endpoint: "tasks/run", wantPath: "/v1/tasks/run"},
		{name: "explicit v1 prefix is addressed as given", endpoint: "v1/models", wantPath: "/v1/models"},
		{name: "model management", endpoint: "models/load", wantPath: "/v1/models/load"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, capture := providertest.JSONServer(t, http.StatusOK, `{"ok":true}`)
			provider := newTestProvider("proxy-token", server.URL, server.Client(), llmclient.Hooks{})

			resp, err := provider.Passthrough(context.Background(), &core.PassthroughRequest{
				Method:   http.MethodPost,
				Endpoint: tt.endpoint,
				Body:     io.NopCloser(strings.NewReader("{}")),
				Headers:  http.Header{"Content-Type": []string{"application/json"}},
			})
			require.NoError(t, err)
			defer resp.Body.Close()

			req := capture.Last(t)
			assert.Equal(t, tt.wantPath, req.Path)
			assert.Equal(t, tt.wantQuery, req.Query.Encode())
			assert.Equal(t, "Bearer proxy-token", req.Header.Get("Authorization"))
		})
	}
}

// A keyless server is the normal deployment: audio.cpp has no credential of
// its own, so nothing must be sent unless the operator configured one for a
// proxy in front of it.
func TestPassthrough_SendsNoCredentialWhenNoneIsConfigured(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, `{"status":"ok"}`)
	provider := New(providers.ProviderConfig{BaseURL: server.URL}, providertest.Options(llmclient.Hooks{})).(*Provider)

	resp, err := provider.Passthrough(context.Background(), &core.PassthroughRequest{
		Method:   http.MethodGet,
		Endpoint: "health",
		Headers:  http.Header{},
	})
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Empty(t, capture.Last(t).Header.Get("Authorization"))
}

// Provider-native errors relay status and body verbatim instead of being
// converted into gateway errors.
func TestPassthrough_RelaysNativeErrors(t *testing.T) {
	server, _ := providertest.Server(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":{"message":"model busy","type":"server_busy"}}`))
	})
	provider := newTestProvider("", server.URL, server.Client(), llmclient.Hooks{})

	resp, err := provider.Passthrough(context.Background(), &core.PassthroughRequest{
		Method:   http.MethodPost,
		Endpoint: "v1/tasks/run",
		Headers:  http.Header{},
	})
	require.NoError(t, err)
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
	assert.Contains(t, string(body), "model busy")
}

func TestPassthrough_RequiresRequest(t *testing.T) {
	provider := New(providers.ProviderConfig{BaseURL: "http://localhost:8099"}, providers.ProviderOptions{}).(*Provider)
	_, err := provider.Passthrough(context.Background(), nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "passthrough request is required")
}

// The enricher names the native routes so the audit log records what a
// passthrough call actually did rather than an opaque /p/audiocpp/... path.
func TestPassthroughSemantics_NameNativeRoutes(t *testing.T) {
	enriched := passthroughSemanticEnricher.Enrich(nil, nil, &core.PassthroughRouteInfo{
		RawEndpoint: "/audio/transcriptions/live?model=moonshine-tiny",
	})
	require.NotNil(t, enriched)
	assert.Equal(t, "audiocpp.audio_transcriptions_live", enriched.SemanticOperation)
	assert.Equal(t, string(core.OperationAudioTranscriptions), enriched.GenAIOperation)
	assert.Equal(t, "/v1/audio/transcriptions/live", enriched.AuditPath)

	unknown := passthroughSemanticEnricher.Enrich(nil, nil, &core.PassthroughRouteInfo{RawEndpoint: "/health"})
	require.NotNil(t, unknown)
	assert.Equal(t, "/p/audiocpp/health", unknown.AuditPath)
}
