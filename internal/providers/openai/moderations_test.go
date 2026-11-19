package openai

import (
	"context"
	"net/http"
	"testing"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
)

const openAIModerationJSON = `{
	"id": "modr-123",
	"model": "omni-moderation-latest",
	"results": [{
		"flagged": true,
		"categories": {"violence": true, "harassment": false},
		"category_scores": {"violence": 0.91, "harassment": 0.01},
		"category_applied_input_types": {"violence": ["text", "image"], "harassment": ["text"]}
	}]
}`

func newModerationTestProvider(t *testing.T, status int, body string) (*Provider, *providertest.Capture) {
	t.Helper()
	server, capture := providertest.JSONServer(t, status, body)
	opts := providertest.Options(llmclient.Hooks{})
	opts.HTTPClient = server.Client()
	provider := New(providers.ProviderConfig{APIKey: "test-key", BaseURL: server.URL}, opts).(*Provider)
	return provider, capture
}

func TestCreateModeration_ForwardsInputVerbatim(t *testing.T) {
	tests := []struct {
		name  string
		body  string
		input any
	}{
		{name: "string", body: `{"model":"omni-moderation-latest","input":"hi"}`, input: "hi"},
		{name: "array", body: `{"model":"omni-moderation-latest","input":["a","b"]}`, input: []any{"a", "b"}},
		{
			name:  "multimodal",
			body:  `{"model":"omni-moderation-latest","input":[{"type":"text","text":"hi"},{"type":"image_url","image_url":{"url":"https://example.com/a.png"}}]}`,
			input: []any{map[string]any{"type": "text", "text": "hi"}, map[string]any{"type": "image_url", "image_url": map[string]any{"url": "https://example.com/a.png"}}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider, capture := newModerationTestProvider(t, http.StatusOK, openAIModerationJSON)
			req, err := core.DecodeModerationRequest([]byte(tt.body))
			require.NoError(t, err)

			resp, err := provider.CreateModeration(context.Background(), req)
			require.NoError(t, err)

			sent := capture.Last(t)
			assert.Equal(t, "/moderations", sent.Path)
			assert.Equal(t, "Bearer test-key", sent.Header.Get("Authorization"))
			body := sent.JSON(t)
			assert.Equal(t, "omni-moderation-latest", body["model"])
			assert.Equal(t, tt.input, body["input"])

			assert.Equal(t, "modr-123", resp.ID)
			require.Len(t, resp.Results, 1)
			result := resp.Results[0]
			assert.True(t, result.Flagged)
			assert.True(t, result.Categories["violence"])
			assert.InDelta(t, 0.91, result.CategoryScores["violence"], 1e-9)
			assert.Equal(t, []string{"text", "image"}, result.CategoryAppliedInputTypes["violence"])
		})
	}
}

func TestCreateModeration_DefaultsModel(t *testing.T) {
	provider, capture := newModerationTestProvider(t, http.StatusOK, openAIModerationJSON)

	_, err := provider.CreateModeration(context.Background(), &core.ModerationRequest{Input: json.RawMessage(`"hi"`)})
	require.NoError(t, err)
	assert.Equal(t, core.DefaultModerationModel, capture.Last(t).JSON(t)["model"])
}

func TestCreateModeration_RejectsInvalidInputWithoutCallingUpstream(t *testing.T) {
	provider, capture := newModerationTestProvider(t, http.StatusOK, openAIModerationJSON)

	_, err := provider.CreateModeration(context.Background(), &core.ModerationRequest{Input: json.RawMessage(`42`)})
	require.Error(t, err)
	assert.Zero(t, capture.Count())
}

func TestCreateModeration_PropagatesUpstreamError(t *testing.T) {
	provider, _ := newModerationTestProvider(t, http.StatusBadRequest,
		`{"error":{"message":"Invalid value for 'input'.","type":"invalid_request_error"}}`)

	_, err := provider.CreateModeration(context.Background(), &core.ModerationRequest{Input: json.RawMessage(`"hi"`)})
	require.Error(t, err)
	var gwErr *core.GatewayError
	require.ErrorAs(t, err, &gwErr)
	assert.Equal(t, http.StatusBadRequest, gwErr.HTTPStatusCode())
	assert.Contains(t, gwErr.Message, "Invalid value for 'input'")
}

func TestCompatibleProvider_DoesNotAdvertiseModeration(t *testing.T) {
	compatible, _ := newTestProvider(t, nil)
	_, ok := any(compatible).(core.ModerationProvider)
	assert.False(t, ok, "OpenAI-compatible upstreams (Azure, OpenRouter, ...) have no /moderations endpoint")
}
