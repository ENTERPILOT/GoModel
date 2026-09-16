package usage

import (
	"encoding/json"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExtractFromChatResponse(t *testing.T) {
	tests := []struct {
		name         string
		resp         *core.ChatResponse
		requestID    string
		provider     string
		endpoint     string
		wantNil      bool
		wantInput    int
		wantOutput   int
		wantTotal    int
		wantRawData  bool
		wantProvider string
		wantModel    string
	}{
		{
			name:     "nil response",
			resp:     nil,
			provider: "openai",
			wantNil:  true,
		},
		{
			name: "basic response",
			resp: &core.ChatResponse{
				ID:    "chatcmpl-123",
				Model: "gpt-4",
				Usage: core.Usage{
					PromptTokens:     100,
					CompletionTokens: 50,
					TotalTokens:      150,
				},
			},
			requestID:    "req-123",
			provider:     "openai",
			endpoint:     "/v1/chat/completions",
			wantInput:    100,
			wantOutput:   50,
			wantTotal:    150,
			wantProvider: "openai",
			wantModel:    "gpt-4",
		},
		{
			name: "response with raw usage",
			resp: &core.ChatResponse{
				ID:    "chatcmpl-456",
				Model: "gpt-4o",
				Usage: core.Usage{
					PromptTokens:     200,
					CompletionTokens: 100,
					TotalTokens:      300,
					RawUsage: map[string]any{
						"cached_tokens":    50,
						"reasoning_tokens": 25,
					},
				},
			},
			requestID:    "req-456",
			provider:     "openai",
			endpoint:     "/v1/chat/completions",
			wantInput:    200,
			wantOutput:   100,
			wantTotal:    300,
			wantRawData:  true,
			wantProvider: "openai",
			wantModel:    "gpt-4o",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entry := ExtractFromChatResponse(tt.resp, tt.requestID, tt.provider, tt.endpoint)

			if tt.wantNil {
				assert.Nil(t, entry)
				return
			}

			require.NotNil(t, entry)
			assert.Equal(t, tt.wantInput, entry.InputTokens)
			assert.Equal(t, tt.wantOutput, entry.OutputTokens)
			assert.Equal(t, tt.wantTotal, entry.TotalTokens)
			assert.Equal(t, tt.wantProvider, entry.Provider)
			assert.Equal(t, tt.wantModel, entry.Model)
			assert.Equal(t, tt.requestID, entry.RequestID)
			assert.Equal(t, tt.endpoint, entry.Endpoint)

			if tt.wantRawData {
				assert.NotNil(t, entry.RawData)
			} else {
				assert.Nil(t, entry.RawData)
			}
		})
	}
}

func TestExtractFromResponsesResponse(t *testing.T) {
	tests := []struct {
		name       string
		resp       *core.ResponsesResponse
		requestID  string
		provider   string
		endpoint   string
		wantNil    bool
		wantInput  int
		wantOutput int
		wantTotal  int
	}{
		{
			name:     "nil response",
			resp:     nil,
			provider: "openai",
			wantNil:  true,
		},
		{
			name: "response with nil usage",
			resp: &core.ResponsesResponse{
				ID:    "resp-123",
				Model: "gpt-4",
				Usage: nil,
			},
			requestID:  "req-123",
			provider:   "openai",
			endpoint:   "/v1/responses",
			wantInput:  0,
			wantOutput: 0,
			wantTotal:  0,
		},
		{
			name: "response with usage",
			resp: &core.ResponsesResponse{
				ID:    "resp-456",
				Model: "gpt-4",
				Usage: &core.ResponsesUsage{
					InputTokens:  100,
					OutputTokens: 50,
					TotalTokens:  150,
				},
			},
			requestID:  "req-456",
			provider:   "openai",
			endpoint:   "/v1/responses",
			wantInput:  100,
			wantOutput: 50,
			wantTotal:  150,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entry := ExtractFromResponsesResponse(tt.resp, tt.requestID, tt.provider, tt.endpoint)

			if tt.wantNil {
				assert.Nil(t, entry)
				return
			}

			require.NotNil(t, entry)
			assert.Equal(t, tt.wantInput, entry.InputTokens)
			assert.Equal(t, tt.wantOutput, entry.OutputTokens)
			assert.Equal(t, tt.wantTotal, entry.TotalTokens)
		})
	}
}

func TestExtractFromChatResponse_WithPromptTokensDetails(t *testing.T) {
	resp := &core.ChatResponse{
		ID:    "chatcmpl-details",
		Model: "gpt-4o",
		Usage: core.Usage{
			PromptTokens:     200,
			CompletionTokens: 100,
			TotalTokens:      300,
			PromptTokensDetails: &core.PromptTokensDetails{
				CachedTokens: 150,
			},
		},
	}

	entry := ExtractFromChatResponse(resp, "req-details", "openai", "/v1/chat/completions")
	require.NotNil(t, entry)
	require.NotNil(t, entry.RawData)
	assert.Equal(t, 150, entry.RawData["prompt_cached_tokens"])
}

