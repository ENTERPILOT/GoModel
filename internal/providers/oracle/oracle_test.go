package oracle

import (
	"context"
	"net/http"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
	"github.com/stretchr/testify/assert"
)

// Oracle sits on the plain OpenAI-compatible adapter: it forwards Responses
// natively, requires a configured base URL, documents no embeddings endpoint,
// and must not advertise native batch, file, audio, or passthrough support.
func TestChatCompatibleContract(t *testing.T) {
	providertest.AssertChatCompatible(t, providertest.ChatCompatible{
		Registration:    Registration,
		Type:            "oracle",
		NativeResponses: true,
		New: func(apiKey, baseURL string, client *http.Client, hooks llmclient.Hooks) core.Provider {
			opts := providertest.Options(hooks)
			opts.HTTPClient = client
			return New(providers.ProviderConfig{APIKey: apiKey, BaseURL: baseURL}, opts)
		},
	})

	provider := newTestProvider("oracle-key", nil, llmclient.Hooks{})
	providertest.AssertNoNativeSurfaces(t, provider)
	_, ok := any(provider).(core.PassthroughProvider)
	assert.False(t, ok, "provider should not implement core.PassthroughProvider")

	_, err := provider.Embeddings(context.Background(), &core.EmbeddingRequest{Model: "text-embedding-3-small"})
	assert.ErrorContains(t, err, "oracle does not support embeddings")
}
