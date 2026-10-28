package runcrate

import (
	"net/http"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
)

// Runcrate is the shared chat-centric adapter with its own model listing, so
// the shared contract covers chat, embeddings, Responses via chat, and
// passthrough; models_test.go covers the listing.
func TestChatCompatibleContract(t *testing.T) {
	providertest.AssertChatCompatible(t, providertest.ChatCompatible{
		Registration:   Registration,
		Type:           "runcrate",
		DefaultBaseURL: "https://api.runcrate.ai/v1",
		New: func(apiKey, baseURL string, client *http.Client, hooks llmclient.Hooks) core.Provider {
			return newTestProvider(apiKey, baseURL, client, hooks)
		},
		Embeddings: true,
	})
	providertest.AssertNoNativeSurfaces(t, New(providers.ProviderConfig{APIKey: "runcrate-key"}, providers.ProviderOptions{}))
}

// newTestProvider builds the provider through its own constructor on a test transport.
func newTestProvider(apiKey, baseURL string, httpClient *http.Client, hooks llmclient.Hooks) *Provider {
	opts := providertest.Options(hooks)
	opts.HTTPClient = httpClient
	return New(providers.ProviderConfig{APIKey: apiKey, BaseURL: baseURL}, opts).(*Provider)
}