func TestExtractFromChatResponse_WithCompletionTokensDetails(t *testing.T) {
	resp := &core.ChatResponse{
		ID:    "chatcmpl-reasoning",
		Model: "o1-preview",
		Usage: core.Usage{
			PromptTokens:     100,
			CompletionTokens: 200,
			TotalTokens:      300,
			CompletionTokensDetails: &core.CompletionTokensDetails{
				ReasoningTokens: 64,
			},
		},
	}

	entry := ExtractFromChatResponse(resp, "req-reason", "openai", "/v1/chat/completions")
	require.NotNil(t, entry)
	require.NotNil(t, entry.RawData)
	assert.Equal(t, 64, entry.RawData["completion_reasoning_tokens"])
}

func TestExtractFromChatResponse_ZeroDetails(t *testing.T) {
	resp := &core.ChatResponse{
		ID:    "chatcmpl-zero",
		Model: "gpt-4",
		Usage: core.Usage{
			PromptTokens:     100,
			CompletionTokens: 50,
			TotalTokens:      150,
			PromptTokensDetails: &core.PromptTokensDetails{
				CachedTokens: 0,
			},
			CompletionTokensDetails: &core.CompletionTokensDetails{
				ReasoningTokens: 0,
			},
		},
	}

	entry := ExtractFromChatResponse(resp, "req-zero", "openai", "/v1/chat/completions")
	require.NotNil(t, entry)
	assert.Nil(t, entry.RawData)
}

func TestExtractFromChatResponse_RawUsageTakesPrecedenceOverDetails(t *testing.T) {
	resp := &core.ChatResponse{
		ID:    "chatcmpl-precedence",
		Model: "gpt-4o",
		Usage: core.Usage{
			PromptTokens:     200,
			CompletionTokens: 100,
			TotalTokens:      300,
			PromptTokensDetails: &core.PromptTokensDetails{
				CachedTokens: 150,
			},
			RawUsage: map[string]any{
				"cached_tokens": 99,
			},
		},
	}

	entry := ExtractFromChatResponse(resp, "req-precedence", "openai", "/v1/chat/completions")
	require.NotNil(t, entry)

	// RawUsage should take precedence - details should NOT overwrite
	assert.Equal(t, 99, entry.RawData["cached_tokens"])
	assert.Equal(t, 150, entry.RawData["prompt_cached_tokens"])
}

func TestExtractFromResponsesResponse_WithDetails(t *testing.T) {
	resp := &core.ResponsesResponse{
		ID:    "resp-details",
		Model: "gpt-4o",
		Usage: &core.ResponsesUsage{
			InputTokens:  200,
			OutputTokens: 100,
			TotalTokens:  300,
			PromptTokensDetails: &core.PromptTokensDetails{
				CachedTokens: 80,
			},
			CompletionTokensDetails: &core.CompletionTokensDetails{
				ReasoningTokens: 30,
			},
		},
	}

	entry := ExtractFromResponsesResponse(resp, "req-resp-details", "openai", "/v1/responses")
	require.NotNil(t, entry)
	require.NotNil(t, entry.RawData)
	assert.Equal(t, 80, entry.RawData["prompt_cached_tokens"])
	assert.Equal(t, 30, entry.RawData["completion_reasoning_tokens"])
}

func TestExtractFromChatResponse_WithPricing(t *testing.T) {
	pricing := &core.ModelPricing{
		InputPerMtok:  new(3.0),  // $3 per million input tokens
		OutputPerMtok: new(15.0), // $15 per million output tokens
	}

	resp := &core.ChatResponse{
		ID:    "chatcmpl-priced",
		Model: "gpt-4o",
		Usage: core.Usage{
			PromptTokens:     1000,
			CompletionTokens: 500,
			TotalTokens:      1500,
		},
	}

	entry := ExtractFromChatResponse(resp, "req-priced", "openai", "/v1/chat/completions", pricing)
	require.NotNil(t, entry)
	require.NotNil(t, entry.InputCost)
	require.NotNil(t, entry.OutputCost)
	require.NotNil(t, entry.TotalCost)

	// 1000 tokens / 1M * $3 = $0.003
	wantInput := 1000.0 / 1_000_000.0 * 3.0
	assert.Equal(t, wantInput, *entry.InputCost)

	// 500 tokens / 1M * $15 = $0.0075
	wantOutput := 500.0 / 1_000_000.0 * 15.0
	assert.Equal(t, wantOutput, *entry.OutputCost)

	wantTotal := wantInput + wantOutput
	assert.Equal(t, wantTotal, *entry.TotalCost)
	assert.Equal(t, CostSourceModelPricing, entry.CostSource)
}

