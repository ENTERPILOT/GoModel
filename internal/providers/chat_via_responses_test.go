package providers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type stubChatViaResponsesProvider struct {
	capturedReq *core.ResponsesRequest
	resp        *core.ResponsesResponse
	respErr     error
	streamData  string
	streamErr   error
}

func (p *stubChatViaResponsesProvider) Responses(_ context.Context, req *core.ResponsesRequest) (*core.ResponsesResponse, error) {
	p.capturedReq = req
	return p.resp, p.respErr
}

func (p *stubChatViaResponsesProvider) StreamResponses(_ context.Context, req *core.ResponsesRequest) (io.ReadCloser, error) {
	p.capturedReq = req
	if p.streamErr != nil {
		return nil, p.streamErr
	}
	return io.NopCloser(strings.NewReader(p.streamData)), nil
}

func chatViaResponsesExtras(fields map[string]string) core.UnknownJSONFields {
	raw := make(map[string]json.RawMessage, len(fields))
	for key, value := range fields {
		raw[key] = json.RawMessage(value)
	}
	return core.UnknownJSONFieldsFromMap(raw)
}

func TestConvertChatRequestToResponses(t *testing.T) {
	temperature := 0.7
	topP := 0.9
	maxTokens := 1024
	parallelToolCalls := false
	req := &core.ChatRequest{
		Model:             "gpt-5.1-codex",
		Messages:          []core.Message{{Role: "user", Content: "hello"}},
		Temperature:       &temperature,
		TopP:              &topP,
		MaxTokens:         &maxTokens,
		ParallelToolCalls: &parallelToolCalls,
		Stream:            true,
		StreamOptions:     &core.StreamOptions{IncludeUsage: true},
		Reasoning:         &core.Reasoning{Effort: "high"},
		User:              "user-1",
		ServiceTier:       "flex",
		ExtraFields: chatViaResponsesExtras(map[string]string{
			"metadata":      `{"session":"abc"}`,
			"x_trace_token": `"keep-me"`,
		}),
	}

	responsesReq, err := ConvertChatRequestToResponses(req)
	require.NoError(t, err)

	assert.Equal(t, "gpt-5.1-codex", responsesReq.Model)
	assert.Equal(t, &temperature, responsesReq.Temperature)
	assert.Equal(t, &topP, responsesReq.TopP)
	assert.Equal(t, &parallelToolCalls, responsesReq.ParallelToolCalls)
	assert.Equal(t, &core.Reasoning{Effort: "high"}, responsesReq.Reasoning)
	assert.Equal(t, "user-1", responsesReq.User)
	assert.Equal(t, "flex", responsesReq.ServiceTier)
	assert.True(t, responsesReq.Stream)

	require.NotNil(t, responsesReq.MaxOutputTokens)
	assert.Equal(t, 1024, *responsesReq.MaxOutputTokens)

	assert.Equal(t, map[string]string{"session": "abc"}, responsesReq.Metadata)

	// stream_options is never forwarded: Responses stream options differ.
	assert.Nil(t, responsesReq.StreamOptions)

	// Unknown extras survive; mapped extras move onto typed fields.
	assert.Equal(t, json.RawMessage(`"keep-me"`), responsesReq.ExtraFields.Lookup("x_trace_token"))
	assert.Nil(t, responsesReq.ExtraFields.Lookup("metadata"))

	require.NotNil(t, responsesReq.Input)
	assert.Empty(t, responsesReq.Instructions)
}

func TestConvertChatRequestToResponses_RequiresRequest(t *testing.T) {
	_, err := ConvertChatRequestToResponses(nil)
	require.Error(t, err)

	var gatewayErr *core.GatewayError
	require.ErrorAs(t, err, &gatewayErr)
	assert.Equal(t, http.StatusBadRequest, gatewayErr.HTTPStatusCode())
}

