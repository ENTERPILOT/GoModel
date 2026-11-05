package azure

import (
	"net/http"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
)

// The Azure OpenAI v1 API documents allowed_tools, so the choice is forwarded.
func TestAllowedTools(t *testing.T) {
	providertest.AssertAllowedTools(t, true, func(baseURL string, client *http.Client) core.Provider {
		provider := newHTTPTestProvider("test-api-key", client, llmclient.Hooks{})
		provider.SetBaseURL(baseURL)
		return provider
	})
}
