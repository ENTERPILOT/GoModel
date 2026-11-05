package providers

import (
	"testing"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
)

// The admin inventory embeds the shared model type, so a max_model_len a
// provider reported must not reach it: /admin/models has to read the same with
// models.vllm_discovery_compat on or off. Only GET /v1/models advertises the
// field, from a response type it owns.
func TestModelWithProvider_DoesNotSerializeAReportedLimit(t *testing.T) {
	limit := 4096

	body, err := json.Marshal(ModelWithProvider{
		Model:        core.Model{ID: "local/custom", Object: "model", MaxModelLen: &limit},
		ProviderType: "openai",
		ProviderName: "custom",
		Selector:     "custom/local/custom",
	})
	require.NoError(t, err)

	assert.NotContains(t, string(body), "max_model_len")
	assert.Contains(t, string(body), `"id":"local/custom"`)
}