func TestConvertChatRequestToResponses_MaxTokensMapping(t *testing.T) {
	maxTokens := 512

	t.Run("max_tokens maps to max_output_tokens", func(t *testing.T) {
		responsesReq, err := ConvertChatRequestToResponses(&core.ChatRequest{
			Model:     "m",
			Messages:  []core.Message{{Role: "user", Content: "hi"}},
			MaxTokens: &maxTokens,
		})
		require.NoError(t, err)
		require.NotNil(t, responsesReq.MaxOutputTokens)
		assert.Equal(t, 512, *responsesReq.MaxOutputTokens)
	})

	t.Run("explicit max_completion_tokens wins over max_tokens", func(t *testing.T) {
		responsesReq, err := ConvertChatRequestToResponses(&core.ChatRequest{
			Model:     "m",
			Messages:  []core.Message{{Role: "user", Content: "hi"}},
			MaxTokens: &maxTokens,
			ExtraFields: chatViaResponsesExtras(map[string]string{
				"max_completion_tokens": `2048`,
			}),
		})
		require.NoError(t, err)
		require.NotNil(t, responsesReq.MaxOutputTokens)
		assert.Equal(t, 2048, *responsesReq.MaxOutputTokens)
		// Lifted onto the typed field, so it must not double-emit as an extra.
		assert.Nil(t, responsesReq.ExtraFields.Lookup("max_completion_tokens"))
	})

	t.Run("unset when neither is present", func(t *testing.T) {
		responsesReq, err := ConvertChatRequestToResponses(&core.ChatRequest{
			Model:    "m",
			Messages: []core.Message{{Role: "user", Content: "hi"}},
		})
		require.NoError(t, err)
		assert.Nil(t, responsesReq.MaxOutputTokens)
	})
}

func TestConvertChatRequestToResponses_MessagesAndInstructions(t *testing.T) {
	req := &core.ChatRequest{
		Model: "m",
		Messages: []core.Message{
			{Role: "system", Content: "You are helpful."},
			{Role: "user", Content: "hello"},
		},
	}

	responsesReq, err := ConvertChatRequestToResponses(req)
	require.NoError(t, err)
	assert.Equal(t, "You are helpful.", responsesReq.Instructions)
	require.NotNil(t, responsesReq.Input)
}

