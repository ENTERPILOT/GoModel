package zai

import (
	"net/http"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
)

// Z.ai is a thin wrapper over the shared chat-centric adapter and forwards
// embeddings upstream, so the shared contract covers its surface. It must
// not advertise native batch, file, or audio support.
func TestChatCompatibleContract(t *testing.T) {
	providertest.AssertChatCompatible(t, providertest.ChatCompatible{
		Registration:   Registration,
		Type:           "zai",
		DefaultBaseURL: "https://api.z.ai/api/paas/v4",
		New: func(apiKey, baseURL string, client *http.Client, hooks llmclient.Hooks) core.Provider {
			return NewWithHTTPClient(apiKey, baseURL, client, hooks)
		},
		Embeddings: true,
	})
	providertest.AssertNoNativeSurfaces(t, NewWithHTTPClient("zai-key", "", nil, llmclient.Hooks{}))
}
