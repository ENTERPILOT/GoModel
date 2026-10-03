package modeldata

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProviderModelIDsAndDefaultBaseURL(t *testing.T) {
	list, err := Parse([]byte(`{"version":1,"updated_at":"2026-10-01T00:00:00Z",
		"providers":{"openai":{"display_name":"OpenAI","default_base_url":" https://api.openai.com/v1 "},"local":{"display_name":"Local"}},
		"models":{"gpt-6-luna":{"display_name":"Luna"},"ada":{"display_name":"Ada"}},
		"provider_models":{
			"openai/gpt-6-luna":{"model_ref":"gpt-6-luna","enabled":true},
			"openai/ada":{"model_ref":"ada","enabled":true,"provider_model_id":"text-ada-001"},
			"openai/gpt-off":{"model_ref":"gpt-off","enabled":false},
			"openai/gpt-6-luna-latest":{"model_ref":"gpt-6-luna","enabled":true,"provider_model_id":"gpt-6-luna"},
			"openai/ ":{"model_ref":"blank","enabled":true},
			"anthropic/claude-x":{"model_ref":"claude-x","enabled":true}}}`))
	require.NoError(t, err)

	assert.Equal(t, []string{"gpt-6-luna", "text-ada-001"}, list.ProviderModelIDs("openai"),
		"enabled entries only, under the provider's own model ID, without blanks or duplicates")
	assert.Empty(t, list.ProviderModelIDs("mistral"))
	assert.Equal(t, "https://api.openai.com/v1", list.ProviderDefaultBaseURL("openai"))
	assert.Empty(t, list.ProviderDefaultBaseURL("local"))

	var missing *ModelList
	assert.Nil(t, missing.ProviderModelIDs("openai"))
	assert.Empty(t, missing.ProviderDefaultBaseURL("openai"))
}
