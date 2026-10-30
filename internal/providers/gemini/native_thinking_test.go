package gemini

import (
	"encoding/json"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/stretchr/testify/require"
)

func TestThinkingConfigForEffort(t *testing.T) {
	tests := []struct {
		name   string
		model  string
		effort string
		want   map[string]any
	}{
		{name: "2.5 none disables thinking", model: "gemini-2.5-flash", effort: "none", want: map[string]any{"thinkingBudget": 0}},
		{name: "2.5 minimal uses budget", model: "gemini-2.5-flash", effort: "minimal", want: map[string]any{"thinkingBudget": 1024}},
		{name: "2.5 high uses budget", model: "gemini-2.5-pro", effort: "high", want: map[string]any{"thinkingBudget": 24576}},
		{name: "2.5 unknown effort is dropped", model: "gemini-2.5-flash", effort: "xhigh", want: nil},
		{name: "3 flash keeps minimal", model: "gemini-3-flash-preview", effort: "minimal", want: map[string]any{"thinkingLevel": "minimal"}},
		{name: "3.5 flash none becomes minimal", model: "gemini-3.5-flash", effort: "none", want: map[string]any{"thinkingLevel": "minimal"}},
		{name: "3.6 flash keeps minimal", model: "gemini-3.6-flash", effort: "MINIMAL", want: map[string]any{"thinkingLevel": "minimal"}},
		{name: "3.7 flash clamps minimal to low", model: "gemini-3.7-flash", effort: "minimal", want: map[string]any{"thinkingLevel": "low"}},
		{name: "3.8 flash clamps minimal to low", model: "gemini-3.8-flash", effort: "minimal", want: map[string]any{"thinkingLevel": "low"}},
		{name: "3.8 flash clamps none to low", model: "gemini-3.8-flash", effort: "none", want: map[string]any{"thinkingLevel": "low"}},
		{name: "3.8 flash cyber clamps minimal to low", model: "gemini-3.8-flash-cyber", effort: "minimal", want: map[string]any{"thinkingLevel": "low"}},
		{name: "3.8 flash passes medium through", model: "gemini-3.8-flash", effort: "medium", want: map[string]any{"thinkingLevel": "medium"}},
		{name: "3.8 flash passes high through", model: "gemini-3.8-flash", effort: " high ", want: map[string]any{"thinkingLevel": "high"}},
		{name: "3.1 pro clamps minimal to low", model: "gemini-3.1-pro-preview", effort: "minimal", want: map[string]any{"thinkingLevel": "low"}},
		{name: "3 pro clamps minimal to low", model: "gemini-3-pro-preview", effort: "minimal", want: map[string]any{"thinkingLevel": "low"}},
		{name: "4 argon clamps minimal to low", model: "gemini-4-argon", effort: "minimal", want: map[string]any{"thinkingLevel": "low"}},
		{name: "4 argon clamps none to low", model: "gemini-4-argon", effort: "none", want: map[string]any{"thinkingLevel": "low"}},
		{name: "4 argon passes medium through", model: "gemini-4-argon", effort: "medium", want: map[string]any{"thinkingLevel": "medium"}},
		{name: "4 argon cyber permissive clamps minimal to low", model: "gemini-4-argon-cyber-permissive", effort: "minimal", want: map[string]any{"thinkingLevel": "low"}},
		{name: "4 carbon clamps minimal to low", model: "gemini-4-carbon", effort: "minimal", want: map[string]any{"thinkingLevel": "low"}},
		{name: "vertex 4 argon clamps none to low", model: "google/gemini-4-argon", effort: "none", want: map[string]any{"thinkingLevel": "low"}},
		{name: "vertex publisher prefix is honored", model: "google/gemini-3.8-flash", effort: "none", want: map[string]any{"thinkingLevel": "low"}},
		{name: "3.8 pro is outside the documented clamp set", model: "gemini-3.8-pro-preview", effort: "minimal", want: map[string]any{"thinkingLevel": "minimal"}},
		{name: "3.70 is not 3.7", model: "gemini-3.70-flash-preview", effort: "none", want: map[string]any{"thinkingLevel": "minimal"}},
		{name: "3.8 flash embedded in a longer name is not matched", model: "my-gemini-3.8-flash", effort: "minimal", want: map[string]any{"thinkingLevel": "minimal"}},
		{name: "gemma has no thinking level", model: "gemma-4-31b-it", effort: "low", want: nil},
		{name: "gemma drops none too", model: "gemma-4-31b-it", effort: "none", want: nil},
		{name: "gemma behind a publisher prefix", model: "models/gemma-4-26b-a4b-it", effort: "high", want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, thinkingConfigForEffort(tt.model, tt.effort))
		})
	}
}

