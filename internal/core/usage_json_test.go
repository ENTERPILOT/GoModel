package core

import (
	"encoding/json"
	"testing"

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
