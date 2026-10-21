package guardrails

import (
	"context"
	"testing"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/plugins"
)

// fakeChatModels describes models by name; a model it does not list is
// unknown and accepted.
type fakeChatModels map[string]string

func (m fakeChatModels) ChatUnsupportedReason(_ context.Context, model string) string {
	return m[model]
}

// A guardrail whose model field names a model that cannot answer chat, such
// as a decision model picked as an LLM judge, is refused when saved: every
// request would otherwise fail the judge call and the fail mode would allow
// or block all traffic.
func TestServiceUpsertRefusesModelsThatCannotChat(t *testing.T) {
	models := fakeChatModels{
		"jev/jev-latest":      "is a decision model (it answers /v1/decisions or /v1/systemone), not a chat model",
		"openai/text-embed-3": "serves embedding only, not chat",
	}
	tests := []struct {
		name    string
		def     Definition
		wantErr string
	}{
		{name: "llm judge on a decision model", def: Definition{Name: "judge", Type: "llm_judge", Config: json.RawMessage(`{"model":"jev/jev-latest"}`)}, wantErr: `model: plugins: model "jev/jev-latest" is a decision model`},
		{name: "altering on an embedding model", def: Definition{Name: "alter", Type: "llm_based_altering", Config: json.RawMessage(`{"model":"openai/text-embed-3"}`)}, wantErr: "serves embedding only, not chat"},
		{name: "llm judge on a chat model", def: Definition{Name: "judge", Type: "llm_judge", Config: json.RawMessage(`{"model":"openai/gpt-6-luna"}`)}},
		{name: "a model the gateway cannot describe yet", def: Definition{Name: "judge", Type: "llm_judge", Config: json.RawMessage(`{"model":"later/unknown"}`)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service, err := NewService(newTestStore(), testCatalog(t), plugins.HostDeps{ChatModels: models})
			require.NoError(t, err)

			err = service.Upsert(context.Background(), tt.def)
			if tt.wantErr == "" {
				require.NoError(t, err)
				assert.Equal(t, 1, service.Len())
				return
			}
			require.Error(t, err)
			assert.True(t, IsValidationError(err), "%v", err)
			assert.Contains(t, err.Error(), tt.wantErr)
			assert.Contains(t, err.Error(), "choose a chat model")
			assert.Equal(t, 0, service.Len())
		})
	}
}
