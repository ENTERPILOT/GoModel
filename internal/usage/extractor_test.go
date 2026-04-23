package usage

import (
	"encoding/json"
	"math"
	"testing"

	"gomodel/internal/core"
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
				if entry != nil {
					t.Error("expected nil entry")
				}
				return
			}

			if entry == nil {
				t.Fatal("expected non-nil entry")
			}

			if entry.InputTokens != tt.wantInput {
				t.Errorf("InputTokens = %d, want %d", entry.InputTokens, tt.wantInput)
			}
			if entry.OutputTokens != tt.wantOutput {
				t.Errorf("OutputTokens = %d, want %d", entry.OutputTokens, tt.wantOutput)
			}
			if entry.TotalTokens != tt.wantTotal {
				t.Errorf("TotalTokens = %d, want %d", entry.TotalTokens, tt.wantTotal)
			}
			if entry.Provider != tt.wantProvider {
				t.Errorf("Provider = %s, want %s", entry.Provider, tt.wantProvider)
			}
			if entry.Model != tt.wantModel {
				t.Errorf("Model = %s, want %s", entry.Model, tt.wantModel)
			}
			if entry.RequestID != tt.requestID {
				t.Errorf("RequestID = %s, want %s", entry.RequestID, tt.requestID)
			}
			if entry.Endpoint != tt.endpoint {
				t.Errorf("Endpoint = %s, want %s", entry.Endpoint, tt.endpoint)
			}
			if tt.wantRawData && entry.RawData == nil {
				t.Error("expected RawData to be set")
			}
			if !tt.wantRawData && entry.RawData != nil {
				t.Error("expected RawData to be nil")
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
				if entry != nil {
					t.Error("expected nil entry")
				}
				return
			}

			if entry == nil {
				t.Fatal("expected non-nil entry")
			}

			if entry.InputTokens != tt.wantInput {
				t.Errorf("InputTokens = %d, want %d", entry.InputTokens, tt.wantInput)
			}
			if entry.OutputTokens != tt.wantOutput {
				t.Errorf("OutputTokens = %d, want %d", entry.OutputTokens, tt.wantOutput)
			}
			if entry.TotalTokens != tt.wantTotal {
				t.Errorf("TotalTokens = %d, want %d", entry.TotalTokens, tt.wantTotal)
			}
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
	if entry == nil {
		t.Fatal("expected non-nil entry")
	}
	if entry.RawData == nil {
		t.Fatal("expected RawData to be set from PromptTokensDetails")
	}
	if entry.RawData["prompt_cached_tokens"] != 150 {
		t.Errorf("RawData[prompt_cached_tokens] = %v, want 150", entry.RawData["prompt_cached_tokens"])
	}
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
	if entry == nil {
		t.Fatal("expected non-nil entry")
	}
	if entry.RawData == nil {
		t.Fatal("expected RawData to be set from CompletionTokensDetails")
	}
	if entry.RawData["completion_reasoning_tokens"] != 64 {
		t.Errorf("RawData[completion_reasoning_tokens] = %v, want 64", entry.RawData["completion_reasoning_tokens"])
	}
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
	if entry == nil {
		t.Fatal("expected non-nil entry")
	}
	if entry.RawData != nil {
		t.Errorf("expected RawData to be nil for zero-value details, got %v", entry.RawData)
	}
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
	if entry == nil {
		t.Fatal("expected non-nil entry")
	}
	// RawUsage should take precedence - details should NOT overwrite
	if entry.RawData["cached_tokens"] != 99 {
		t.Errorf("RawData[cached_tokens] = %v, want 99 (from RawUsage)", entry.RawData["cached_tokens"])
	}
	if entry.RawData["prompt_cached_tokens"] != 150 {
		t.Errorf("RawData[prompt_cached_tokens] = %v, want 150 (from PromptTokensDetails)", entry.RawData["prompt_cached_tokens"])
	}
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
	if entry == nil {
		t.Fatal("expected non-nil entry")
	}
	if entry.RawData == nil {
		t.Fatal("expected RawData to be set from details")
	}
	if entry.RawData["prompt_cached_tokens"] != 80 {
		t.Errorf("RawData[prompt_cached_tokens] = %v, want 80", entry.RawData["prompt_cached_tokens"])
	}
	if entry.RawData["completion_reasoning_tokens"] != 30 {
		t.Errorf("RawData[completion_reasoning_tokens] = %v, want 30", entry.RawData["completion_reasoning_tokens"])
	}
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
	if entry == nil {
		t.Fatal("expected non-nil entry")
	}
	if entry.InputCost == nil {
		t.Fatal("expected InputCost to be non-nil")
	}
	if entry.OutputCost == nil {
		t.Fatal("expected OutputCost to be non-nil")
	}
	if entry.TotalCost == nil {
		t.Fatal("expected TotalCost to be non-nil")
	}

	// 1000 tokens / 1M * $3 = $0.003
	wantInput := 1000.0 / 1_000_000.0 * 3.0
	if *entry.InputCost != wantInput {
		t.Errorf("InputCost = %f, want %f", *entry.InputCost, wantInput)
	}
	// 500 tokens / 1M * $15 = $0.0075
	wantOutput := 500.0 / 1_000_000.0 * 15.0
	if *entry.OutputCost != wantOutput {
		t.Errorf("OutputCost = %f, want %f", *entry.OutputCost, wantOutput)
	}
	wantTotal := wantInput + wantOutput
	if *entry.TotalCost != wantTotal {
		t.Errorf("TotalCost = %f, want %f", *entry.TotalCost, wantTotal)
	}
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
	if entry == nil {
		t.Fatal("expected non-nil entry")
	}
	if entry.InputCost == nil {
		t.Fatal("expected InputCost to be non-nil")
	}
	if entry.OutputCost == nil {
		t.Fatal("expected OutputCost to be non-nil")
	}
	if entry.TotalCost == nil {
		t.Fatal("expected TotalCost to be non-nil")
	}

	wantInput := 2000.0 / 1_000_000.0 * 2.5
	if *entry.InputCost != wantInput {
		t.Errorf("InputCost = %f, want %f", *entry.InputCost, wantInput)
	}
	wantOutput := 800.0 / 1_000_000.0 * 10.0
	if *entry.OutputCost != wantOutput {
		t.Errorf("OutputCost = %f, want %f", *entry.OutputCost, wantOutput)
	}
	wantTotal := wantInput + wantOutput
	if *entry.TotalCost != wantTotal {
		t.Errorf("TotalCost = %f, want %f", *entry.TotalCost, wantTotal)
	}
}

