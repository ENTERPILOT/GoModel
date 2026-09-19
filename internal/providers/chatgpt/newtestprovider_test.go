package chatgpt

import (
	"net/http"

	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
)

// newTestProvider builds the provider through its own constructor on a test transport.
func newTestProvider(apiKey, baseURL string, httpClient *http.Client, hooks llmclient.Hooks) *Provider {
	opts := providertest.Options(hooks)
	opts.HTTPClient = httpClient
	return New(providers.ProviderConfig{APIKey: apiKey, BaseURL: baseURL}, opts).(*Provider)
}
