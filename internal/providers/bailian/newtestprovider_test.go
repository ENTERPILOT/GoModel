package bailian

import (
	"net/http"

	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
)

// newHTTPTestProvider builds the provider through its own constructor on a test transport.
func newHTTPTestProvider(apiKey string, httpClient *http.Client, hooks llmclient.Hooks) *Provider {
	opts := providertest.Options(hooks)
	opts.HTTPClient = httpClient
	return New(providers.ProviderConfig{APIKey: apiKey}, opts).(*Provider)
}
