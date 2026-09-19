package xiaomi

import (
	"net/http"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
	"github.com/stretchr/testify/require"
)

func TestChatCompatibleContract(t *testing.T) {
	providertest.AssertChatCompatible(t, providertest.ChatCompatible{
		Registration:   Registration,
		Type:           "xiaomi",
		DefaultBaseURL: "https://api.xiaomimimo.com/v1",
		New: func(apiKey, baseURL string, client *http.Client, hooks llmclient.Hooks) core.Provider {
			opts := providertest.Options(hooks)
			opts.HTTPClient = client
			return New(providers.ProviderConfig{APIKey: apiKey, BaseURL: baseURL}, opts)
		},
		Embeddings: false,
	})
}

// Xiaomi serves audio through its chat endpoint (see audio.go), so only the
// batch and file surfaces must stay hidden.
func TestProvider_DoesNotExposeBatchOrFileInterfaces(t *testing.T) {
	provider := New(providers.ProviderConfig{APIKey: "mimo-key"}, providers.ProviderOptions{})
	_, ok := any(provider).(core.NativeBatchProvider)
	require.False(t, ok)
	_, ok = any(provider).(core.NativeFileProvider)
	require.False(t, ok)
}