func TestConvertChatRequestToResponses_FlattensTools(t *testing.T) {
	parameters := map[string]any{"type": "object", "properties": map[string]any{}}

	t.Run("nested chat function tool flattens", func(t *testing.T) {
		responsesReq, err := ConvertChatRequestToResponses(&core.ChatRequest{
			Model:    "m",
			Messages: []core.Message{{Role: "user", Content: "hi"}},
			Tools: []map[string]any{
				{
					"type": "function",
					"function": map[string]any{
						"name":        "exec_command",
						"description": "Run a command.",
						"parameters":  parameters,
						"strict":      true,
					},
				},
			},
		})
		require.NoError(t, err)
		require.Len(t, responsesReq.Tools, 1)
		assert.Equal(t, map[string]any{
			"type":        "function",
			"name":        "exec_command",
			"description": "Run a command.",
			"parameters":  parameters,
			"strict":      true,
		}, responsesReq.Tools[0])
	})

	t.Run("already flat tool passes through", func(t *testing.T) {
		responsesReq, err := ConvertChatRequestToResponses(&core.ChatRequest{
			Model:    "m",
			Messages: []core.Message{{Role: "user", Content: "hi"}},
			Tools: []map[string]any{
				{"type": "function", "name": "exec_command", "parameters": parameters},
			},
		})
		require.NoError(t, err)
		require.Len(t, responsesReq.Tools, 1)
		assert.Equal(t, map[string]any{
			"type":       "function",
			"name":       "exec_command",
			"parameters": parameters,
		}, responsesReq.Tools[0])
	})

	t.Run("non-function tool rejected", func(t *testing.T) {
		_, err := ConvertChatRequestToResponses(&core.ChatRequest{
			Model:    "m",
			Messages: []core.Message{{Role: "user", Content: "hi"}},
			Tools:    []map[string]any{{"type": "custom", "name": "exec_command"}},
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "tools")
	})
}

func TestConvertChatRequestToResponses_ToolChoice(t *testing.T) {
	tests := []struct {
		name   string
		choice any
		want   any
	}{
		{name: "auto string passes", choice: "auto", want: "auto"},
		{name: "required string passes", choice: "required", want: "required"},
		{name: "none string passes", choice: "none", want: "none"},
		{
			name:   "nested function choice flattens",
			choice: map[string]any{"type": "function", "function": map[string]any{"name": "exec_command"}},
			want:   map[string]any{"type": "function", "name": "exec_command"},
		},
		{
			name:   "already flat function choice passes through",
			choice: map[string]any{"type": "function", "name": "exec_command"},
			want:   map[string]any{"type": "function", "name": "exec_command"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			responsesReq, err := ConvertChatRequestToResponses(&core.ChatRequest{
				Model:      "m",
				Messages:   []core.Message{{Role: "user", Content: "hi"}},
				ToolChoice: tt.choice,
			})
			require.NoError(t, err)
			assert.Equal(t, tt.want, responsesReq.ToolChoice)
		})
	}

	t.Run("unknown object type rejected", func(t *testing.T) {
		_, err := ConvertChatRequestToResponses(&core.ChatRequest{
			Model:      "m",
			Messages:   []core.Message{{Role: "user", Content: "hi"}},
			ToolChoice: map[string]any{"type": "allowed_tools", "tools": []any{}},
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "tool_choice")
	})

	t.Run("non-string non-object rejected", func(t *testing.T) {
		_, err := ConvertChatRequestToResponses(&core.ChatRequest{
			Model:      "m",
			Messages:   []core.Message{{Role: "user", Content: "hi"}},
			ToolChoice: 42,
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "tool_choice")
	})
}

func TestConvertChatRequestToResponses_ResponseFormat(t *testing.T) {
	newRequest := func(responseFormat string) *core.ChatRequest {
		return &core.ChatRequest{
			Model:    "m",
			Messages: []core.Message{{Role: "user", Content: "hi"}},
			ExtraFields: chatViaResponsesExtras(map[string]string{
				"response_format": responseFormat,
			}),
		}
	}

	t.Run("text yields no text settings", func(t *testing.T) {
		responsesReq, err := ConvertChatRequestToResponses(newRequest(`{"type":"text"}`))
		require.NoError(t, err)
		assert.Nil(t, responsesReq.Text)
		assert.Nil(t, responsesReq.ExtraFields.Lookup("response_format"))
	})

	t.Run("json_object maps to text.format", func(t *testing.T) {
		responsesReq, err := ConvertChatRequestToResponses(newRequest(`{"type":"json_object"}`))
		require.NoError(t, err)
		assert.Equal(t, map[string]any{
			"format": map[string]any{"type": "json_object"},
		}, responsesReq.Text)
		assert.Nil(t, responsesReq.ExtraFields.Lookup("response_format"))
	})

	t.Run("json_schema flattens one level", func(t *testing.T) {
		responsesReq, err := ConvertChatRequestToResponses(newRequest(
			`{"type":"json_schema","json_schema":{"name":"out","schema":{"type":"object"},"strict":true}}`,
		))
		require.NoError(t, err)
		assert.Equal(t, map[string]any{
			"format": map[string]any{
				"type":   "json_schema",
				"name":   "out",
				"schema": map[string]any{"type": "object"},
				"strict": true,
			},
		}, responsesReq.Text)
	})

	t.Run("unknown format type rejected", func(t *testing.T) {
		_, err := ConvertChatRequestToResponses(newRequest(`{"type":"grammar"}`))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "response_format")
	})

	t.Run("malformed value rejected", func(t *testing.T) {
		_, err := ConvertChatRequestToResponses(newRequest(`"json_object"`))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "response_format")
	})
}

func TestConvertChatRequestToResponses_RejectsUnsupportedFields(t *testing.T) {
	tests := []struct {
		name  string
		extra map[string]string
		want  string
	}{
		{name: "n greater than one", extra: map[string]string{"n": `2`}, want: "n"},
		{name: "logit_bias", extra: map[string]string{"logit_bias": `{"123":-100}`}, want: "logit_bias"},
		{name: "stop", extra: map[string]string{"stop": `["END"]`}, want: "stop"},
		{name: "seed", extra: map[string]string{"seed": `42`}, want: "seed"},
		{name: "frequency_penalty", extra: map[string]string{"frequency_penalty": `0.5`}, want: "frequency_penalty"},
		{name: "presence_penalty", extra: map[string]string{"presence_penalty": `0.5`}, want: "presence_penalty"},
		{name: "logprobs", extra: map[string]string{"logprobs": `true`}, want: "logprobs"},
		{name: "top_logprobs", extra: map[string]string{"top_logprobs": `3`}, want: "top_logprobs"},
		{name: "modalities", extra: map[string]string{"modalities": `["text","audio"]`}, want: "modalities"},
		{name: "audio", extra: map[string]string{"audio": `{"voice":"alloy","format":"wav"}`}, want: "audio"},
		{name: "web_search_options", extra: map[string]string{"web_search_options": `{}`}, want: "web_search_options"},
		{name: "function_call", extra: map[string]string{"function_call": `{"name":"exec_command"}`}, want: "function_call"},
		{name: "functions", extra: map[string]string{"functions": `[{"name":"exec_command"}]`}, want: "functions"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := &core.ChatRequest{
				Model:       "m",
				Messages:    []core.Message{{Role: "user", Content: "hi"}},
				ExtraFields: chatViaResponsesExtras(tt.extra),
			}

			_, err := ConvertChatRequestToResponses(req)
			require.Error(t, err)

			var gatewayErr *core.GatewayError
			require.ErrorAs(t, err, &gatewayErr)
			assert.Equal(t, http.StatusBadRequest, gatewayErr.HTTPStatusCode())
			assert.Contains(t, gatewayErr.Message, tt.want)
		})
	}
}