func TestExtractFromResponsesResponse_WithPricing(t *testing.T) {
	pricing := &core.ModelPricing{
		InputPerMtok:  new(2.5),
		OutputPerMtok: new(10.0),
	}

	resp := &core.ResponsesResponse{
		ID:    "resp-priced",
		Model: "gpt-4o",
		Usage: &core.ResponsesUsage{
			InputTokens:  2000,
			OutputTokens: 800,
			TotalTokens:  2800,
		},
	}

	entry := ExtractFromResponsesResponse(resp, "req-resp-priced", "openai", "/v1/responses", pricing)
	require.NotNil(t, entry)
	require.NotNil(t, entry.InputCost)
	require.NotNil(t, entry.OutputCost)
	require.NotNil(t, entry.TotalCost)

	wantInput := 2000.0 / 1_000_000.0 * 2.5
	assert.Equal(t, wantInput, *entry.InputCost)

	wantOutput := 800.0 / 1_000_000.0 * 10.0
	assert.Equal(t, wantOutput, *entry.OutputCost)

	wantTotal := wantInput + wantOutput
	assert.Equal(t, wantTotal, *entry.TotalCost)
	assert.Equal(t, CostSourceModelPricing, entry.CostSource)
}

func TestExtractFromChatResponse_OpenRouterCreditCostWithoutStaticPricing(t *testing.T) {
	resp := &core.ChatResponse{
		ID:    "gen-openrouter",
		Model: "openai/gpt-4o",
		Usage: core.Usage{
			PromptTokens:     10,
			CompletionTokens: 4,
			TotalTokens:      14,
			RawUsage: map[string]any{
				"cost": 0.00014,
			},
		},
	}

	entry := ExtractFromChatResponse(resp, "req-openrouter", "openrouter", "/v1/chat/completions")
	require.NotNil(t, entry)
	require.Nil(t, entry.InputCost)
	require.Nil(t, entry.OutputCost)
	require.NotNil(t, entry.TotalCost)
	require.Equal(t, 0.00014, *entry.TotalCost)
	require.Equal(t, CostSourceOpenRouterCredits, entry.CostSource)
}

func TestExtractFromChatResponse_XAITicksWithoutStaticPricing(t *testing.T) {
	resp := &core.ChatResponse{
		ID:    "chatcmpl-xai",
		Model: "grok-4.3",
		Usage: core.Usage{
			PromptTokens:     199,
			CompletionTokens: 1,
			TotalTokens:      200,
			RawUsage: map[string]any{
				"cost_in_usd_ticks": float64(158_500),
			},
		},
	}

	entry := ExtractFromChatResponse(resp, "req-xai", "xai", "/v1/chat/completions")
	require.NotNil(t, entry)
	require.Nil(t, entry.InputCost)
	require.Nil(t, entry.OutputCost)

	assertCostNear(t, "TotalCost", entry.TotalCost, 0.00001585)
	require.Equal(t, CostSourceXAITicks, entry.CostSource)
}

func TestExtractFromResponsesResponse_XAITicksWithoutStaticPricing(t *testing.T) {
	resp := &core.ResponsesResponse{
		ID:    "resp-xai",
		Model: "grok-4.3",
		Usage: &core.ResponsesUsage{
			InputTokens:  199,
			OutputTokens: 1,
			TotalTokens:  200,
			RawUsage: map[string]any{
				"cost_in_usd_ticks": float64(158_500),
			},
		},
	}

	entry := ExtractFromResponsesResponse(resp, "req-xai-response", "xai", "/v1/responses")
	require.NotNil(t, entry)

	assertCostNear(t, "TotalCost", entry.TotalCost, 0.00001585)
	require.Equal(t, CostSourceXAITicks, entry.CostSource)
}

func TestExtractFromSSEUsage(t *testing.T) {
	entry := ExtractFromSSEUsage(
		"chatcmpl-789",
		100, 50, 150,
		map[string]any{"cached_tokens": 25},
		"req-789", "gpt-4", "openai", "/v1/chat/completions",
	)

	require.NotNil(t, entry)
	assert.Equal(t, "chatcmpl-789", entry.ProviderID)
	assert.Equal(t, 100, entry.InputTokens)
	assert.Equal(t, 50, entry.OutputTokens)
	assert.Equal(t, 150, entry.TotalTokens)
	require.NotNil(t, entry.RawData)
	assert.Equal(t, 25, entry.RawData["cached_tokens"])
}

func TestExtractFromSSEUsageEmptyRawData(t *testing.T) {
	entry := ExtractFromSSEUsage(
		"chatcmpl-789",
		100, 50, 150,
		nil, // empty raw data
		"req-789", "gpt-4", "openai", "/v1/chat/completions",
	)

	require.NotNil(t, entry)
	assert.Nil(t, entry.RawData)
}

