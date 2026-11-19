package run

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
)

// headerlessProviderTypes are the provider types that refuse `headers`: their
// requests are signed (a proxy that strips its header breaks the signature),
// or they are subscription clients whose vendor endpoint expects fixed
// headers. Keep it sorted.
var headerlessProviderTypes = []string{"bedrock", "bedrock-mantle", "chatgpt", "github-copilot", "kimicode", "opencode_go"}

// TestEveryProviderTypeSendsItsConfiguredHeaders builds each shipped provider
// type through the factory, as startup does, and calls it. Every request that
// reaches the provider's API must carry the operator's configured headers
// (providers.<name>.headers): an adapter that builds an HTTP client without
// them would silently fail behind the proxy the headers authenticate to.
func TestEveryProviderTypeSendsItsConfiguredHeaders(t *testing.T) {
	const header, token = "X-Edge-Auth", "edge-token"
	factory := defaultProviderFactory(&config.Config{})
	types := factory.RegisteredTypes()
	slices.Sort(types)

	// Vertex authenticates with Google credentials: point Application Default
	// Credentials at a token endpoint of the test's own.
	tokenServer, tokenCapture := providertest.JSONServer(t, http.StatusOK, `{"access_token":"google-token","token_type":"Bearer","expires_in":3600}`)
	adc := filepath.Join(t.TempDir(), "adc.json")
	require.NoError(t, os.WriteFile(adc, []byte(`{"type":"authorized_user","client_id":"id","client_secret":"secret","refresh_token":"refresh","token_uri":"`+tokenServer.URL+`"}`), 0o600))
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", adc)

	var headerless []string
	for _, providerType := range types {
		if err := factory.ValidateHeaders(providerType, map[string]string{header: token}); err != nil {
			headerless = append(headerless, providerType)
			continue
		}
		t.Run(providerType, func(t *testing.T) {
			upstream, capture := providertest.JSONServer(t, http.StatusOK, `{}`)
			resilience := providertest.Resilience()
			resilience.Retry.MaxRetries = 0
			resilience.CircuitBreaker.Enabled = false
			provider, err := factory.Create(providers.ProviderConfig{
				Name:           providerType,
				Type:           providerType,
				APIKey:         "test-key",
				APIKeys:        []string{"test-key"},
				BaseURL:        upstream.URL,
				APIVersion:     "2024-10-21",
				VertexProject:  "project",
				VertexLocation: "us-central1",
				Headers:        map[string]string{header: token},
				Resilience:     resilience,
			})
			require.NoError(t, err)

			// The calls only have to reach the upstream; what it answers,
			// and whether the adapter can decode that, does not matter here.
			ctx := context.Background()
			_, _ = provider.ListModels(ctx)
			_, _ = provider.ChatCompletion(ctx, &core.ChatRequest{Model: "model", Messages: []core.Message{{Role: "user", Content: "hi"}}})
			_, _ = provider.Embeddings(ctx, &core.EmbeddingRequest{Model: "model", Input: "hi"})
			if audio, ok := provider.(core.AudioProvider); ok {
				_, _ = audio.CreateSpeech(ctx, &core.AudioSpeechRequest{Model: "model", Input: "hi", Voice: "voice"})
			}
			if passthrough, ok := provider.(core.PassthroughProvider); ok {
				resp, err := passthrough.Passthrough(ctx, &core.PassthroughRequest{
					Method:   http.MethodPost,
					Endpoint: "/native",
					Body:     io.NopCloser(strings.NewReader(`{}`)),
					Headers:  http.Header{header: {"client-token"}},
				})
				if err == nil && resp != nil && resp.Body != nil {
					_ = resp.Body.Close()
				}
			}

			sent := capture.All()
			require.NotEmpty(t, sent, "no request reached the upstream; the test covers nothing for this type")
			for _, request := range sent {
				assert.Equal(t, []string{token}, request.Header.Values(header), "%s %s", request.Method, request.Path)
			}
		})
	}

	assert.Equal(t, headerlessProviderTypes, headerless, "provider types that refuse headers changed; update the list and the provider headers docs")
	for _, exchange := range tokenCapture.All() {
		assert.Empty(t, exchange.Header.Get(header), "a token endpoint must never receive a provider header")
	}
}
