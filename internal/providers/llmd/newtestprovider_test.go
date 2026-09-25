package llmd

import (
	"net/http"

	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
)

// newTestProvider builds the provider through its registered constructor with
// a test transport. The control settings travel on ProviderConfig, which is
// where New reads them from in production.
func newTestProvider(apiKey, baseURL string, controls ControlConfig, httpClient *http.Client, hooks llmclient.Hooks) *Provider {
	opts := providertest.Options(hooks)
	opts.HTTPClient = httpClient
	return New(providers.ProviderConfig{
		APIKey:               apiKey,
		BaseURL:              baseURL,
		InferenceObjective:   controls.InferenceObjective,
		FairnessFromUserPath: controls.FairnessFromUserPath,
	}, opts).(*Provider)
}