func TestConvertChatRequestToResponses_AllowsSingleChoiceN(t *testing.T) {
	responsesReq, err := ConvertChatRequestToResponses(&core.ChatRequest{
		Model:       "m",
		Messages:    []core.Message{{Role: "user", Content: "hi"}},
		ExtraFields: chatViaResponsesExtras(map[string]string{"n": `1`}),
	})
	require.NoError(t, err)
	// n=1 is a validated no-op; Responses has no n field, so it is
	// stripped from the translated request rather than passed through.
	assert.Nil(t, responsesReq.ExtraFields.Lookup("n"))
}

func TestConvertChatRequestToResponses_StripsPrediction(t *testing.T) {
	responsesReq, err := ConvertChatRequestToResponses(&core.ChatRequest{
		Model:    "m",
		Messages: []core.Message{{Role: "user", Content: "hi"}},
		ExtraFields: chatViaResponsesExtras(map[string]string{
			"prediction":    `{"type":"content","content":"draft"}`,
			"x_trace_token": `"keep-me"`,
		}),
	})
	require.NoError(t, err)
	// prediction is a pure speed hint with no Responses equivalent: it is
	// tolerated but stripped, while unknown extras survive.
	assert.Nil(t, responsesReq.ExtraFields.Lookup("prediction"))
	assert.Equal(t, json.RawMessage(`"keep-me"`), responsesReq.ExtraFields.Lookup("x_trace_token"))
}

func TestConvertChatRequestToResponses_ToleratesExplicitNulls(t *testing.T) {
	// An explicit JSON null spells "not set" on the wire; it must not trip
	// the unsupported-field rejection.
	responsesReq, err := ConvertChatRequestToResponses(&core.ChatRequest{
		Model:    "m",
		Messages: []core.Message{{Role: "user", Content: "hi"}},
		ExtraFields: chatViaResponsesExtras(map[string]string{
			"n":    `null`,
			"stop": `null`,
			"seed": `null`,
		}),
	})
	require.NoError(t, err)
	assert.Nil(t, responsesReq.ExtraFields.Lookup("n"))
}

