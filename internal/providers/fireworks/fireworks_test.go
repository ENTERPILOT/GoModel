package fireworks

import (
	"net/http"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
)

// Fireworks is a thin wrapper over the shared chat-centric adapter and
// forwards embeddings upstream, so the shared contract covers its surface.
// It must not advertise native batch, file, or audio support.
func TestChatCompatibleContract(t *testing.T) {
	providertest.AssertChatCompatible(t, providertest.ChatCompatible{
		Registration:   Registration,
		Type:           "fireworks",
		DefaultBaseURL: "https://api.fireworks.ai/inference/v1",
		New: func(apiKey, baseURL string, client *http.Client, hooks llmclient.Hooks) core.Provider {
			return NewWithHTTPClient(apiKey, baseURL, client, hooks)
		},
		Embeddings: true,
	})
	providertest.AssertNoNativeSurfaces(t, NewWithHTTPClient("fw-key", "", nil, llmclient.Hooks{}))
}
