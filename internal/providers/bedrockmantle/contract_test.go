package bedrockmantle

import (
	"net/http"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
)

// TestChatCompatibleContract runs the shared contract with a bearer token.
// Mantle mounts the OpenAI surface under /v1; the keyless mode signs with
// AWS credentials and is covered by TestSigV4AuthenticationUsesBedrockService.
func TestChatCompatibleContract(t *testing.T) {
	providertest.AssertChatCompatible(t, providertest.ChatCompatible{
		Registration: Registration,
		Type:         providerName,
		New: func(apiKey, baseURL string, client *http.Client, hooks llmclient.Hooks) core.Provider {
			opts := providertest.Options(hooks)
			opts.HTTPClient = client
			return New(providers.ProviderConfig{APIKey: apiKey, BaseURL: baseURL}, opts)
		},
		PathPrefix:      "/v1",
		SkipKeyless:     true,
		NativeResponses: true,
		Embeddings:      false,
	})
}