func chatViaResponsesCompletedResponse() *core.ResponsesResponse {
	return &core.ResponsesResponse{
		ID:     "resp_123",
		Object: "response",
		Status: "completed",
		Model:  "gpt-5.1-codex",
		Output: []core.ResponsesOutputItem{
			{
				Type:    "message",
				Role:    "assistant",
				Content: []core.ResponsesContentItem{{Type: "output_text", Text: "hello"}},
			},
		},
	}
}

func TestChatViaResponses(t *testing.T) {
	t.Run("converts request and response", func(t *testing.T) {
		provider := &stubChatViaResponsesProvider{resp: chatViaResponsesCompletedResponse()}
		maxTokens := 64

		chatResp, err := ChatViaResponses(context.Background(), provider, &core.ChatRequest{
			Model:     "gpt-5.1-codex",
			Messages:  []core.Message{{Role: "user", Content: "hi"}},
			MaxTokens: &maxTokens,
		}, "chatgpt")
		require.NoError(t, err)
		require.NotNil(t, chatResp)
		assert.NotEmpty(t, chatResp.Choices)

		require.NotNil(t, provider.capturedReq)
		assert.Equal(t, "gpt-5.1-codex", provider.capturedReq.Model)
		require.NotNil(t, provider.capturedReq.MaxOutputTokens)
		assert.Equal(t, 64, *provider.capturedReq.MaxOutputTokens)
		assert.False(t, provider.capturedReq.Stream)
	})

	t.Run("provider error passes through", func(t *testing.T) {
		wantErr := errors.New("upstream exploded")
		provider := &stubChatViaResponsesProvider{respErr: wantErr}

		chatResp, err := ChatViaResponses(context.Background(), provider, &core.ChatRequest{
			Model:    "m",
			Messages: []core.Message{{Role: "user", Content: "hi"}},
		}, "chatgpt")
		require.Nil(t, chatResp)
		require.ErrorIs(t, err, wantErr)
	})

	t.Run("nil response rejected", func(t *testing.T) {
		provider := &stubChatViaResponsesProvider{}

		chatResp, err := ChatViaResponses(context.Background(), provider, &core.ChatRequest{
			Model:    "m",
			Messages: []core.Message{{Role: "user", Content: "hi"}},
		}, "chatgpt")
		require.Nil(t, chatResp)

		var gatewayErr *core.GatewayError
		require.ErrorAs(t, err, &gatewayErr)
		assert.Equal(t, http.StatusBadGateway, gatewayErr.HTTPStatusCode())
		assert.Equal(t, "provider returned empty response", gatewayErr.Message)
		assert.Equal(t, "chatgpt", gatewayErr.Provider)
	})

	t.Run("failed response is a provider error", func(t *testing.T) {
		provider := &stubChatViaResponsesProvider{resp: &core.ResponsesResponse{
			ID:     "resp_123",
			Status: "failed",
			Error:  &core.ResponsesError{Code: "server_error", Message: "boom upstream"},
		}}

		chatResp, err := ChatViaResponses(context.Background(), provider, &core.ChatRequest{
			Model:    "m",
			Messages: []core.Message{{Role: "user", Content: "hi"}},
		}, "chatgpt")
		require.Nil(t, chatResp)

		var gatewayErr *core.GatewayError
		require.ErrorAs(t, err, &gatewayErr)
		assert.Equal(t, http.StatusBadGateway, gatewayErr.HTTPStatusCode())
		assert.Contains(t, gatewayErr.Message, "boom upstream")
		assert.Equal(t, "chatgpt", gatewayErr.Provider)
		require.NotNil(t, gatewayErr.Code)
		assert.Equal(t, "server_error", *gatewayErr.Code)
	})

	t.Run("failed response without upstream error has generic message", func(t *testing.T) {
		provider := &stubChatViaResponsesProvider{resp: &core.ResponsesResponse{
			ID:     "resp_123",
			Status: "failed",
		}}

		chatResp, err := ChatViaResponses(context.Background(), provider, &core.ChatRequest{
			Model:    "m",
			Messages: []core.Message{{Role: "user", Content: "hi"}},
		}, "chatgpt")
		require.Nil(t, chatResp)

		var gatewayErr *core.GatewayError
		require.ErrorAs(t, err, &gatewayErr)
		assert.Equal(t, http.StatusBadGateway, gatewayErr.HTTPStatusCode())
		assert.Contains(t, gatewayErr.Message, "failed")
	})

	t.Run("unsupported field fails before upstream call", func(t *testing.T) {
		provider := &stubChatViaResponsesProvider{resp: chatViaResponsesCompletedResponse()}

		chatResp, err := ChatViaResponses(context.Background(), provider, &core.ChatRequest{
			Model:       "m",
			Messages:    []core.Message{{Role: "user", Content: "hi"}},
			ExtraFields: chatViaResponsesExtras(map[string]string{"seed": `42`}),
		}, "chatgpt")
		require.Nil(t, chatResp)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "seed")
		assert.Nil(t, provider.capturedReq)
	})
}

