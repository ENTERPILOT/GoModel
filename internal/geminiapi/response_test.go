package geminiapi

import (
	"net/http"
	"testing"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
)

func encode(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	require.NoError(t, err)
	return string(raw)
}

func TestFromChatResponse_Text(t *testing.T) {
	resp := &core.ChatResponse{
		ID:    "resp-1",
		Model: "gemini-test",
		Choices: []core.Choice{{
			Message:      core.ResponseMessage{Role: "assistant", Content: "Paris."},
			FinishReason: "stop",
		}},
		Usage: core.Usage{PromptTokens: 14, CompletionTokens: 2, TotalTokens: 16},
	}

	// The exact shape the Gemini API answers with.
	assert.Equal(t,
		`{"candidates":[{"content":{"parts":[{"text":"Paris."}],"role":"model"},"finishReason":"STOP","index":0}],`+
			`"usageMetadata":{"promptTokenCount":14,"candidatesTokenCount":2,"totalTokenCount":16},`+
			`"modelVersion":"gemini-test","responseId":"resp-1"}`,
		encode(t, FromChatResponse(resp, ResponseOptions{})))
}

func TestFromChatResponse_FunctionCalls(t *testing.T) {
	resp := &core.ChatResponse{
		Choices: []core.Choice{{
			Message: core.ResponseMessage{
				Role: "assistant",
				ToolCalls: []core.ToolCall{
					{ID: "call_a", Type: "function", Function: core.FunctionCall{Name: "get_weather", Arguments: `{"city": "Paris"}`}, ExtraFields: thoughtSignatureFields("sig-1")},
					{ID: "call_b", Type: "function", Function: core.FunctionCall{Name: "now"}},
				},
			},
			FinishReason: "tool_calls",
		}},
	}

	out := FromChatResponse(resp, ResponseOptions{})
	require.Len(t, out.Candidates, 1)
	// Gemini reports a turn that ends in function calls as STOP.
	assert.Equal(t, "STOP", out.Candidates[0].FinishReason)
	assert.Equal(t,
		`[{"functionCall":{"id":"call_a","name":"get_weather","args":{"city":"Paris"}},"thoughtSignature":"sig-1"},`+
			`{"functionCall":{"id":"call_b","name":"now","args":{}}}]`,
		encode(t, out.Candidates[0].Content.Parts))
}

func TestFromChatResponse_Thoughts(t *testing.T) {
	fields, err := core.MergeUnknownJSONFields(thoughtSignatureFields("sig-text"), map[string]json.RawMessage{
		"reasoning_content": json.RawMessage(`"pondering"`),
	})
	require.NoError(t, err)
	resp := &core.ChatResponse{Choices: []core.Choice{{
		Message:      core.ResponseMessage{Role: "assistant", Content: "Hello", ExtraFields: fields},
		FinishReason: "stop",
	}}}

	// Reasoning is returned only when the request asked for it.
	assert.Equal(t, `[{"text":"Hello","thoughtSignature":"sig-text"}]`,
		encode(t, FromChatResponse(resp, ResponseOptions{}).Candidates[0].Content.Parts))
	assert.Equal(t, `[{"text":"pondering","thought":true},{"text":"Hello","thoughtSignature":"sig-text"}]`,
		encode(t, FromChatResponse(resp, ResponseOptions{IncludeThoughts: true}).Candidates[0].Content.Parts))
}

func TestFromChatResponse_EmptyAnswerKeepsATextPart(t *testing.T) {
	resp := &core.ChatResponse{Choices: []core.Choice{{Message: core.ResponseMessage{Role: "assistant"}, FinishReason: "length"}}}

	out := FromChatResponse(resp, ResponseOptions{})
	require.Len(t, out.Candidates, 1)
	assert.Equal(t, "MAX_TOKENS", out.Candidates[0].FinishReason)
	assert.Equal(t, `[{"text":""}]`, encode(t, out.Candidates[0].Content.Parts))
}

func TestFromChatResponse_Nil(t *testing.T) {
	assert.Equal(t, `{"candidates":[]}`, encode(t, FromChatResponse(nil, ResponseOptions{})))
}

func TestFinishReasonFromChat(t *testing.T) {
	tests := map[string]string{
		"":               "STOP",
		"stop":           "STOP",
		"tool_calls":     "STOP",
		"function_call":  "STOP",
		"length":         "MAX_TOKENS",
		"content_filter": "SAFETY",
		"something_else": "OTHER",
	}
	for finish, want := range tests {
		assert.Equal(t, want, finishReasonFromChat(finish), finish)
	}
}

