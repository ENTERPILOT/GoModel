package gemini

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
)

func TestGeminiGeneration(t *testing.T) {
	tests := []struct {
		model     string
		major     int
		minor     int
		wantOK    bool
		wantDrops bool
	}{
		{model: "gemini-3.8-flash", major: 3, minor: 8, wantOK: true, wantDrops: true},
		{model: "gemini-3.8-flash-cyber", major: 3, minor: 8, wantOK: true, wantDrops: true},
		{model: "gemini-3.5-flash-lite", major: 3, minor: 5, wantOK: true, wantDrops: true},
		{model: "gemini-3-pro-preview", major: 3, minor: 0, wantOK: true, wantDrops: true},
		{model: "google/gemini-3.7-flash", major: 3, minor: 7, wantOK: true, wantDrops: true},
		{model: "Gemini-4-Flash", major: 4, minor: 0, wantOK: true, wantDrops: true},
		{model: "gemini-2.5-flash", major: 2, minor: 5, wantOK: true},
		{model: "gemini-2.5-flash-image", major: 2, minor: 5, wantOK: true},
		{model: "gemini-embedding-001"},
		{model: "gemma-3-27b-it"},
		{model: "imagen-4.0-generate-001"},
		{model: ""},
	}
	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			major, minor, ok := geminiGeneration(tt.model)
			if ok != tt.wantOK || major != tt.major || minor != tt.minor {
				t.Fatalf("geminiGeneration(%q) = (%d, %d, %v), want (%d, %d, %v)", tt.model, major, minor, ok, tt.major, tt.minor, tt.wantOK)
			}
			if got := dropsSamplingParameters(tt.model); got != tt.wantDrops {
				t.Fatalf("dropsSamplingParameters(%q) = %v, want %v", tt.model, got, tt.wantDrops)
			}
		})
	}
}

func TestGeminiGenerationConfig_DropsSamplingParametersOnGemini3(t *testing.T) {
	const body = `{"model":%q,"messages":[{"role":"user","content":"hi"}],` +
		`"max_tokens":32,"temperature":0.2,"top_p":0.9,"top_k":40,"candidate_count":2,` +
		`"presence_penalty":0.5,"stop":["END"]}`
	tests := []struct {
		name     string
		model    string
		wantKept bool
	}{
		{name: "3.8 flash drops sampling parameters", model: "gemini-3.8-flash"},
		{name: "3 pro preview drops sampling parameters", model: "gemini-3-pro-preview"},
		{name: "vertex publisher prefix drops sampling parameters", model: "google/gemini-3.5-flash"},
		{name: "2.5 flash keeps sampling parameters", model: "gemini-2.5-flash", wantKept: true},
		{name: "non-gemini model keeps sampling parameters", model: "gemma-3-27b-it", wantKept: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var req core.ChatRequest
			if err := json.Unmarshal([]byte(fmt.Sprintf(body, tt.model)), &req); err != nil {
				t.Fatalf("Unmarshal() error = %v", err)
			}
			cfg := geminiGenerationConfig(&req)

			for _, key := range []string{"temperature", "topP", "topK", "candidateCount"} {
				_, present := cfg[key]
				if present != tt.wantKept {
					t.Errorf("generationConfig[%q] present = %v, want %v (cfg = %#v)", key, present, tt.wantKept, cfg)
				}
			}
			if got := cfg["maxOutputTokens"]; got != 32 {
				t.Errorf("maxOutputTokens = %#v, want 32", got)
			}
			if _, ok := cfg["presencePenalty"]; !ok {
				t.Errorf("presencePenalty missing: %#v", cfg)
			}
			if _, ok := cfg["stopSequences"]; !ok {
				t.Errorf("stopSequences missing: %#v", cfg)
			}
		})
	}
}
