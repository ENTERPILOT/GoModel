package core

import (
	"encoding/json"
	"testing"
)

func TestUsageUnmarshalJSON_PreservesExtendedFields(t *testing.T) {
	var usage Usage
	if err := json.Unmarshal([]byte(`{
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
	}`), &usage); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	if usage.PromptTokens != 120 {
		t.Fatalf("PromptTokens = %d, want 120", usage.PromptTokens)
	}
	if usage.PromptTokensDetails == nil || usage.PromptTokensDetails.CachedTokens != 80 {
		t.Fatalf("PromptTokensDetails = %+v, want cached_tokens=80", usage.PromptTokensDetails)
	}
	if usage.CompletionTokensDetails == nil || usage.CompletionTokensDetails.ReasoningTokens != 12 {
		t.Fatalf("CompletionTokensDetails = %+v, want reasoning_tokens=12", usage.CompletionTokensDetails)
	}
	if usage.RawUsage["cost_in_usd_ticks"] != float64(969250) {
		t.Fatalf("RawUsage[cost_in_usd_ticks] = %#v, want 969250", usage.RawUsage["cost_in_usd_ticks"])
	}
	if usage.RawUsage["num_sources_used"] != float64(2) {
		t.Fatalf("RawUsage[num_sources_used] = %#v, want 2", usage.RawUsage["num_sources_used"])
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
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if payload["cache_read_input_tokens"] != float64(140) {
		t.Fatalf("cache_read_input_tokens = %#v, want 140", payload["cache_read_input_tokens"])
	}
	if payload["service_tier"] != "priority" {
		t.Fatalf("service_tier = %#v, want priority", payload["service_tier"])
	}
	if _, exists := payload["raw_usage"]; exists {
		t.Fatalf("did not expect raw_usage field in marshaled usage payload: %s", string(body))
	}
}

func TestResponsesUsageUnmarshalJSON_AcceptsResponsesDetailFieldNames(t *testing.T) {
	var usage ResponsesUsage
	if err := json.Unmarshal([]byte(`{
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
	}`), &usage); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	if usage.InputTokens != 125 {
		t.Fatalf("InputTokens = %d, want 125", usage.InputTokens)
	}
	if usage.PromptTokensDetails == nil || usage.PromptTokensDetails.CachedTokens != 98 {
		t.Fatalf("PromptTokensDetails = %+v, want cached_tokens=98", usage.PromptTokensDetails)
	}
	if usage.CompletionTokensDetails == nil || usage.CompletionTokensDetails.ReasoningTokens != 7 {
		t.Fatalf("CompletionTokensDetails = %+v, want reasoning_tokens=7", usage.CompletionTokensDetails)
	}
	if usage.RawUsage["cost_in_usd_ticks"] != float64(158500) {
		t.Fatalf("RawUsage[cost_in_usd_ticks] = %#v, want 158500", usage.RawUsage["cost_in_usd_ticks"])
	}
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
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if _, exists := payload["prompt_tokens_details"]; exists {
		t.Fatalf("did not expect prompt_tokens_details in marshaled responses payload: %s", string(body))
	}
	if _, exists := payload["completion_tokens_details"]; exists {
		t.Fatalf("did not expect completion_tokens_details in marshaled responses payload: %s", string(body))
	}

	inputDetails, ok := payload["input_tokens_details"].(map[string]any)
	if !ok {
		t.Fatalf("input_tokens_details = %#v, want object", payload["input_tokens_details"])
	}
	if inputDetails["cached_tokens"] != float64(98) {
		t.Fatalf("input_tokens_details.cached_tokens = %#v, want 98", inputDetails["cached_tokens"])
	}

	outputDetails, ok := payload["output_tokens_details"].(map[string]any)
	if !ok {
		t.Fatalf("output_tokens_details = %#v, want object", payload["output_tokens_details"])
	}
	if outputDetails["reasoning_tokens"] != float64(7) {
		t.Fatalf("output_tokens_details.reasoning_tokens = %#v, want 7", outputDetails["reasoning_tokens"])
	}
	if payload["cache_read_input_tokens"] != float64(98) {
		t.Fatalf("cache_read_input_tokens = %#v, want 98", payload["cache_read_input_tokens"])
	}
}