func TestExtractFromCachedResponseBody(t *testing.T) {
	t.Run("parses and overrides metadata", func(t *testing.T) {
		resp := &core.ChatResponse{
			ID:    "chatcmpl-cache",
			Model: "gpt-4o-body",
			Usage: core.Usage{
				PromptTokens:     42,
				CompletionTokens: 18,
				TotalTokens:      60,
			},
		}
		body, err := json.Marshal(resp)
		require.NoError(t, err)

		entry := ExtractFromCachedResponseBody(body, "req-cache", "gpt-4o", "openai", "/v1/chat/completions", CacheTypeExact)
		require.NotNil(t, entry)
		require.Equal(t, CacheTypeExact, entry.CacheType)
		require.Equal(t, "req-cache", entry.RequestID)
		require.Equal(t, "openai", entry.Provider)
		require.Equal(t, "/v1/chat/completions", entry.Endpoint)
		require.Equal(t, "gpt-4o", entry.Model)
		require.Equal(t, 42, entry.InputTokens)
		require.Equal(t, 18, entry.OutputTokens)
		require.Equal(t, 60, entry.TotalTokens, "unexpected token counts: %+v", entry)
	})

	t.Run("normalizes equivalent endpoint paths", func(t *testing.T) {
		resp := &core.ChatResponse{
			ID:    "chatcmpl-cache",
			Model: "gpt-4o-body",
			Usage: core.Usage{
				PromptTokens:     7,
				CompletionTokens: 3,
				TotalTokens:      10,
			},
		}
		body, err := json.Marshal(resp)
		require.NoError(t, err)

		entry := ExtractFromCachedResponseBody(body, "req-cache", "gpt-4o", "openai", "/v1/chat/completions/", CacheTypeExact)
		require.NotNil(t, entry)
		require.Equal(t, "/v1/chat/completions", entry.Endpoint)
		require.Equal(t, 10, entry.TotalTokens)
	})

	t.Run("falls back to synthetic entry when body cannot be parsed", func(t *testing.T) {
		entry := ExtractFromCachedResponseBody([]byte("{"), "req-cache-fallback", "gpt-4o", "openai", "/v1/chat/completions", CacheTypeExact)
		require.NotNil(t, entry)
		require.Equal(t, "req-cache-fallback", entry.RequestID)
		require.Equal(t, "openai", entry.Provider)
		require.Equal(t, "/v1/chat/completions", entry.Endpoint)
		require.Equal(t, "gpt-4o", entry.Model)
		require.Equal(t, 0, entry.InputTokens)
		require.Equal(t, 0, entry.OutputTokens)
		require.Equal(t, 0, entry.TotalTokens, "expected zero-token synthetic entry, got %+v", entry)
	})

	t.Run("parses cached chat SSE bodies", func(t *testing.T) {
		body := []byte(
			"data: {\"id\":\"chatcmpl-cache-sse\",\"object\":\"chat.completion.chunk\",\"model\":\"gpt-4o\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Hi\"},\"finish_reason\":null}]}\n\n" +
				"data: {\"id\":\"chatcmpl-cache-sse\",\"object\":\"chat.completion.chunk\",\"model\":\"gpt-4o\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":9,\"completion_tokens\":4,\"total_tokens\":13}}\n\n" +
				"data: [DONE]\n\n",
		)

		entry := ExtractFromCachedResponseBody(body, "req-cache-sse", "gpt-4o", "openai", "/v1/chat/completions", CacheTypeExact)
		require.NotNil(t, entry)
		require.Equal(t, "chatcmpl-cache-sse", entry.ProviderID)
		require.Equal(t, 9, entry.InputTokens)
		require.Equal(t, 4, entry.OutputTokens)
		require.Equal(t, 13, entry.TotalTokens, "unexpected token counts: %+v", entry)
	})

	t.Run("parses cached responses SSE bodies", func(t *testing.T) {
		body := []byte(
			"event: response.created\n" +
				"data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp-cache-sse\",\"object\":\"response\",\"status\":\"in_progress\",\"model\":\"gpt-5\",\"output\":[]}}\n\n" +
				"event: response.completed\n" +
				"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-cache-sse\",\"object\":\"response\",\"status\":\"completed\",\"model\":\"gpt-5\",\"output\":[],\"usage\":{\"input_tokens\":15,\"output_tokens\":8,\"total_tokens\":23}}}\n\n" +
				"data: [DONE]\n\n",
		)

		entry := ExtractFromCachedResponseBody(body, "req-resp-sse", "gpt-5", "openai", "/v1/responses", CacheTypeExact)
		require.NotNil(t, entry)
		require.Equal(t, "resp-cache-sse", entry.ProviderID)
		require.Equal(t, 15, entry.InputTokens)
		require.Equal(t, 8, entry.OutputTokens)
		require.Equal(t, 23, entry.TotalTokens, "unexpected token counts: %+v", entry)
	})

	t.Run("defaults unknown cache type to exact", func(t *testing.T) {
		resp := &core.ChatResponse{
			ID:    "chatcmpl-cache",
			Model: "gpt-4o-body",
			Usage: core.Usage{
				PromptTokens:     2,
				CompletionTokens: 1,
				TotalTokens:      3,
			},
		}
		body, err := json.Marshal(resp)
		require.NoError(t, err)

		entry := ExtractFromCachedResponseBody(body, "req-cache", "gpt-4o", "openai", "/v1/chat/completions", "unknown")
		require.NotNil(t, entry)
		require.Equal(t, CacheTypeExact, entry.CacheType)
	})
}

