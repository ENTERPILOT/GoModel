package gemini

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
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
		{name: "vertex publisher prefix is honored", model: "google/gemini-3.8-flash", effort: "none", want: map[string]any{"thinkingLevel": "low"}},
		{name: "3.8 pro is outside the documented clamp set", model: "gemini-3.8-pro-preview", effort: "minimal", want: map[string]any{"thinkingLevel": "minimal"}},
		{name: "3.70 is not 3.7", model: "gemini-3.70-flash-preview", effort: "none", want: map[string]any{"thinkingLevel": "minimal"}},
		{name: "3.8 flash embedded in a longer name is not matched", model: "my-gemini-3.8-flash", effort: "minimal", want: map[string]any{"thinkingLevel": "minimal"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := thinkingConfigForEffort(tt.model, tt.effort)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("thinkingConfigForEffort(%q, %q) = %#v, want %#v", tt.model, tt.effort, got, tt.want)
			}
		})
	}
}

func TestGeminiGenerationConfig_ExplicitThinkingConfigWins(t *testing.T) {
	var req core.ChatRequest
	body := `{"model":"gemini-3.8-flash","messages":[{"role":"user","content":"hi"}],` +
		`"reasoning":{"effort":"minimal"},` +
		`"extra_body":{"google":{"thinking_config":{"thinking_level":"minimal"}}}}`
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}

	cfg := geminiGenerationConfig(&req)
	want := map[string]any{"thinkingLevel": "minimal"}
	if got := cfg["thinkingConfig"]; !reflect.DeepEqual(got, want) {
		t.Fatalf("thinkingConfig = %#v, want explicit %#v", got, want)
	}
}
