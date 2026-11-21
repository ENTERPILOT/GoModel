package mistral

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
)

const (
	mistralCleanResult = `{"categories": {"sexual": false, "violence_and_threats": false, "pii": false},
		"category_scores": {"sexual": 0.001, "violence_and_threats": 0.002, "pii": 0.01}}`
	mistralViolentResult = `{"categories": {"sexual": false, "violence_and_threats": true, "pii": false},
		"category_scores": {"sexual": 0.001, "violence_and_threats": 0.97, "pii": 0.01}}`
)

// mistralModerationJSON is a Mistral moderation reply with the given results.
func mistralModerationJSON(results ...string) string {
	return `{"id": "4d71ae510af942108ef7344f903e2b88", "model": "mistral-moderation-latest", "results": [` +
		strings.Join(results, ",") + `]}`
}

func TestCreateModeration_TranslatesToOpenAIShape(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		want        []any
		results     []string
		wantFlagged []bool
	}{
		{name: "string", input: `"I will hurt you"`, want: []any{"I will hurt you"}, results: []string{mistralViolentResult}, wantFlagged: []bool{true}},
		{
			name: "array", input: `["hello","I will hurt you"]`, want: []any{"hello", "I will hurt you"},
			results: []string{mistralCleanResult, mistralViolentResult}, wantFlagged: []bool{false, true},
		},
		{
			name: "text parts", input: `[{"type":"text","text":"I will"},{"type":"text","text":"hurt you"}]`, want: []any{"I will\nhurt you"},
			results: []string{mistralViolentResult}, wantFlagged: []bool{true},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, capture := providertest.JSONServer(t, http.StatusOK, mistralModerationJSON(tt.results...))
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
			require.Len(t, resp.Results, len(tt.want), "one result per input")
			for i, want := range tt.wantFlagged {
				assert.Equal(t, want, resp.Results[i].Flagged, "result %d", i)
			}
			last := resp.Results[len(resp.Results)-1]
			assert.True(t, last.Categories["violence_and_threats"])
			assert.InDelta(t, 0.97, last.CategoryScores["violence_and_threats"], 1e-9)
			assert.Equal(t, []string{"text"}, last.CategoryAppliedInputTypes["pii"])
		})
	}
}

func TestCreateModeration_ForwardsMetadata(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, mistralModerationJSON(mistralCleanResult))
	provider := newTestProvider(server).(*Provider)

	req, err := core.DecodeModerationRequest([]byte(`{"model":"mistral-moderation-latest","input":"hello","metadata":{"team":"support"},"unknown":1}`))
	require.NoError(t, err)
	_, err = provider.CreateModeration(context.Background(), req)
	require.NoError(t, err)

	sent := capture.Last(t).JSON(t)
	assert.Equal(t, map[string]any{"team": "support"}, sent["metadata"])
	assert.NotContains(t, sent, "unknown", "only fields Mistral's endpoint takes are forwarded")
}

func TestCreateModeration_RejectsImageInput(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, mistralModerationJSON(mistralCleanResult))
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
