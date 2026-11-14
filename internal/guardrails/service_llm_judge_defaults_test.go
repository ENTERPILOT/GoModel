package guardrails

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/plugins"
	"github.com/enterpilot/gomodel/pluginapi"
)

// The stored config carries every default explicitly, so a change to a
// plugin's default reaches new instances only: an llm_judge instance stored
// when on_unclear defaulted to warn keeps warning, and a new one fails closed.
func TestServiceStoredLLMJudgeKeepsItsOnUnclear(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(Definition{Name: "stored", Type: "llm_judge", Config: rawConfig(t, map[string]any{"model": "a/b", "on_unclear": "warn"})})
	// The judge answers nothing that parses as a verdict.
	service := newService(t, store, replyChat("???"))
	require.NoError(t, service.Upsert(ctx, Definition{Name: "new", Type: "llm_judge", Config: rawConfig(t, map[string]any{"model": "a/b"})}))

	tests := []struct {
		name          string
		wantOnUnclear string
		wantAction    pluginapi.Action
	}{
		{name: "stored", wantOnUnclear: "warn", wantAction: pluginapi.ActionWarn},
		{name: "new", wantOnUnclear: "block", wantAction: pluginapi.ActionBlock},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var cfg map[string]any
			require.NoError(t, json.Unmarshal(store.definitions[tt.name].Config, &cfg))
			assert.Equal(t, tt.wantOnUnclear, cfg["on_unclear"], "the stored config names on_unclear explicitly")

			chains, err := service.BuildChains([]StepReference{{Ref: tt.name, Step: 10}})
			require.NoError(t, err)
			x := plugins.NewRequestState().NewExchange(ctx, pluginapi.Meta{})
			msg := pluginapi.TextMessage(pluginapi.RoleUser, "hello")
			msg.ID = "m0"
			x.Prompt = &pluginapi.Prompt{Messages: []pluginapi.Message{msg}}
			outcome, err := chains.Prompt.RunPrompt(ctx, x)
			require.NoError(t, err)
			assert.Equal(t, tt.wantAction, outcome.Decision.Action)
		})
	}
}