func TestUsageFromCore(t *testing.T) {
	tests := []struct {
		name  string
		usage core.Usage
		want  UsageMetadata
	}{
		{
			name:  "plain",
			usage: core.Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
			want:  UsageMetadata{PromptTokenCount: 10, CandidatesTokenCount: 5, TotalTokenCount: 15},
		},
		{
			// OpenAI counts cached tokens inside prompt_tokens and reasoning
			// inside completion_tokens; Gemini reports thoughts apart.
			name: "openai shape",
			usage: core.Usage{
				PromptTokens: 100, CompletionTokens: 50, TotalTokens: 150,
				PromptTokensDetails:     &core.PromptTokensDetails{CachedTokens: 40},
				CompletionTokensDetails: &core.CompletionTokensDetails{ReasoningTokens: 30},
			},
			want: UsageMetadata{PromptTokenCount: 100, CachedContentTokenCount: 40, CandidatesTokenCount: 20, ThoughtsTokenCount: 30, TotalTokenCount: 150},
		},
		{
			// Anthropic excludes cached tokens from the prompt count; Gemini's
			// promptTokenCount is the whole prompt.
			name: "anthropic shape",
			usage: core.Usage{
				PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15,
				RawUsage: map[string]any{"cache_read_input_tokens": float64(70), "cache_creation_input_tokens": float64(20)},
			},
			want: UsageMetadata{PromptTokenCount: 100, CachedContentTokenCount: 70, CandidatesTokenCount: 5, TotalTokenCount: 105},
		},
		{
			name: "reasoning never exceeds the completion",
			usage: core.Usage{
				PromptTokens: 1, CompletionTokens: 5,
				CompletionTokensDetails: &core.CompletionTokensDetails{ReasoningTokens: 9},
			},
			want: UsageMetadata{PromptTokenCount: 1, ThoughtsTokenCount: 5, TotalTokenCount: 6},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, *usageFromCore(tt.usage))
		})
	}
}

func TestErrorFromGateway(t *testing.T) {
	tests := []struct {
		name   string
		err    *core.GatewayError
		status int
		rpc    string
	}{
		{name: "nil", status: http.StatusInternalServerError, rpc: "INTERNAL"},
		{name: "invalid request", err: core.NewInvalidRequestError("bad", nil), status: http.StatusBadRequest, rpc: "INVALID_ARGUMENT"},
		{name: "too large", err: core.NewInvalidRequestErrorWithStatus(http.StatusRequestEntityTooLarge, "big", nil), status: http.StatusRequestEntityTooLarge, rpc: "INVALID_ARGUMENT"},
		{name: "authentication", err: core.NewAuthenticationError("", "no key"), status: http.StatusUnauthorized, rpc: "UNAUTHENTICATED"},
		{name: "not found", err: core.NewNotFoundError("nope"), status: http.StatusNotFound, rpc: "NOT_FOUND"},
		{name: "rate limit", err: core.NewRateLimitError("openai", "slow down"), status: http.StatusTooManyRequests, rpc: "RESOURCE_EXHAUSTED"},
		{name: "provider", err: core.NewProviderError("openai", http.StatusBadGateway, "upstream", nil), status: http.StatusBadGateway, rpc: "INTERNAL"},
		{name: "unavailable", err: core.NewProviderError("openai", http.StatusServiceUnavailable, "down", nil), status: http.StatusServiceUnavailable, rpc: "UNAVAILABLE"},
		{name: "timeout", err: core.NewProviderError("openai", http.StatusGatewayTimeout, "slow", nil), status: http.StatusGatewayTimeout, rpc: "DEADLINE_EXCEEDED"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, body := ErrorFromGateway(tt.err)
			assert.Equal(t, tt.status, status)
			assert.Equal(t, tt.status, body.Error.Code)
			assert.Equal(t, tt.rpc, body.Error.Status)
			assert.NotEmpty(t, body.Error.Message)
		})
	}
}

func TestFromModels(t *testing.T) {
	window, output := 1048576, 65536
	models := []core.Model{
		{ID: "gemini/gemini-2.5-flash", Metadata: &core.ModelMetadata{
			DisplayName: "Gemini 2.5 Flash", Description: "Fast", Modes: []string{"chat"},
			ContextWindow: &window, MaxOutputTokens: &output,
		}},
		{ID: "openai/text-embedding-3-small", Metadata: &core.ModelMetadata{Modes: []string{"embedding"}}},
		{ID: "ollama/llama3:8b"},
	}

	assert.Equal(t,
		`{"models":[`+
			`{"name":"models/gemini/gemini-2.5-flash","displayName":"Gemini 2.5 Flash","description":"Fast","inputTokenLimit":1048576,"outputTokenLimit":65536,"supportedGenerationMethods":["generateContent"]},`+
			`{"name":"models/openai/text-embedding-3-small","displayName":"openai/text-embedding-3-small","supportedGenerationMethods":[]},`+
			`{"name":"models/ollama/llama3:8b","displayName":"ollama/llama3:8b","supportedGenerationMethods":["generateContent"]}`+
			`]}`,
		encode(t, FromModels(models)))
	assert.Equal(t, `{"models":[]}`, encode(t, FromModels(nil)))
}