func TestExtractFromChatResponse_WithBatchPricingEndpoint(t *testing.T) {
	pricing := &core.ModelPricing{
		InputPerMtok:       new(4.0),
		OutputPerMtok:      new(8.0),
		BatchInputPerMtok:  new(1.0),
		BatchOutputPerMtok: new(2.0),
	}

	resp := &core.ChatResponse{
		ID:    "chatcmpl-batch-priced",
		Model: "gpt-4o",
		Usage: core.Usage{
			PromptTokens:     1_000_000,
			CompletionTokens: 500_000,
			TotalTokens:      1_500_000,
		},
	}

	entry := ExtractFromChatResponse(resp, "req-batch-priced", "openai", "/v1/batches", pricing)
	require.NotNil(t, entry)
	require.NotNil(t, entry.InputCost)
	require.NotNil(t, entry.OutputCost)
	require.NotNil(t, entry.TotalCost)
	assert.InDelta(t, 1.0, *entry.InputCost, 1e-9)
	assert.InDelta(t, 1.0, *entry.OutputCost, 1e-9)
	assert.InDelta(t, 2.0, *entry.TotalCost, 1e-9)
}

func TestExtractFromChatResponse_BatchPricingIgnoresStandardTiers(t *testing.T) {
	pricing := &core.ModelPricing{
		InputPerMtok:       new(4.0),
		OutputPerMtok:      new(8.0),
		BatchInputPerMtok:  new(1.0),
		BatchOutputPerMtok: new(2.0),
		Tiers: []core.ModelPricingTier{
			{UpToTokens: new(200_000.0), InputPerMtok: new(4.0), OutputPerMtok: new(8.0)},
			{UpToTokens: new(1_048_576.0), InputPerMtok: new(40.0), OutputPerMtok: new(80.0)},
		},
	}
	resp := &core.ChatResponse{
		ID:    "chatcmpl-batch-tiered",
		Model: "gpt-4o",
		Usage: core.Usage{
			PromptTokens:     250_000,
			CompletionTokens: 10_000,
			TotalTokens:      260_000,
		},
	}

	entry := ExtractFromChatResponse(resp, "req-batch-tiered", "openai", "/v1/batches", pricing)
	require.NotNil(t, entry)
	assert.InDelta(t, 0.25, *entry.InputCost, 1e-9)
	assert.InDelta(t, 0.02, *entry.OutputCost, 1e-9)
}

func TestExtractFromChatResponse_PartialBatchPricingPreservesOtherSideTiers(t *testing.T) {
	tests := []struct {
		name       string
		pricing    *core.ModelPricing
		wantInput  float64
		wantOutput float64
	}{
		{
			name: "batch input preserves output tier",
			pricing: &core.ModelPricing{
				InputPerMtok:      new(4.0),
				OutputPerMtok:     new(8.0),
				BatchInputPerMtok: new(1.0),
				Tiers: []core.ModelPricingTier{
					{UpToTokens: new(200_000.0), InputPerMtok: new(4.0), OutputPerMtok: new(8.0)},
					{UpToTokens: new(1_048_576.0), InputPerMtok: new(40.0), OutputPerMtok: new(80.0)},
				},
			},
			wantInput:  0.25,
			wantOutput: 0.8,
		},
		{
			name: "batch output preserves input tier",
			pricing: &core.ModelPricing{
				InputPerMtok:       new(4.0),
				OutputPerMtok:      new(8.0),
				BatchOutputPerMtok: new(2.0),
				Tiers: []core.ModelPricingTier{
					{UpToTokens: new(200_000.0), InputPerMtok: new(4.0), OutputPerMtok: new(8.0)},
					{UpToTokens: new(1_048_576.0), InputPerMtok: new(40.0), OutputPerMtok: new(80.0)},
				},
			},
			wantInput:  10.0,
			wantOutput: 0.02,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := &core.ChatResponse{
				ID:    "chatcmpl-batch-partial-tiered",
				Model: "gpt-4o",
				Usage: core.Usage{
					PromptTokens:     250_000,
					CompletionTokens: 10_000,
					TotalTokens:      260_000,
				},
			}

			entry := ExtractFromChatResponse(resp, "req-batch-partial-tiered", "openai", "/v1/batches", tt.pricing)
			require.NotNil(t, entry)
			require.NotNil(t, entry.InputCost)
			require.NotNil(t, entry.OutputCost)
			assert.InDelta(t, tt.wantInput, *entry.InputCost, 1e-9)
			assert.InDelta(t, tt.wantOutput, *entry.OutputCost, 1e-9)
		})
	}
}

