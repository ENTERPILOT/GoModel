package hetzner

import (
	"net/http"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
)

// Hetzner is a thin wrapper over the shared chat-centric adapter, so the
// shared contract covers its surface. Hetzner documents no embeddings
// endpoint, so Embeddings must fail fast without an upstream call, and the
// provider must not advertise native batch, file, or audio support.
func TestChatCompatibleContract(t *testing.T) {
	providertest.AssertChatCompatible(t, providertest.ChatCompatible{
		Registration:   Registration,
		Type:           "hetzner",
		DefaultBaseURL: "https://inference.hetzner.com/api/v1",
		New: func(apiKey, baseURL string, client *http.Client, hooks llmclient.Hooks) core.Provider {
			opts := providertest.Options(hooks)
			opts.HTTPClient = client
			return New(providers.ProviderConfig{APIKey: apiKey, BaseURL: baseURL}, opts)
		},
	})
	providertest.AssertNoNativeSurfaces(t, New(providers.ProviderConfig{APIKey: "hetzner-key"}, providers.ProviderOptions{}))
}
