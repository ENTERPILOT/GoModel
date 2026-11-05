package azure

import (
	"net/http"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
)

// TestChatCompatibleContract runs the shared contract on a resource-root base
// URL, where the data-plane paths are the plain OpenAI ones and the model
// list comes from the resource's /openai/models.
func TestChatCompatibleContract(t *testing.T) {
	providertest.AssertChatCompatible(t, providertest.ChatCompatible{
		Registration: Registration,
		Type:         "azure",
		New: func(apiKey, baseURL string, client *http.Client, hooks llmclient.Hooks) core.Provider {
			opts := providertest.Options(hooks)
			opts.HTTPClient = client
			return New(providers.ProviderConfig{APIKey: apiKey, BaseURL: baseURL}, opts)
		},
		AuthHeader:      "api-key",
		ModelsPath:      "/openai/models",
		NativeResponses: true,
		Embeddings:      true,
		// The Azure OpenAI v1 API documents the allowed_tools tool_choice.
		ForwardsAllowedTools: true,
	})
}