func TestGeminiGenerationConfig_ExplicitThinkingConfigWins(t *testing.T) {
	var req core.ChatRequest
	body := `{"model":"gemini-3.8-flash","messages":[{"role":"user","content":"hi"}],` +
		`"reasoning":{"effort":"minimal"},` +
		`"extra_body":{"google":{"thinking_config":{"thinking_level":"minimal"}}}}`
	err := json.Unmarshal([]byte(body), &req)
	require.NoError(t, err)

	cfg := geminiGenerationConfig(&req)
	require.Equal(t, map[string]any{"thinkingLevel": "minimal"}, cfg["thinkingConfig"])
}

func TestGeminiGenerationConfig_EffortJoinsPartialThinkingConfig(t *testing.T) {
	var req core.ChatRequest
	body := `{"model":"gemini-2.5-flash","messages":[{"role":"user","content":"hi"}],` +
		`"reasoning":{"effort":"none"},` +
		`"extra_body":{"google":{"thinking_config":{"include_thoughts":true}}}}`
	err := json.Unmarshal([]byte(body), &req)
	require.NoError(t, err)

	cfg := geminiGenerationConfig(&req)
	require.Equal(t, map[string]any{"thinkingBudget": 0, "includeThoughts": true}, cfg["thinkingConfig"])
}

func TestGeminiGenerationConfig_FlatReasoningEffort(t *testing.T) {
	tests := []struct {
		name string
		body string
		want map[string]any
	}{
		{
			name: "flat field maps like the reasoning object",
			body: `{"model":"gemini-2.5-flash","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"none"}`,
			want: map[string]any{"thinkingBudget": 0},
		},
		{
			name: "flat field is normalized",
			body: `{"model":"gemini-3.8-flash","messages":[{"role":"user","content":"hi"}],"reasoning_effort":" HIGH "}`,
			want: map[string]any{"thinkingLevel": "high"},
		},
		{
			name: "reasoning object wins",
			body: `{"model":"gemini-2.5-flash","messages":[{"role":"user","content":"hi"}],"reasoning":{"effort":"high"},"reasoning_effort":"none"}`,
			want: map[string]any{"thinkingBudget": 24576},
		},
		{
			name: "empty reasoning object falls back to the flat field",
			body: `{"model":"gemini-2.5-flash","messages":[{"role":"user","content":"hi"}],"reasoning":{},"reasoning_effort":"low"}`,
			want: map[string]any{"thinkingBudget": 1024},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var req core.ChatRequest
			require.NoError(t, json.Unmarshal([]byte(tt.body), &req))

			cfg := geminiGenerationConfig(&req)
			require.Equal(t, tt.want, cfg["thinkingConfig"])
		})
	}
}

func TestGeminiGenerationConfig_NonStringFlatEffortIsIgnored(t *testing.T) {
	var req core.ChatRequest
	body := `{"model":"gemini-2.5-flash","messages":[{"role":"user","content":"hi"}],"reasoning_effort":3}`
	require.NoError(t, json.Unmarshal([]byte(body), &req))

	require.Nil(t, geminiGenerationConfig(&req))
}