func TestExtractFromSSEUsage(t *testing.T) {
	entry := ExtractFromSSEUsage(
		"chatcmpl-789",
		100, 50, 150,
		map[string]any{"cached_tokens": 25},
		"req-789", "gpt-4", "openai", "/v1/chat/completions",
	)

	if entry == nil {
		t.Fatal("expected non-nil entry")
	}

	if entry.ProviderID != "chatcmpl-789" {
		t.Errorf("ProviderID = %s, want chatcmpl-789", entry.ProviderID)
	}
	if entry.InputTokens != 100 {
		t.Errorf("InputTokens = %d, want 100", entry.InputTokens)
	}
	if entry.OutputTokens != 50 {
		t.Errorf("OutputTokens = %d, want 50", entry.OutputTokens)
	}
	if entry.TotalTokens != 150 {
		t.Errorf("TotalTokens = %d, want 150", entry.TotalTokens)
	}
	if entry.RawData == nil {
		t.Error("expected RawData to be set")
	}
	if entry.RawData["cached_tokens"] != 25 {
		t.Errorf("RawData[cached_tokens] = %v, want 25", entry.RawData["cached_tokens"])
	}
}

func TestExtractFromSSEUsageEmptyRawData(t *testing.T) {
	entry := ExtractFromSSEUsage(
		"chatcmpl-789",
		100, 50, 150,
		nil, // empty raw data
		"req-789", "gpt-4", "openai", "/v1/chat/completions",
	)

	if entry == nil {
		t.Fatal("expected non-nil entry")
	}

	if entry.RawData != nil {
		t.Error("expected RawData to be nil")
	}
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
		if err != nil {
			t.Fatalf("Marshal: %v", err)
		}

		entry := ExtractFromCachedResponseBody(body, "req-cache", "gpt-4o", "openai", "/v1/chat/completions", CacheTypeExact)
		if entry == nil {
			t.Fatal("expected non-nil entry")
		}
		if entry.CacheType != CacheTypeExact {
			t.Fatalf("CacheType = %q, want %q", entry.CacheType, CacheTypeExact)
		}
		if entry.RequestID != "req-cache" {
			t.Fatalf("RequestID = %q, want %q", entry.RequestID, "req-cache")
		}
		if entry.Provider != "openai" {
			t.Fatalf("Provider = %q, want %q", entry.Provider, "openai")
		}
		if entry.Endpoint != "/v1/chat/completions" {
			t.Fatalf("Endpoint = %q, want %q", entry.Endpoint, "/v1/chat/completions")
		}
		if entry.Model != "gpt-4o" {
			t.Fatalf("Model = %q, want %q", entry.Model, "gpt-4o")
		}
		if entry.InputTokens != 42 || entry.OutputTokens != 18 || entry.TotalTokens != 60 {
			t.Fatalf("unexpected token counts: %+v", entry)
		}
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
		if err != nil {
			t.Fatalf("Marshal: %v", err)
		}

		entry := ExtractFromCachedResponseBody(body, "req-cache", "gpt-4o", "openai", "/v1/chat/completions/", CacheTypeExact)
		if entry == nil {
			t.Fatal("expected non-nil entry")
		}
		if entry.Endpoint != "/v1/chat/completions" {
			t.Fatalf("Endpoint = %q, want %q", entry.Endpoint, "/v1/chat/completions")
		}
		if entry.TotalTokens != 10 {
			t.Fatalf("TotalTokens = %d, want 10", entry.TotalTokens)
		}
	})

	t.Run("falls back to synthetic entry when body cannot be parsed", func(t *testing.T) {
		entry := ExtractFromCachedResponseBody([]byte("{"), "req-cache-fallback", "gpt-4o", "openai", "/v1/chat/completions", CacheTypeExact)
		if entry == nil {
			t.Fatal("expected non-nil entry")
		}
		if entry.RequestID != "req-cache-fallback" {
			t.Fatalf("RequestID = %q, want %q", entry.RequestID, "req-cache-fallback")
		}
		if entry.Provider != "openai" {
			t.Fatalf("Provider = %q, want %q", entry.Provider, "openai")
		}
		if entry.Endpoint != "/v1/chat/completions" {
			t.Fatalf("Endpoint = %q, want %q", entry.Endpoint, "/v1/chat/completions")
		}
		if entry.Model != "gpt-4o" {
			t.Fatalf("Model = %q, want %q", entry.Model, "gpt-4o")
		}
		if entry.InputTokens != 0 || entry.OutputTokens != 0 || entry.TotalTokens != 0 {
			t.Fatalf("expected zero-token synthetic entry, got %+v", entry)
		}
	})

	t.Run("parses cached chat SSE bodies", func(t *testing.T) {
		body := []byte(
			"data: {\"id\":\"chatcmpl-cache-sse\",\"object\":\"chat.completion.chunk\",\"model\":\"gpt-4o\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Hi\"},\"finish_reason\":null}]}\n\n" +
				"data: {\"id\":\"chatcmpl-cache-sse\",\"object\":\"chat.completion.chunk\",\"model\":\"gpt-4o\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":9,\"completion_tokens\":4,\"total_tokens\":13}}\n\n" +
				"data: [DONE]\n\n",
		)

		entry := ExtractFromCachedResponseBody(body, "req-cache-sse", "gpt-4o", "openai", "/v1/chat/completions", CacheTypeExact)
		if entry == nil {
			t.Fatal("expected non-nil entry")
		}
		if entry.ProviderID != "chatcmpl-cache-sse" {
			t.Fatalf("ProviderID = %q, want chatcmpl-cache-sse", entry.ProviderID)
		}
		if entry.InputTokens != 9 || entry.OutputTokens != 4 || entry.TotalTokens != 13 {
			t.Fatalf("unexpected token counts: %+v", entry)
		}
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
		if entry == nil {
			t.Fatal("expected non-nil entry")
		}
		if entry.ProviderID != "resp-cache-sse" {
			t.Fatalf("ProviderID = %q, want resp-cache-sse", entry.ProviderID)
		}
		if entry.InputTokens != 15 || entry.OutputTokens != 8 || entry.TotalTokens != 23 {
			t.Fatalf("unexpected token counts: %+v", entry)
		}
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
		if err != nil {
			t.Fatalf("Marshal: %v", err)
		}

		entry := ExtractFromCachedResponseBody(body, "req-cache", "gpt-4o", "openai", "/v1/chat/completions", "unknown")
		if entry == nil {
			t.Fatal("expected non-nil entry")
		}
		if entry.CacheType != CacheTypeExact {
			t.Fatalf("CacheType = %q, want %q", entry.CacheType, CacheTypeExact)
		}
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
	if entry == nil {
		t.Fatal("expected non-nil entry")
	}
	if entry.InputCost == nil || entry.OutputCost == nil || entry.TotalCost == nil {
		t.Fatal("expected all costs to be populated")
	}

	if math.Abs(*entry.InputCost-1.0) > 1e-9 {
		t.Errorf("InputCost = %f, want 1.0", *entry.InputCost)
	}
	if math.Abs(*entry.OutputCost-1.0) > 1e-9 {
		t.Errorf("OutputCost = %f, want 1.0", *entry.OutputCost)
	}
	if math.Abs(*entry.TotalCost-2.0) > 1e-9 {
		t.Errorf("TotalCost = %f, want 2.0", *entry.TotalCost)
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
	if entry == nil {
		t.Fatal("expected non-nil entry")
	}
	if entry.InputCost == nil || entry.OutputCost == nil || entry.TotalCost == nil {
		t.Fatal("expected all costs to be populated")
	}

	if math.Abs(*entry.InputCost-1.0) > 1e-9 {
		t.Errorf("InputCost = %f, want 1.0", *entry.InputCost)
	}
	if math.Abs(*entry.OutputCost-1.0) > 1e-9 {
		t.Errorf("OutputCost = %f, want 1.0", *entry.OutputCost)
	}
	if math.Abs(*entry.TotalCost-2.0) > 1e-9 {
		t.Errorf("TotalCost = %f, want 2.0", *entry.TotalCost)
	}
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
	if entry == nil {
		t.Fatal("expected non-nil entry")
	}
	if entry.InputCost == nil {
		t.Fatal("expected InputCost to be populated")
	}
	if math.Abs(*entry.InputCost-1.5) > 1e-9 {
		t.Errorf("InputCost = %f, want 1.5", *entry.InputCost)
	}
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
	if entry == nil {
		t.Fatal("expected non-nil entry")
	}
	if entry.InputCost == nil || entry.OutputCost == nil || entry.TotalCost == nil {
		t.Fatal("expected all costs to be populated")
	}
	if math.Abs(*entry.InputCost-4.0) > 1e-9 {
		t.Errorf("InputCost = %f, want 4.0", *entry.InputCost)
	}
	if math.Abs(*entry.OutputCost-4.0) > 1e-9 {
		t.Errorf("OutputCost = %f, want 4.0", *entry.OutputCost)
	}
	if math.Abs(*entry.TotalCost-8.0) > 1e-9 {
		t.Errorf("TotalCost = %f, want 8.0", *entry.TotalCost)
	}
}