func TestExtractFromChatResponse_WithBatchPricingSubpathEndpoint(t *testing.T) {
	pricing := &core.ModelPricing{
		InputPerMtok:       new(4.0),
		OutputPerMtok:      new(8.0),
		BatchInputPerMtok:  new(1.0),
		BatchOutputPerMtok: new(2.0),
	}

	resp := &core.ChatResponse{
		ID:    "chatcmpl-batch-subpath-priced",
		Model: "gpt-4o",
		Usage: core.Usage{
			PromptTokens:     1_000_000,
			CompletionTokens: 500_000,
			TotalTokens:      1_500_000,
		},
	}

	entry := ExtractFromChatResponse(resp, "req-batch-subpath-priced", "openai", "/v1/batches/batch_123", pricing)
	require.NotNil(t, entry)
	require.NotNil(t, entry.InputCost)
	require.NotNil(t, entry.OutputCost)
	require.NotNil(t, entry.TotalCost)
	assert.InDelta(t, 1.0, *entry.InputCost, 1e-9)
	assert.InDelta(t, 1.0, *entry.OutputCost, 1e-9)
	assert.InDelta(t, 2.0, *entry.TotalCost, 1e-9)
}

func TestExtractFromEmbeddingResponse_WithBatchPricingEndpoint(t *testing.T) {
	pricing := &core.ModelPricing{
		InputPerMtok:      new(3.0),
		BatchInputPerMtok: new(1.5),
	}

	resp := &core.EmbeddingResponse{
		Object: "list",
		Model:  "text-embedding-3-small",
		Usage: core.EmbeddingUsage{
			PromptTokens: 1_000_000,
			TotalTokens:  1_000_000,
		},
	}

	entry := ExtractFromEmbeddingResponse(resp, "req-embed-batch", "openai", "/v1/batches", pricing)
	require.NotNil(t, entry)
	require.NotNil(t, entry.InputCost)
	assert.InDelta(t, 1.5, *entry.InputCost, 1e-9)
}

func TestExtractFromChatResponse_BatchPrefixOvermatchUsesStandardPricing(t *testing.T) {
	pricing := &core.ModelPricing{
		InputPerMtok:       new(4.0),
		OutputPerMtok:      new(8.0),
		BatchInputPerMtok:  new(1.0),
		BatchOutputPerMtok: new(2.0),
	}

	resp := &core.ChatResponse{
		ID:    "chatcmpl-standard-priced",
		Model: "gpt-4o",
		Usage: core.Usage{
			PromptTokens:     1_000_000,
			CompletionTokens: 500_000,
			TotalTokens:      1_500_000,
		},
	}

	entry := ExtractFromChatResponse(resp, "req-standard-priced", "openai", "/v1/batcheship", pricing)
	require.NotNil(t, entry)
	require.NotNil(t, entry.InputCost)
	require.NotNil(t, entry.OutputCost)
	require.NotNil(t, entry.TotalCost)
	assert.InDelta(t, 4.0, *entry.InputCost, 1e-9)
	assert.InDelta(t, 4.0, *entry.OutputCost, 1e-9)
	assert.InDelta(t, 8.0, *entry.TotalCost, 1e-9)
}

func TestExtractFromEmbeddingResponse_NoUsageCaveat(t *testing.T) {
	pricing := &core.ModelPricing{InputPerMtok: new(0.15)}

	zero := ExtractFromEmbeddingResponse(&core.EmbeddingResponse{Model: "gemini-embedding-001"}, "req", "gemini", "/v1/embeddings", pricing)
	require.NotEmpty(t, zero.CostsCalculationCaveat)

	counted := ExtractFromEmbeddingResponse(&core.EmbeddingResponse{
		Model: "text-embedding-3-small",
		Usage: core.EmbeddingUsage{PromptTokens: 8, TotalTokens: 8},
	}, "req", "openai", "/v1/embeddings", pricing)
	require.Empty(t, counted.CostsCalculationCaveat)
	require.NotNil(t, counted.TotalCost)

	// Pricing that does not depend on token counts determines the cost even
	// with nothing reported, so the row must not claim it was uncalculated.
	determined := map[string]*core.ModelPricing{
		"per request":        {PerRequest: new(0.01)},
		"explicit zero rate": {InputPerMtok: new(0.0)},
	}
	for name, priced := range determined {
		t.Run(name, func(t *testing.T) {
			entry := ExtractFromEmbeddingResponse(&core.EmbeddingResponse{Model: "free-embed"}, "req", "openai", "/v1/embeddings", priced)
			require.Empty(t, entry.CostsCalculationCaveat)
			require.NotNil(t, entry.TotalCost)
		})
	}

	// A tier is selected by input token count, so a zero-token row is priced by
	// the base rate alone — here $0. Reported usage is exactly what would have
	// selected the priced tier, so the row is understated and keeps the caveat.
	tiered := &core.ModelPricing{
		InputPerMtok: new(0.0),
		Tiers: []core.ModelPricingTier{
			{UpToTokens: new(200_000.0), InputPerMtok: new(0.15), OutputPerMtok: new(0.6)},
			{UpToTokens: new(10_000_000.0), InputPerMtok: new(0.3), OutputPerMtok: new(1.2)},
		},
	}
	unreported := ExtractFromEmbeddingResponse(&core.EmbeddingResponse{Model: "tiered-embed"}, "req", "openai", "/v1/embeddings", tiered)
	require.Equal(t, caveatEmbeddingMissingUsage, unreported.CostsCalculationCaveat)
	retained := retainedMissingUsageCaveat(caveatEmbeddingMissingUsage, 0, nil, tiered)
	require.Equal(t, caveatEmbeddingMissingUsage, retained)
}

