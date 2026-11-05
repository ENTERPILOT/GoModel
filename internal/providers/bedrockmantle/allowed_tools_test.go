package bedrockmantle

import (
	"net/http"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
)

// Bedrock Mantle does not document allowed_tools, so the declared tools are
// narrowed.
func TestAllowedTools(t *testing.T) {
	providertest.AssertAllowedTools(t, false, func(baseURL string, client *http.Client) core.Provider {
		return New(providers.ProviderConfig{APIKey: "secret", BaseURL: baseURL, APIMode: modeOpenAI},
			providers.ProviderOptions{HTTPClient: client})
	})
}
