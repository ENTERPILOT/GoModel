package core

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUsageUnmarshalJSON_PreservesExtendedFields(t *testing.T) {
	var usage Usage
	err := json.Unmarshal([]byte(`{
		"prompt_tokens": 120,
		"completion_tokens": 30,
		"total_tokens": 150,
		"prompt_tokens_details": {
			"cached_tokens": 80
		},
		"completion_tokens_details": {
			"reasoning_tokens": 12
		},
		"cost_in_usd_ticks": 969250,
		"num_sources_used": 2
	}`), &usage)
	require.NoError(t, err)
	require.Equal(t, 120, usage.PromptTokens)
	require.NotNil(t, usage.PromptTokensDetails)
	require.Equal(t, 80, usage.PromptTokensDetails.CachedTokens)
	require.NotNil(t, usage.CompletionTokensDetails)
	require.Equal(t, 12, usage.CompletionTokensDetails.ReasoningTokens)
	require.Equal(t, float64(969250), usage.RawUsage["cost_in_usd_ticks"])
	require.Equal(t, float64(2), usage.RawUsage["num_sources_used"])
}

func TestUsageJSON_RoundTripsCacheWriteTokens(t *testing.T) {
	tests := []struct {
		name    string
		details string
		want    any // nil means the member must be absent
	}{
		{name: "reported", details: `{"cached_tokens":800,"cache_write_tokens":150}`, want: float64(150)},
		{name: "zero is omitted", details: `{"cached_tokens":800,"cache_write_tokens":0}`},
		{name: "absent stays absent", details: `{"cached_tokens":800}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var usage Usage
			require.NoError(t, json.Unmarshal([]byte(`{"prompt_tokens":1000,"prompt_tokens_details":`+tt.details+`}`), &usage))
			body, err := json.Marshal(usage)
			require.NoError(t, err)

			var payload struct {
				PromptTokensDetails map[string]any `json:"prompt_tokens_details"`
			}
			require.NoError(t, json.Unmarshal(body, &payload))
			got, exists := payload.PromptTokensDetails["cache_write_tokens"]
			if tt.want == nil {
				assert.False(t, exists, "unexpected cache_write_tokens in %s", body)
			} else {
				assert.Equal(t, tt.want, got)
			}
		})
	}
}

func TestUsageMarshalJSON_MergesRawUsageIntoTopLevelUsage(t *testing.T) {
	body, err := json.Marshal(Usage{
		PromptTokens:     200,
		CompletionTokens: 40,
		TotalTokens:      240,
		PromptTokensDetails: &PromptTokensDetails{
			CachedTokens: 140,
		},
		RawUsage: map[string]any{
			"cache_read_input_tokens": 140,
			"service_tier":            "priority",
		},
	})
	require.NoError(t, err)

	var payload map[string]any
	err = json.Unmarshal(body, &payload)
	require.NoError(t, err)
	require.Equal(t, float64(140), payload["cache_read_input_tokens"])
	require.Equal(t, "priority", payload["service_tier"])
	_, exists := payload["raw_usage"]
	require.False(t, exists, "did not expect raw_usage field in marshaled usage payload: %s", string(body))
}

func TestResponsesUsageUnmarshalJSON_AcceptsResponsesDetailFieldNames(t *testing.T) {
	var usage ResponsesUsage
	err := json.Unmarshal([]byte(`{
		"input_tokens": 125,
		"output_tokens": 48,
		"total_tokens": 173,
		"input_tokens_details": {
			"cached_tokens": 98
		},
		"output_tokens_details": {
			"reasoning_tokens": 7
		},
		"cost_in_usd_ticks": 158500
	}`), &usage)
	require.NoError(t, err)
	require.Equal(t, 125, usage.InputTokens)
	require.NotNil(t, usage.PromptTokensDetails)
	require.Equal(t, 98, usage.PromptTokensDetails.CachedTokens)
	require.NotNil(t, usage.CompletionTokensDetails)
	require.Equal(t, 7, usage.CompletionTokensDetails.ReasoningTokens)
	require.Equal(t, float64(158500), usage.RawUsage["cost_in_usd_ticks"])
}

func TestResponsesUsageMarshalJSON_UsesResponsesDetailFieldNames(t *testing.T) {
	body, err := json.Marshal(ResponsesUsage{
		InputTokens:  125,
		OutputTokens: 48,
		TotalTokens:  173,
		PromptTokensDetails: &PromptTokensDetails{
			CachedTokens: 98,
		},
		CompletionTokensDetails: &CompletionTokensDetails{
			ReasoningTokens: 7,
		},
		RawUsage: map[string]any{
			"cache_read_input_tokens": 98,
		},
	})
	require.NoError(t, err)

	var payload map[string]any
	err = json.Unmarshal(body, &payload)
	require.NoError(t, err)
	_, exists := payload["prompt_tokens_details"]
	require.False(t, exists, "did not expect prompt_tokens_details in marshaled responses payload: %s", string(body))
	_, exists = payload["completion_tokens_details"]
	require.False(t, exists, "did not expect completion_tokens_details in marshaled responses payload: %s", string(body))

	inputDetails, ok := payload["input_tokens_details"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, float64(98), inputDetails["cached_tokens"])

	outputDetails, ok := payload["output_tokens_details"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, float64(7), outputDetails["reasoning_tokens"])
	_, exists = // The Responses usage object is closed: provider-named members stay in
		// RawUsage for usage records and cost calculation.
		payload["cache_read_input_tokens"]
	require.False(t, exists, "did not expect cache_read_input_tokens in marshaled responses payload: %s", string(body))
	_, exists = payload["raw_usage"]
	require.False(t, exists, "did not expect raw_usage in marshaled responses payload: %s", string(body))
}
