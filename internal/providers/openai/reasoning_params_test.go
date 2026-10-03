package openai

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
)

// Each model family accepts its own set of reasoning_effort values on Chat
// Completions (verified against the API); a level outside the set moves to the
// closest accepted one instead of failing with a 400.
func TestSupportedEffort(t *testing.T) {
	tests := []struct {
		model, effort, want string
	}{
		{"gpt-5-mini", "none", "minimal"},
		{"gpt-5-mini", "xhigh", "high"},
		{"gpt-5-mini", "minimal", "minimal"},
		{"gpt-5.1", "minimal", "low"},
		{"gpt-5.1", "max", "high"},
		{"gpt-5.1", "none", "none"},
		{"gpt-5.5", "minimal", "low"},
		{"gpt-5.5", "max", "xhigh"},
		{"gpt-6-luna", "minimal", "low"},
		{"gpt-6-luna", "none", "none"},
		{"gpt-6-luna", "max", "xhigh"},
		{"gpt-6-astra", "none", "low"},
		{"gpt-6.1-sol", "minimal", "low"},
		{"gpt-6.1-sol", "xhigh", "xhigh"},
		{"o4-mini", "minimal", "low"},
		{"o4-mini", "max", "high"},
		{"gpt-4o", "max", "max"},
		{"qwen3-32b", "minimal", "minimal"},
		{"gpt-6-luna", "turbo", "turbo"},
	}
	for _, tt := range tests {
		t.Run(tt.model+"/"+tt.effort, func(t *testing.T) {
			assert.Equal(t, tt.want, supportedEffort(tt.model, tt.effort))
		})
	}
}

// The chat path sends the mapped effort, whether it arrived nested or flat,
// and drops top_p for reasoning models as it does temperature.
func TestChatCompletion_AdaptsEffortAndTopP(t *testing.T) {
	topP := 0.5
	tests := []struct {
		name       string
		req        core.ChatRequest
		wantEffort any // nil means absent
		wantTopP   any
	}{
		{name: "nested minimal on gpt-6", req: core.ChatRequest{Model: "gpt-6-luna", Reasoning: &core.Reasoning{Effort: "minimal"}},
			wantEffort: "low"},
		{name: "flat max on gpt-5.5", req: core.ChatRequest{Model: "gpt-5.5",
			ExtraFields: core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{"reasoning_effort": json.RawMessage(`"max"`)})},
			wantEffort: "xhigh"},
		{name: "supported flat effort kept", req: core.ChatRequest{Model: "gpt-5.5",
			ExtraFields: core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{"reasoning_effort": json.RawMessage(`"high"`)})},
			wantEffort: "high"},
		{name: "top_p dropped on a reasoning model", req: core.ChatRequest{Model: "gpt-6-luna", TopP: &topP}},
		{name: "top_p kept on gpt-4o", req: core.ChatRequest{Model: "gpt-4o", TopP: &topP}, wantTopP: 0.5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, capture := providertest.JSONServer(t, http.StatusOK, providertest.ChatCompletionJSON)
			provider := New(providers.ProviderConfig{APIKey: testAPIKey, BaseURL: server.URL}, providertest.Options(llmclient.Hooks{})).(*Provider)
			req := tt.req
			req.Messages = []core.Message{{Role: "user", Content: "hi"}}

			_, err := provider.ChatCompletion(context.Background(), &req)
			require.NoError(t, err)

			sent := capture.Last(t).JSON(t)
			assert.Equal(t, tt.wantEffort, sent["reasoning_effort"])
			assert.Equal(t, tt.wantTopP, sent["top_p"])
		})
	}
}
