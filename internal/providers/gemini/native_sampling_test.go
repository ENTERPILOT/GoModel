package gemini

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
			assert.Equal(t, tt.wantOK, ok)
			assert.Equal(t, tt.major, major)
			assert.Equal(t, tt.minor, minor)
			assert.Equal(t, tt.wantDrops, dropsSamplingParameters(tt.model))
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
			err := json.Unmarshal([]byte(fmt.Sprintf(body, tt.model)), &req)
			require.NoError(t, err)

			cfg := geminiGenerationConfig(&req)

			for _, key := range []string{"temperature", "topP", "topK", "candidateCount"} {
				_, present := cfg[key]
				assert.Equal(t, tt.wantKept, present, "%s present", key)
			}
			assert.Equal(t, 32, cfg["maxOutputTokens"])
			assert.Contains(t, cfg, "presencePenalty")
			assert.Contains(t, cfg, "stopSequences")
		})
	}
}
