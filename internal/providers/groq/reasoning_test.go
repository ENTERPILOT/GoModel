package groq

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const minimalChatCompletionJSON = `{"id":"c1","object":"chat.completion","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}]}`

func TestChatCompletion_MapsReasoningPerModelFamily(t *testing.T) {
	tests := []struct {
		name       string
		model      string
		effort     string
		wantEffort string // "" means the field must be absent
	}{
		{name: "gpt-oss keeps the effort", model: "openai/gpt-oss-20b", effort: "low", wantEffort: "low"},
		{name: "gpt-oss caps max", model: "openai/gpt-oss-120b", effort: "max", wantEffort: "high"},
		{name: "gpt-oss cannot turn reasoning off", model: "openai/gpt-oss-20b", effort: "none", wantEffort: "low"},
		{name: "gpt-oss minimal", model: "openai/gpt-oss-20b", effort: "minimal", wantEffort: "low"},
		{name: "qwen3.6 turns reasoning on", model: "qwen/qwen3.6-27b", effort: "medium", wantEffort: "default"},
		{name: "qwen3.6 keeps none", model: "qwen/qwen3.6-27b", effort: "none", wantEffort: "none"},
		{name: "qwen3.8 keeps the level", model: "qwen/qwen3.8-27b", effort: "medium", wantEffort: "medium"},
		{name: "qwen3.8 keeps none", model: "qwen/qwen3.8-27b", effort: "none", wantEffort: "none"},
		{name: "qwen3.8 caps max", model: "qwen/qwen3.8-27b", effort: "max", wantEffort: "high"},
		{name: "other models drop it", model: "llama-3.3-70b-versatile", effort: "high"},
		{name: "empty effort drops it", model: "openai/gpt-oss-20b"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, capture := providertest.JSONServer(t, http.StatusOK, minimalChatCompletionJSON)
			provider := newTestProvider(server.URL)

			_, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
				Model:     tt.model,
				Messages:  []core.Message{{Role: "user", Content: "hi"}},
				Reasoning: &core.Reasoning{Effort: tt.effort},
			})
			require.NoError(t, err)

			raw := capture.Last(t).JSON(t)
			assert.NotContains(t, raw, "reasoning", "request body includes nested reasoning")
			if tt.wantEffort == "" {
				assert.NotContains(t, raw, "reasoning_effort")
				return
			}
			assert.Equal(t, tt.wantEffort, raw["reasoning_effort"])
		})
	}
}

func TestChatCompletion_DefaultsReasoningFormatPerModelFamily(t *testing.T) {
	tests := []struct {
		name       string
		model      string
		caller     string // caller-supplied reasoning_format, "" for none
		wantFormat any    // nil means the field must be absent
	}{
		{name: "qwen3 gets parsed so <think> is not the answer", model: "qwen/qwen3.6-27b", wantFormat: "parsed"},
		{name: "gpt-oss gets parsed", model: "openai/gpt-oss-20b", wantFormat: "parsed"},
		{name: "caller choice wins", model: "qwen/qwen3.6-27b", caller: "raw", wantFormat: "raw"},
		{name: "compound rejects the field", model: "groq/compound-mini"},
		{name: "plain chat models are left alone", model: "llama-3.3-70b-versatile"},
		{name: "whisper is left alone", model: "whisper-large-v3-turbo"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, capture := providertest.JSONServer(t, http.StatusOK, minimalChatCompletionJSON)
			provider := newTestProvider(server.URL)

			req := &core.ChatRequest{Model: tt.model, Messages: []core.Message{{Role: "user", Content: "hi"}}}
			if tt.caller != "" {
				extra, err := core.MergeUnknownJSONFields(req.ExtraFields, map[string]json.RawMessage{
					"reasoning_format": json.RawMessage(`"` + tt.caller + `"`),
				})
				require.NoError(t, err)
				req.ExtraFields = extra
			}
			_, err := provider.ChatCompletion(context.Background(), req)
			require.NoError(t, err)

			raw := capture.Last(t).JSON(t)
			if tt.wantFormat == nil {
				assert.NotContains(t, raw, "reasoning_format")
				return
			}
			assert.Equal(t, tt.wantFormat, raw["reasoning_format"])
		})
	}
}