// TestExtractFromChatResponse_EdenAIExactCostReachesTotalCost closes the seam
// between the Eden provider and this package. The provider lifts Eden's
// root-level cost into Usage.RawUsage; this asserts the extractor carries it
// into rawData and that the entry ends up priced from it, so Eden spend
// reaches usage records, budgets, and cost reporting.
func TestExtractFromChatResponse_EdenAIExactCostReachesTotalCost(t *testing.T) {
	resp := &core.ChatResponse{
		ID:    "chatcmpl-eden",
		Model: "gpt-4o-mini-2024-07-18",
		Usage: core.Usage{
			PromptTokens:     1170,
			CompletionTokens: 99,
			TotalTokens:      1269,
			RawUsage:         map[string]any{"cost": 0.0002349},
		},
	}
	// Static pricing that would produce a very different number, to prove the
	// exact charge wins rather than merely agreeing by coincidence.
	pricing := &core.ModelPricing{InputPerMtok: new(100.0), OutputPerMtok: new(100.0)}

	entry := ExtractFromChatResponse(resp, "req-eden", "edenai", "/v1/chat/completions", pricing)

	if entry == nil {
		t.Fatal("ExtractFromChatResponse() = nil")
	}
	if entry.RawData["cost"] != 0.0002349 {
		t.Fatalf("RawData[cost] = %#v, want the lifted 0.0002349", entry.RawData["cost"])
	}
	if entry.TotalCost == nil || math.Abs(*entry.TotalCost-0.0002349) > 1e-12 {
		t.Fatalf("TotalCost = %v, want 0.0002349", entry.TotalCost)
	}
	if entry.CostSource != CostSourceEdenAICost {
		t.Fatalf("CostSource = %q, want %q", entry.CostSource, CostSourceEdenAICost)
	}
	if entry.InputTokens != 1170 || entry.OutputTokens != 99 {
		t.Errorf("token counts = %d/%d, want 1170/99", entry.InputTokens, entry.OutputTokens)
	}
}

// TestExtractFromChatResponse_EdenAIWithoutCostFallsBackToPricing asserts the
// fallback path: no exact charge means the discovered per-model pricing is
// used, rather than the entry going uncosted.
func TestExtractFromChatResponse_EdenAIWithoutCostFallsBackToPricing(t *testing.T) {
	resp := &core.ChatResponse{
		ID:    "chatcmpl-eden",
		Model: "gpt-4o-mini",
		Usage: core.Usage{PromptTokens: 1_000_000, CompletionTokens: 500_000, TotalTokens: 1_500_000},
	}
	pricing := &core.ModelPricing{InputPerMtok: new(0.06), OutputPerMtok: new(0.18)}

	entry := ExtractFromChatResponse(resp, "req-eden", "edenai", "/v1/chat/completions", pricing)

	if entry == nil {
		t.Fatal("ExtractFromChatResponse() = nil")
	}
	if entry.CostSource != CostSourceModelPricing {
		t.Fatalf("CostSource = %q, want %q", entry.CostSource, CostSourceModelPricing)
	}
	// 1M * 0.06/1M + 0.5M * 0.18/1M = 0.06 + 0.09
	if entry.TotalCost == nil || math.Abs(*entry.TotalCost-0.15) > 1e-9 {
		t.Fatalf("TotalCost = %v, want 0.15 from discovered per-model pricing", entry.TotalCost)
	}
}

