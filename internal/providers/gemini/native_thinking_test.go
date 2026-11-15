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
		{name: "2.5 xhigh uses the high budget", model: "gemini-2.5-flash", effort: "xhigh", want: map[string]any{"thinkingBudget": 24576}},
		{name: "2.5 unknown effort is dropped", model: "gemini-2.5-flash", effort: "ultra", want: nil},
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
		{name: "4 argon clamps xhigh to high", model: "gemini-4-argon", effort: "xhigh", want: map[string]any{"thinkingLevel": "high"}},
		{name: "4 carbon clamps xhigh to high", model: "gemini-4-carbon", effort: "XHIGH", want: map[string]any{"thinkingLevel": "high"}},
		{name: "3.8 flash clamps xhigh to high", model: "gemini-3.8-flash", effort: "xhigh", want: map[string]any{"thinkingLevel": "high"}},
		{name: "3.1 pro clamps xhigh to high", model: "gemini-3.1-pro-preview", effort: "xhigh", want: map[string]any{"thinkingLevel": "high"}},
		{name: "models prefix clamps xhigh to high", model: "models/gemini-4-argon", effort: "xhigh", want: map[string]any{"thinkingLevel": "high"}},
		{name: "vertex 4 carbon clamps xhigh to high", model: "google/gemini-4-carbon", effort: "xhigh", want: map[string]any{"thinkingLevel": "high"}},
		{name: "latest alias clamps xhigh to high", model: "gemini-pro-latest", effort: "xhigh", want: map[string]any{"thinkingLevel": "high"}},
		{name: "latest alias clamps minimal to low", model: "gemini-flash-latest", effort: "minimal", want: map[string]any{"thinkingLevel": "low"}},
		{name: "4 hydrogen keeps xhigh", model: "gemini-4-hydrogen", effort: "xhigh", want: map[string]any{"thinkingLevel": "xhigh"}},
		{name: "4 hydrogen preview keeps xhigh", model: "gemini-4-hydrogen-preview", effort: "xhigh", want: map[string]any{"thinkingLevel": "xhigh"}},
		{name: "vertex 4 hydrogen keeps xhigh", model: "google/gemini-4-hydrogen", effort: "xhigh", want: map[string]any{"thinkingLevel": "xhigh"}},
		{name: "hydrogen embedded in a longer family is not matched", model: "gemini-4-hydrogenx", effort: "xhigh", want: map[string]any{"thinkingLevel": "high"}},
		{name: "4 argon passes max through", model: "gemini-4-argon", effort: "max", want: map[string]any{"thinkingLevel": "max"}},
		{name: "4 hydrogen passes max through", model: "gemini-4-hydrogen", effort: "max", want: map[string]any{"thinkingLevel": "max"}},
		{name: "4 argon passes high through", model: "gemini-4-argon", effort: "high", want: map[string]any{"thinkingLevel": "high"}},
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

// A thinking level the client set itself is forwarded as given: only the level
// derived from reasoning_effort is clamped.
func TestGeminiGenerationConfig_ExplicitThinkingConfigWins(t *testing.T) {
	tests := []struct {
		name, model, level string
	}{
		{name: "minimal on 3.8 flash", model: "gemini-3.8-flash", level: "minimal"},
		{name: "xhigh on 4 argon", model: "gemini-4-argon", level: "xhigh"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var req core.ChatRequest
			body := `{"model":"` + tt.model + `","messages":[{"role":"user","content":"hi"}],` +
				`"reasoning":{"effort":"` + tt.level + `"},` +
				`"extra_body":{"google":{"thinking_config":{"thinking_level":"` + tt.level + `"}}}}`
			require.NoError(t, json.Unmarshal([]byte(body), &req))

			cfg := geminiGenerationConfig(&req)
			require.Equal(t, map[string]any{"thinkingLevel": tt.level}, cfg["thinkingConfig"])
		})
	}
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
			name: "flat xhigh is clamped on 4 argon",
			body: `{"model":"gemini-4-argon","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"xhigh"}`,
			want: map[string]any{"thinkingLevel": "high"},
		},
		{
			name: "xhigh joins a partial thinking config clamped",
			body: `{"model":"gemini-4-argon","messages":[{"role":"user","content":"hi"}],"reasoning":{"effort":"xhigh"},` +
				`"extra_body":{"google":{"thinking_config":{"include_thoughts":true}}}}`,
			want: map[string]any{"thinkingLevel": "high", "includeThoughts": true},
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