func TestStreamChatViaResponses(t *testing.T) {
	t.Run("forces stream and wraps converter", func(t *testing.T) {
		provider := &stubChatViaResponsesProvider{streamData: "data: [DONE]\n\n"}

		stream, err := StreamChatViaResponses(context.Background(), provider, &core.ChatRequest{
			Model:         "gpt-5.1-codex",
			Messages:      []core.Message{{Role: "user", Content: "hi"}},
			Stream:        true,
			StreamOptions: &core.StreamOptions{IncludeUsage: true},
		}, "chatgpt")
		require.NoError(t, err)
		require.NotNil(t, stream)
		defer func() {
			_ = stream.Close()
		}()

		require.NotNil(t, provider.capturedReq)
		assert.True(t, provider.capturedReq.Stream, "upstream Responses call must stream")
		assert.Nil(t, provider.capturedReq.StreamOptions, "chat stream_options must not be forwarded")
	})

	t.Run("stream error passes through", func(t *testing.T) {
		wantErr := errors.New("stream failed")
		provider := &stubChatViaResponsesProvider{streamErr: wantErr}

		stream, err := StreamChatViaResponses(context.Background(), provider, &core.ChatRequest{
			Model:    "m",
			Messages: []core.Message{{Role: "user", Content: "hi"}},
		}, "chatgpt")
		require.Nil(t, stream)
		require.ErrorIs(t, err, wantErr)
	})

	t.Run("usage policy forces the converter usage chunk", func(t *testing.T) {
		provider := &stubChatViaResponsesProvider{streamData: chatViaResponsesTextStream()}
		ctx := core.WithEnforceReturningUsageData(context.Background(), true)

		stream, err := StreamChatViaResponses(ctx, provider, &core.ChatRequest{
			Model:    "gpt-5.1-codex",
			Messages: []core.Message{{Role: "user", Content: "hi"}},
			Stream:   true,
		}, "chatgpt")
		require.NoError(t, err)
		defer func() {
			_ = stream.Close()
		}()

		raw, err := io.ReadAll(stream)
		require.NoError(t, err)
		assert.Contains(t, string(raw), `"usage"`)
		assert.Nil(t, provider.capturedReq.StreamOptions, "chat stream_options must not be forwarded")
	})

	t.Run("unsupported field fails before upstream call", func(t *testing.T) {
		provider := &stubChatViaResponsesProvider{}

		stream, err := StreamChatViaResponses(context.Background(), provider, &core.ChatRequest{
			Model:       "m",
			Messages:    []core.Message{{Role: "user", Content: "hi"}},
			ExtraFields: chatViaResponsesExtras(map[string]string{"logprobs": `true`}),
		}, "chatgpt")
		require.Nil(t, stream)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "logprobs")
		assert.Nil(t, provider.capturedReq)
	})
}