// TestExtractFromEmbeddingResponse_ForwardsRawUsage asserts the provider's
// extra usage members reach the cost pipeline. Without this the embeddings
// path has no channel for a provider-reported exact charge at all, because
// ExtractFromEmbeddingResponse builds the entry itself.
func TestExtractFromEmbeddingResponse_ForwardsRawUsage(t *testing.T) {
	resp := &core.EmbeddingResponse{
		Model: "openai/text-embedding-3-small",
		Usage: core.EmbeddingUsage{
			PromptTokens: 9,
			TotalTokens:  9,
			RawUsage:     map[string]any{"cost": 0.0000012},
		},
	}

	entry := ExtractFromEmbeddingResponse(resp, "req", "edenai", "/v1/embeddings")
	if entry == nil {
		t.Fatal("ExtractFromEmbeddingResponse returned nil")
	}
	if got := entry.RawData["cost"]; got != 0.0000012 {
		t.Errorf("RawData[cost] = %v, want 0.0000012 forwarded from RawUsage", got)
	}
	if entry.TotalCost == nil || *entry.TotalCost != 0.0000012 {
		t.Errorf("TotalCost = %v, want the provider-reported 0.0000012", entry.TotalCost)
	}
	if entry.CostSource != CostSourceEdenAICost {
		t.Errorf("CostSource = %q, want %q", entry.CostSource, CostSourceEdenAICost)
	}
}

// TestExtractFromEmbeddingResponse_NilRawUsageStaysNil asserts a provider that
// reports nothing beyond the token counts is unaffected: RawData stays nil, so
// cost is calculated exactly as it was before embeddings gained the carrier.
func TestExtractFromEmbeddingResponse_NilRawUsageStaysNil(t *testing.T) {
	rate := 0.02
	resp := &core.EmbeddingResponse{
		Model: "text-embedding-3-small",
		Usage: core.EmbeddingUsage{PromptTokens: 1000, TotalTokens: 1000},
	}

	entry := ExtractFromEmbeddingResponse(resp, "req", "openai", "/v1/embeddings",
		&core.ModelPricing{Currency: "USD", InputPerMtok: &rate})
	if entry.RawData != nil {
		t.Errorf("RawData = %v, want nil when the provider reported no extra usage", entry.RawData)
	}
	if entry.TotalCost == nil || *entry.TotalCost != 0.00002 {
		t.Errorf("TotalCost = %v, want 0.00002 from the token rate", entry.TotalCost)
	}
	if entry.CostSource != CostSourceModelPricing {
		t.Errorf("CostSource = %q, want %q", entry.CostSource, CostSourceModelPricing)
	}
}

// TestExtractFromEmbeddingResponse_ExactCostSuppressesMissingUsageCaveat
// asserts a zero-token row is not flagged as uncalculated when the cost came
// from a figure the provider itself reported.
//
// The caveat exists because zero tokens priced from token rates understate the
// call. A provider-reported total is authoritative however many tokens came
// with it, so flagging it would tell operators the cost is unreliable when it
// is the most reliable number available.
func TestExtractFromEmbeddingResponse_ExactCostSuppressesMissingUsageCaveat(t *testing.T) {
	rate := 0.02
	resp := &core.EmbeddingResponse{
		Model: "openai/text-embedding-3-small",
		Usage: core.EmbeddingUsage{RawUsage: map[string]any{"cost": 0.0000012}},
	}

	entry := ExtractFromEmbeddingResponse(resp, "req", "edenai", "/v1/embeddings",
		&core.ModelPricing{Currency: "USD", InputPerMtok: &rate})
	if entry.CostsCalculationCaveat != "" {
		t.Errorf("CostsCalculationCaveat = %q, want empty: the cost was reported by the provider", entry.CostsCalculationCaveat)
	}
	if entry.TotalCost == nil || *entry.TotalCost != 0.0000012 {
		t.Errorf("TotalCost = %v, want the provider-reported 0.0000012", entry.TotalCost)
	}
}

// TestExtractFromEmbeddingResponse_ZeroTokenCaveatStillApplies asserts the
// guard above did not disable the caveat generally: a zero-token row with no
// provider-reported cost is still flagged.
func TestExtractFromEmbeddingResponse_ZeroTokenCaveatStillApplies(t *testing.T) {
	rate := 0.02
	resp := &core.EmbeddingResponse{Model: "gemini-embedding-001"}

	entry := ExtractFromEmbeddingResponse(resp, "req", "gemini", "/v1/embeddings",
		&core.ModelPricing{Currency: "USD", InputPerMtok: &rate})
	if entry.CostsCalculationCaveat == "" {
		t.Error("CostsCalculationCaveat = empty, want the zero-token row flagged")
	}
}

// TestisProviderReportedCostSource pins which cost sources count as figures the
// provider returned rather than rate-card reconstructions.
func TestIsProviderReportedCostSource(t *testing.T) {
	tests := []struct {
		source string
		want   bool
	}{
		{CostSourceOpenRouterCredits, true},
		{CostSourceXAITicks, true},
		{CostSourceEdenAICost, true},
		{"  " + CostSourceEdenAICost + "  ", true},
		{CostSourceModelPricing, false},
		{"", false},
		{"something_else", false},
	}

	for _, tc := range tests {
		if got := isProviderReportedCostSource(tc.source); got != tc.want {
			t.Errorf("isProviderReportedCostSource(%q) = %v, want %v", tc.source, got, tc.want)
		}
	}
}
