package cohere

import (
	"testing"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
)

// Cohere reads no reasoning effort, and its reasoning models think by default,
// so the adapter writes the effort as Cohere's thinking control.
func TestChatRequestTranslatesReasoningEffortToThinking(t *testing.T) {
	const reasoner, plain = "command-a-reasoning-08-2025", "command-a-03-2025"
	tests := []struct {
		name      string
		model     string
		effort    string
		maxTokens *int
		extras    map[string]json.RawMessage
		want      string // "" when no thinking control is sent
	}{
		{name: "no effort keeps the model's default", model: reasoner},
		{name: "none turns thinking off", model: reasoner, effort: "none", want: `{"type":"disabled"}`},
		{name: "none is sent to any model", model: plain, effort: "none", want: `{"type":"disabled"}`},
		{name: "low", model: reasoner, effort: "low", want: `{"type":"enabled","token_budget":1024}`},
		{name: "minimal is the lowest budget", model: reasoner, effort: " Minimal ", want: `{"type":"enabled","token_budget":1024}`},
		{name: "medium", model: reasoner, effort: "medium", want: `{"type":"enabled","token_budget":8192}`},
		{name: "high", model: reasoner, effort: "high", want: `{"type":"enabled","token_budget":24576}`},
		{name: "max keeps the default, unlimited thinking", model: reasoner, effort: "max"},
		{name: "a value off the scale keeps the default", model: reasoner, effort: "ultra"},
		{name: "a model that does not reason refuses enabled thinking", model: plain, effort: "high"},
		{name: "review request: command-a-plus thinks too", model: "command-a-plus-05-2026", effort: "low", want: `{"type":"enabled","token_budget":1024}`},
		{
			name: "review request: the flat chat effort", model: reasoner,
			extras: map[string]json.RawMessage{"reasoning_effort": json.RawMessage(`"medium"`)},
			want:   `{"type":"enabled","token_budget":8192}`,
		},
		{
			name: "the reasoning object wins over the flat effort", model: reasoner, effort: "none",
			extras: map[string]json.RawMessage{"reasoning_effort": json.RawMessage(`"high"`)},
			want:   `{"type":"disabled"}`,
		},
		{name: "a budget leaves room for the answer", model: reasoner, effort: "high", maxTokens: new(4096), want: `{"type":"enabled","token_budget":3072}`},
		{name: "thinking is off when no budget fits", model: reasoner, effort: "low", maxTokens: new(1024), want: `{"type":"disabled"}`},
		{
			name: "Cohere's own control wins over the effort", model: reasoner, effort: "none",
			extras: map[string]json.RawMessage{"thinking": json.RawMessage(`{"type":"enabled","token_budget":500}`)},
			want:   `{"type":"enabled","token_budget":500}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := &core.ChatRequest{
				Model:       tt.model,
				Messages:    []core.Message{{Role: "user", Content: "hi"}},
				MaxTokens:   tt.maxTokens,
				ExtraFields: core.UnknownJSONFieldsFromMap(tt.extras),
			}
			if tt.effort != "" {
				req.Reasoning = &core.Reasoning{Effort: tt.effort}
			}

			out, err := toCohereChatRequest(req, false)
			require.NoError(t, err)

			if tt.want == "" {
				assert.Empty(t, out.Thinking)
				return
			}
			assert.JSONEq(t, tt.want, string(out.Thinking))
		})
	}
}
