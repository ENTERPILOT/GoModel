package xai

import (
	"net/http"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
)

// xAI accepts an allowed_tools choice but ignores it, so the declared tools are narrowed.
func TestAllowedTools(t *testing.T) {
	providertest.AssertAllowedTools(t, false, func(baseURL string, client *http.Client) core.Provider {
		provider := newHTTPTestProvider("test-api-key", client, llmclient.Hooks{})
		provider.SetBaseURL(baseURL)
		return provider
	})
}
