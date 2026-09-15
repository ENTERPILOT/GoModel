package hetzner

import (
	"net/http"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
)

// Hetzner is a thin wrapper over the shared chat-centric adapter, so the
// shared contract covers its surface. Hetzner documents no embeddings
// endpoint, so Embeddings must fail fast without an upstream call, and the
// provider must not advertise native batch, file, or audio support.
func TestChatCompatibleContract(t *testing.T) {
	providertest.AssertChatCompatible(t, providertest.ChatCompatible{
		Registration:   Registration,
		Type:           "hetzner",
		DefaultBaseURL: "https://inference.hetzner.com/api/v1",
		New: func(apiKey, baseURL string, client *http.Client, hooks llmclient.Hooks) core.Provider {
			return NewWithHTTPClient(apiKey, baseURL, client, hooks)
		},
	})
	providertest.AssertNoNativeSurfaces(t, NewWithHTTPClient("hetzner-key", "", nil, llmclient.Hooks{}))
}
