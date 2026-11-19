package mistral

import (
	"context"
	"net/http"
	"testing"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
)

const mistralModerationJSON = `{
	"id": "4d71ae510af942108ef7344f903e2b88",
	"model": "mistral-moderation-latest",
	"results": [
		{"categories": {"sexual": false, "violence_and_threats": false, "pii": false},
		 "category_scores": {"sexual": 0.001, "violence_and_threats": 0.002, "pii": 0.01}},
		{"categories": {"sexual": false, "violence_and_threats": true, "pii": false},
		 "category_scores": {"sexual": 0.001, "violence_and_threats": 0.97, "pii": 0.01}}
	]
}`

func TestCreateModeration_TranslatesToOpenAIShape(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []any
	}{
		{name: "string", input: `"hello"`, want: []any{"hello"}},
		{name: "array", input: `["hello","I will hurt you"]`, want: []any{"hello", "I will hurt you"}},
		{name: "text parts", input: `[{"type":"text","text":"hello"},{"type":"text","text":"there"}]`, want: []any{"hello\nthere"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, capture := providertest.JSONServer(t, http.StatusOK, mistralModerationJSON)
			provider := newTestProvider(server).(*Provider)

			resp, err := provider.CreateModeration(context.Background(), &core.ModerationRequest{
				Model: "mistral-moderation-latest",
				Input: json.RawMessage(tt.input),
			})
			require.NoError(t, err)

			sent := capture.Last(t)
			assert.Equal(t, "/moderations", sent.Path)
			assert.Equal(t, "Bearer mistral-key", sent.Header.Get("Authorization"))
			assert.Equal(t, map[string]any{"model": "mistral-moderation-latest", "input": tt.want}, sent.JSON(t))

			assert.Equal(t, "4d71ae510af942108ef7344f903e2b88", resp.ID)
			assert.Equal(t, "mistral-moderation-latest", resp.Model)
			require.Len(t, resp.Results, 2)
			assert.False(t, resp.Results[0].Flagged)
			assert.True(t, resp.Results[1].Flagged)
			assert.True(t, resp.Results[1].Categories["violence_and_threats"])
			assert.InDelta(t, 0.97, resp.Results[1].CategoryScores["violence_and_threats"], 1e-9)
			assert.Equal(t, []string{"text"}, resp.Results[1].CategoryAppliedInputTypes["pii"])
		})
	}
}

func TestCreateModeration_RejectsImageInput(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, mistralModerationJSON)
	provider := newTestProvider(server).(*Provider)

	_, err := provider.CreateModeration(context.Background(), &core.ModerationRequest{
		Model: "mistral-moderation-latest",
		Input: json.RawMessage(`[{"type":"image_url","image_url":{"url":"https://example.com/a.png"}}]`),
	})
	providertest.AssertUnsupported(t, err)
	assert.Contains(t, err.Error(), "text input only")
	assert.Zero(t, capture.Count())
}

func TestCreateModeration_PropagatesUpstreamError(t *testing.T) {
	server, _ := providertest.JSONServer(t, http.StatusUnauthorized, `{"message":"Unauthorized"}`)
	provider := newTestProvider(server).(*Provider)

	_, err := provider.CreateModeration(context.Background(), &core.ModerationRequest{
		Model: "mistral-moderation-latest",
		Input: json.RawMessage(`"hi"`),
	})
	require.Error(t, err)
	var gwErr *core.GatewayError
	require.ErrorAs(t, err, &gwErr)
	assert.Equal(t, http.StatusUnauthorized, gwErr.HTTPStatusCode())
}
