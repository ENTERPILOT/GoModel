package providers

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"strings"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type capturingChatProvider struct {
	capturedReq *core.ChatRequest
	chatResp    *core.ChatResponse
	streamData  string
	streamErr   error
}

func (p *capturingChatProvider) ChatCompletion(_ context.Context, _ *core.ChatRequest) (*core.ChatResponse, error) {
	return p.chatResp, nil
}

func (p *capturingChatProvider) StreamChatCompletion(_ context.Context, req *core.ChatRequest) (io.ReadCloser, error) {
	p.capturedReq = req
	if p.streamErr != nil {
		return nil, p.streamErr
	}
	return io.NopCloser(strings.NewReader(p.streamData)), nil
}

func TestResponsesFunctionCallIDs(t *testing.T) {
	t.Run("preserve explicit call id", func(t *testing.T) {
		const callID = "call_123"
		got := ResponsesFunctionCallCallID(callID)
		require.Equal(t, callID, got)
		got = ResponsesFunctionCallItemID(callID)
		require.Equal(t, "fc_"+callID, got, "ResponsesFunctionCallItemID(%q)", callID)
	})

	t.Run("generate ids when empty", func(t *testing.T) {
		callID := ResponsesFunctionCallCallID("  ")
		require.True(t, strings.HasPrefix(callID, "call_"), "generated call id = %q, want prefix call_", callID)

		itemID := ResponsesFunctionCallItemID("")
		require.True(t, strings.HasPrefix(itemID, "fc_call_"), "generated item id = %q, want prefix fc_call_", itemID)
	})
}

func TestConvertResponsesRequestToChat(t *testing.T) {
	temp := 0.7
	maxTokens := 1024
	includeUsage := true
	mustResponsesRequest := func(data string) *core.ResponsesRequest {
		t.Helper()
		var req core.ResponsesRequest
		err := json.Unmarshal([]byte(data), &req)
		require.NoError(t, err)

		return &req
	}

	tests := []struct {
		name      string
		input     *core.ResponsesRequest
		expectErr bool
		checkFn   func(*testing.T, *core.ChatRequest)
	}{
		{
			name: "string input",
			input: &core.ResponsesRequest{
				Model: "test-model",
				Input: "Hello",
			},
			checkFn: func(t *testing.T, req *core.ChatRequest) {
				assert.Equal(t, "test-model", req.Model)
				require.Len(t, req.Messages, 1)
				assert.Equal(t, "user", req.Messages[0].Role)
				got := core.ExtractTextContent(req.Messages[0].Content)
				assert.Equal(t, "Hello", got)

			},
		},
		{
			name: "with instructions and options",
			input: &core.ResponsesRequest{
				Model:             "test-model",
				Input:             "Hello",
				Instructions:      "Be helpful",
				Temperature:       &temp,
				MaxOutputTokens:   &maxTokens,
				Reasoning:         &core.Reasoning{Effort: "high"},
				StreamOptions:     &core.StreamOptions{IncludeUsage: includeUsage},
				Tools:             []map[string]any{{"type": "function", "function": map[string]any{"name": "lookup_weather"}}},
				ToolChoice:        map[string]any{"type": "function", "function": map[string]any{"name": "lookup_weather"}},
				ParallelToolCalls: new(false),
			},
			checkFn: func(t *testing.T, req *core.ChatRequest) {
				require.Len(t, req.Messages, 2)
				require.Equal(t, "system", req.Messages[0].Role)
				require.NotNil(t, req.MaxTokens)
				require.Equal(t, 1024, *req.MaxTokens)
				require.NotNil(t, req.Reasoning)
				require.Equal(t, "high", req.Reasoning.Effort)
				require.NotNil(t, req.StreamOptions)
				require.True(t, req.StreamOptions.IncludeUsage)
				require.Len(t, req.Tools, 1)
				require.NotNil(t, req.ToolChoice)
				require.NotNil(t, req.ParallelToolCalls)
				require.False(t, *req.ParallelToolCalls)
			},
		},
		{
			name: "normalizes native responses tool format",
			input: &core.ResponsesRequest{
				Model: "test-model",
				Input: "Hello",
				Tools: []map[string]any{
					{
						"type":        "function",
						"name":        "lookup_weather",
						"description": "Get weather by city.",
						"parameters": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"city": map[string]any{"type": "string"},
							},
						},
					},
				},
				ToolChoice: map[string]any{
					"type": "function",
					"name": "lookup_weather",
				},
			},
			checkFn: func(t *testing.T, req *core.ChatRequest) {
				require.Len(t, req.Tools, 1)

				function, ok := req.Tools[0]["function"].(map[string]any)
				require.True(t, ok, "Tools[0].function = %#v, want object", req.Tools[0]["function"])
				require.Equal(t, "lookup_weather", function["name"])
				_, ok = req.Tools[0]["name"]
				require.False(t, ok, "Tools[0].name should be wrapped into function, got %+v", req.Tools[0])

				toolChoice, ok := req.ToolChoice.(map[string]any)
				require.True(t, ok, "ToolChoice = %#v, want object", req.ToolChoice)

				selected, ok := toolChoice["function"].(map[string]any)
				require.True(t, ok, "ToolChoice.function = %#v, want object", toolChoice["function"])
				require.Equal(t, "lookup_weather", selected["name"])
				_, ok = toolChoice["name"]
				require.False(t, ok, "ToolChoice.name should be wrapped into function, got %+v", toolChoice)

			},
		},
		{
			name: "typed multimodal input",
			input: &core.ResponsesRequest{
				Model: "test-model",
				Input: []core.ResponsesInputElement{
					{
						Role: " user ",
						Content: []core.ContentPart{
							{Type: "input_text", Text: "Describe the image."},
							{
								Type: "input_image",
								ImageURL: &core.ImageURLContent{
									URL:    "https://example.com/image.png",
									Detail: "high",
								},
							},
						},
					},
				},
			},
			checkFn: func(t *testing.T, req *core.ChatRequest) {
				require.Len(t, req.Messages, 1)

				parts, ok := req.Messages[0].Content.([]core.ContentPart)
				require.True(t, ok, "Messages[0].Content type = %T, want []core.ContentPart", req.Messages[0].Content)
				require.Len(t, parts, 2)
				require.NotNil(t, parts[1].ImageURL)
				require.Equal(t, "https://example.com/image.png", parts[1].ImageURL.URL)

			},
		},
		{
			name: "function call loop items",
			input: &core.ResponsesRequest{
				Model: "test-model",
				Input: []any{
					map[string]any{
						"type":      "function_call",
						"call_id":   "call_123",
						"name":      "lookup_weather",
						"arguments": `{"city":"Warsaw"}`,
					},
					map[string]any{
						"type":    "function_call_output",
						"call_id": "call_123",
						"output":  map[string]any{"temperature_c": 21},
					},
				},
			},
			checkFn: func(t *testing.T, req *core.ChatRequest) {
				require.Len(t, req.Messages, 2)
				require.Len(t, req.Messages[0].ToolCalls, 1)
				require.Equal(t, "call_123", req.Messages[0].ToolCalls[0].ID)
				require.True(t, req.Messages[0].ContentNull)
				require.Equal(t, "tool", req.Messages[1].Role)
				require.Equal(t, "call_123", req.Messages[1].ToolCallID, "unexpected tool result message: %+v", req.Messages[1])

			},
		},
		{
			name: "typed function call output stringifies structured output",
			input: mustResponsesRequest(`{
				"model":"test-model",
				"input":[{
					"type":"function_call_output",
					"call_id":"call_456",
					"output":{"temperature_c":21},
					"x_meta":true
				}]
			}`),
			checkFn: func(t *testing.T, req *core.ChatRequest) {
				require.Len(t, req.Messages, 1)
				require.Equal(t, "tool", req.Messages[0].Role)
				require.Equal(t, "call_456", req.Messages[0].ToolCallID, "unexpected tool result message: %+v", req.Messages[0])
				got := req.Messages[0].Content
				require.Equal(t, `{"temperature_c":21}`, got)
				require.NotNil(t, req.Messages[0].ExtraFields.Lookup("x_meta"))

			},
		},
		{
			name: "assistant text merges with later function call item",
			input: &core.ResponsesRequest{
				Model: "test-model",
				Input: []any{
					map[string]any{
						"type":   "message",
						"role":   "assistant",
						"status": "completed",
						"content": []map[string]any{
							{"type": "output_text", "text": "I'll check that for you."},
						},
					},
					map[string]any{
						"type":      "function_call",
						"call_id":   "call_123",
						"name":      "lookup_weather",
						"arguments": `{"city":"Warsaw"}`,
					},
				},
			},
			checkFn: func(t *testing.T, req *core.ChatRequest) {
				require.Len(t, req.Messages, 1)
				got := core.ExtractTextContent(req.Messages[0].Content)
				require.Equal(t, "I'll check that for you.", got)
				require.Len(t, req.Messages[0].ToolCalls, 1)

			},
		},
		{
			name: "assistant structured content merges with later function call item",
			input: &core.ResponsesRequest{
				Model: "test-model",
				Input: []any{
					map[string]any{
						"type":   "message",
						"role":   "assistant",
						"status": "completed",
						"content": []map[string]any{
							{"type": "output_text", "text": "I'll check that for you."},
							{"type": "input_image", "image_url": map[string]any{"url": "https://example.com/image.png"}},
						},
					},
					map[string]any{
						"type":      "function_call",
						"call_id":   "call_123",
						"name":      "lookup_weather",
						"arguments": `{"city":"Warsaw"}`,
					},
				},
			},
			checkFn: func(t *testing.T, req *core.ChatRequest) {
				require.Len(t, req.Messages, 1)

				parts, ok := req.Messages[0].Content.([]core.ContentPart)
				require.True(t, ok, "Messages[0].Content type = %T, want []core.ContentPart", req.Messages[0].Content)
				require.Len(t, parts, 2)
				require.Equal(t, "I'll check that for you.", parts[0].Text)
				require.NotNil(t, parts[1].ImageURL)
				require.Equal(t, "https://example.com/image.png", parts[1].ImageURL.URL)
				require.Len(t, req.Messages[0].ToolCalls, 1)
				require.Equal(t, "call_123", req.Messages[0].ToolCalls[0].ID)

			},
		},
		{
			name: "invalid content fails",
			input: &core.ResponsesRequest{
				Model: "test-model",
				Input: []any{
					map[string]any{
						"role": "user",
						"content": []any{
							map[string]any{"type": "unknown"},
						},
					},
				},
			},
			expectErr: true,
		},
		{
			name: "nil input fails",
			input: &core.ResponsesRequest{
				Model: "test-model",
				Input: nil,
			},
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := ConvertResponsesRequestToChat(tt.input)
			if tt.expectErr {
				require.Error(t, err)

				return
			}
			require.NoError(t, err)

			tt.checkFn(t, result)
		})
	}
}

func TestConvertResponsesRequestToChat_MapsPortableAgentsSDKFields(t *testing.T) {
	topP := 0.8
	req := &core.ResponsesRequest{
		Model:       "test-model",
		Input:       "Hello",
		TopP:        &topP,
		Text:        map[string]any{"format": map[string]any{"type": "text"}},
		User:        "tenant-123",
		ServiceTier: "flex",
	}

	chatReq, err := ConvertResponsesRequestToChat(req)
	require.NoError(t, err)
	require.NotNil(t, chatReq.TopP)
	require.Equal(t, 0.8, *chatReq.TopP)
	require.Equal(t, "tenant-123", chatReq.User)
	require.Equal(t, "flex", chatReq.ServiceTier)
}

func TestConvertResponsesRequestToChat_AcceptsAnnotationOnlyInclude(t *testing.T) {
	tests := []struct {
		name    string
		include []string
	}{
		{name: "encrypted reasoning", include: []string{"reasoning.encrypted_content"}},
		{name: "hosted tool annotations", include: []string{"web_search_call.action.sources", "file_search_call.results", "code_interpreter_call.outputs"}},
		{name: "input echo annotations", include: []string{"message.input_image.image_url", "computer_call_output.output.image_url"}},
		{name: "unrecognized future value", include: []string{"some_future_call.details"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := &core.ResponsesRequest{Model: "test-model", Input: "Hello", Include: tt.include}

			chatReq, err := ConvertResponsesRequestToChat(req)
			require.NoError(t, err)
			require.Len(t, chatReq.Messages, 1)
			require.Equal(t, "Hello", chatReq.Messages[0].Content)
			require.True(t, chatReq.ExtraFields.IsEmpty(), "ExtraFields = %#v, want include dropped rather than forwarded", chatReq.ExtraFields)
			require.Equal(t, len(tt.include), len(req.Include), "req.Include = %#v, want the caller's request left unmutated", req.Include)
		})
	}
}

// TestConvertResponsesRequestToChat_TranslatesCodexRequest locks the payload
// Codex sends over wire_api = "responses". Codex always attaches
// include: ["reasoning.encrypted_content"], which used to fail the whole
// request against chat-translated providers such as DeepSeek (issue #532).
func TestConvertResponsesRequestToChat_TranslatesCodexRequest(t *testing.T) {
	const body = `{
  "model": "deepseek-v4-pro",
  "instructions": "You are Codex.",
  "input": [
    {"type": "message", "role": "user", "content": [{"type": "input_text", "text": "Reply with exactly ok"}]}
  ],
  "tools": [
    {"type": "function", "name": "shell", "description": "run", "strict": false,
     "parameters": {"type": "object", "properties": {"command": {"type": "string"}}}}
  ],
  "tool_choice": "auto",
  "parallel_tool_calls": false,
  "reasoning": {"effort": "medium"},
  "store": false,
  "stream": true,
  "include": ["reasoning.encrypted_content"],
  "text": {"verbosity": "medium"}
}`

	var req core.ResponsesRequest
	err := json.Unmarshal([]byte(body), &req)
	require.NoError(t, err)

	chatReq, err := ConvertResponsesRequestToChat(&req)
	require.NoError(t, err)
	require.Len(t, chatReq.Messages, 2)
	require.Equal(t, "system", chatReq.Messages[0].Role)
	require.Equal(t, "user", chatReq.Messages[1].Role)
	require.Len(t, chatReq.Tools, 1)
	_, ok := chatReq.Tools[0]["function"]
	require.True(t, ok, "Tools[0] = %#v, want chat-shaped function member", chatReq.Tools[0])
	require.NotNil(t, chatReq.Reasoning)
	require.Equal(t, "medium", chatReq.Reasoning.Effort)
	require.True(t, chatReq.Stream)
}

func TestConvertResponsesRequestToChat_RejectsOutputLogprobsInclude(t *testing.T) {
	tests := []struct {
		name    string
		include []string
	}{
		{name: "alone", include: []string{"message.output_text.logprobs"}},
		{name: "mixed with droppable values", include: []string{"reasoning.encrypted_content", "message.output_text.logprobs"}},
		{name: "padded", include: []string{" message.output_text.logprobs "}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := &core.ResponsesRequest{Model: "test-model", Input: "Hello", Include: tt.include}

			_, err := ConvertResponsesRequestToChat(req)
			require.Error(t, err)
			require.Contains(t, err.Error(), "include")
		})
	}
}

func TestConvertResponsesRequestToChat_NormalizesToolChoiceAliases(t *testing.T) {
	functionTools := []map[string]any{
		{
			"type":       "function",
			"name":       "exec_command",
			"parameters": map[string]any{"type": "object"},
		},
	}
	tests := []struct {
		name string
		req  *core.ResponsesRequest
		want any
	}{
		{
			name: "tool_choice none alias",
			req:  &core.ResponsesRequest{Model: "test-model", Input: "Hello", ToolChoice: map[string]any{"type": "none"}},
			want: "none",
		},
		{
			name: "tool_choice auto alias",
			req:  &core.ResponsesRequest{Model: "test-model", Input: "Hello", ToolChoice: map[string]any{"type": "auto"}},
			want: "auto",
		},
		{
			name: "tool_choice required alias",
			req:  &core.ResponsesRequest{Model: "test-model", Input: "Hello", ToolChoice: map[string]any{"type": "required"}},
			want: "required",
		},
		{
			name: "bare tool_choice none",
			req:  &core.ResponsesRequest{Model: "test-model", Input: "Hello", ToolChoice: "none"},
			want: "none",
		},
		{
			name: "bare tool_choice auto",
			req:  &core.ResponsesRequest{Model: "test-model", Input: "Hello", ToolChoice: "auto"},
			want: "auto",
		},
		{
			name: "bare tool_choice required",
			req:  &core.ResponsesRequest{Model: "test-model", Input: "Hello", ToolChoice: "required"},
			want: "required",
		},
		{
			name: "unsupported bare tool_choice",
			req:  &core.ResponsesRequest{Model: "test-model", Input: "Hello", ToolChoice: "web_search"},
		},
		{
			name: "function choice without function map",
			req: &core.ResponsesRequest{
				Model:      "test-model",
				Input:      "Hello",
				Tools:      functionTools,
				ToolChoice: map[string]any{"type": "function", "function": "invalid"},
			},
			want: map[string]any{"type": "function", "function": "invalid"},
		},
		{
			name: "function choice without name",
			req: &core.ResponsesRequest{
				Model:      "test-model",
				Input:      "Hello",
				Tools:      functionTools,
				ToolChoice: map[string]any{"type": "function", "function": map[string]any{}},
			},
			want: map[string]any{"type": "function", "function": map[string]any{}},
		},
		{
			name: "function choice with empty name",
			req: &core.ResponsesRequest{
				Model:      "test-model",
				Input:      "Hello",
				Tools:      functionTools,
				ToolChoice: map[string]any{"type": "function", "function": map[string]any{"name": ""}},
			},
			want: map[string]any{"type": "function", "function": map[string]any{"name": ""}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			chatReq, err := ConvertResponsesRequestToChat(tt.req)
			require.NoError(t, err)
			require.Equal(t, tt.want, chatReq.ToolChoice)
		})
	}
}

func TestConvertResponsesRequestToChat_RejectsStatefulAgentsSDKFields(t *testing.T) {
	tests := []struct {
		name string
		req  *core.ResponsesRequest
		want string
	}{
		{
			name: "previous response id",
			req:  &core.ResponsesRequest{Model: "test-model", Input: "Hello", PreviousResponseID: "resp_123"},
			want: "previous_response_id",
		},
		{
			name: "conversation",
			req:  &core.ResponsesRequest{Model: "test-model", Input: "Hello", Conversation: &core.ResponsesConversationRef{ID: "conv_123"}},
			want: "conversation",
		},
		{
			name: "unknown text format type",
			req:  &core.ResponsesRequest{Model: "test-model", Input: "Hello", Text: map[string]any{"format": map[string]any{"type": "grammar"}}},
			want: "text",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ConvertResponsesRequestToChat(tt.req)
			require.Error(t, err)
			require.Contains(t, err.Error(), tt.want)
		})
	}
}

func TestConvertResponsesRequestToChat_IgnoresUnsupportedTools(t *testing.T) {
	req := &core.ResponsesRequest{
		Model: "test-model",
		Input: "Hello",
		Tools: []map[string]any{
			{
				"type": "namespace",
				"name": "multi_agent_v1",
				"tools": []any{
					map[string]any{"type": "function", "name": "spawn_agent"},
				},
			},
			{"type": "web_search"},
			{"type": "file_search", "vector_store_ids": []string{"vs_123"}},
			{
				"type":        "function",
				"name":        "exec_command",
				"description": "Run a command.",
				"parameters":  map[string]any{"type": "object"},
			},
		},
		ToolChoice: map[string]any{"type": "auto"},
	}

	chatReq, err := ConvertResponsesRequestToChat(req)
	require.NoError(t, err)
	require.Len(t, chatReq.Tools, 1)

	function, ok := chatReq.Tools[0]["function"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "exec_command", function["name"], "Tools[0] = %#v, want exec_command function", chatReq.Tools[0])
	require.Equal(t, "auto", chatReq.ToolChoice)
}

func TestConvertResponsesRequestToChat_IgnoresOnlyUnsupportedToolsAndChoice(t *testing.T) {
	parallelToolCalls := true
	req := &core.ResponsesRequest{
		Model: "test-model",
		Input: "Hello",
		Tools: []map[string]any{
			{"type": "namespace", "name": "multi_agent_v1", "tools": []any{}},
			{"type": "web_search"},
		},
		ToolChoice:        map[string]any{"type": "web_search"},
		ParallelToolCalls: &parallelToolCalls,
	}

	chatReq, err := ConvertResponsesRequestToChat(req)
	require.NoError(t, err)
	require.Nil(t, chatReq.Tools)
	require.Nil(t, chatReq.ToolChoice)
	require.Nil(t, chatReq.ParallelToolCalls)
}

func TestConvertResponsesRequestToChat_DropsChoiceForOmittedNamespaceChild(t *testing.T) {
	parallelToolCalls := true
	tests := []struct {
		name       string
		toolChoice map[string]any
		wantChoice bool
	}{
		{
			name:       "omitted namespace child",
			toolChoice: map[string]any{"type": "function", "name": "spawn_agent"},
		},
		{
			name:       "retained function",
			toolChoice: map[string]any{"type": "function", "name": "exec_command"},
			wantChoice: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := &core.ResponsesRequest{
				Model: "test-model",
				Input: "Hello",
				Tools: []map[string]any{
					{
						"type": "namespace",
						"name": "multi_agent_v1",
						"tools": []any{
							map[string]any{"type": "function", "name": "spawn_agent"},
						},
					},
					{
						"type":       "function",
						"name":       "exec_command",
						"parameters": map[string]any{"type": "object"},
					},
				},
				ToolChoice:        tt.toolChoice,
				ParallelToolCalls: &parallelToolCalls,
			}

			chatReq, err := ConvertResponsesRequestToChat(req)
			require.NoError(t, err)
			require.Len(t, chatReq.Tools, 1)

			function, ok := chatReq.Tools[0]["function"].(map[string]any)
			require.True(t, ok)
			require.Equal(t, "exec_command", function["name"], "Tools[0] = %#v, want exec_command function", chatReq.Tools[0])
			require.Equal(t, tt.wantChoice, chatReq.ToolChoice != nil, "ToolChoice = %#v, want present %v", chatReq.ToolChoice, tt.wantChoice)
			require.NotNil(t, chatReq.ParallelToolCalls)
			require.True(t, *chatReq.ParallelToolCalls)
		})
	}
}

func TestConvertResponsesRequestToChat_MapsTextFormatToResponseFormat(t *testing.T) {
	t.Run("json_schema nests schema fields", func(t *testing.T) {
		req := &core.ResponsesRequest{
			Model: "test-model",
			Input: "Hello",
			Text: map[string]any{
				"format": map[string]any{
					"type":   "json_schema",
					"name":   "weather",
					"strict": true,
					"schema": map[string]any{"type": "object"},
				},
				"verbosity": "low",
			},
		}

		chatReq, err := ConvertResponsesRequestToChat(req)
		require.NoError(t, err)

		raw := chatReq.ExtraFields.Lookup("response_format")
		require.NotNil(t, raw)

		var responseFormat struct {
			Type       string `json:"type"`
			JSONSchema struct {
				Name   string         `json:"name"`
				Strict bool           `json:"strict"`
				Schema map[string]any `json:"schema"`
			} `json:"json_schema"`
		}
		err = json.Unmarshal(raw, &responseFormat)
		require.NoError(t, err)
		require.Equal(t, "json_schema", responseFormat.Type)
		require.Equal(t, "weather", responseFormat.JSONSchema.Name)
		require.True(t, responseFormat.JSONSchema.Strict, "response_format.json_schema = %#v, want nested name/strict", responseFormat.JSONSchema)
		require.Equal(t, "object", responseFormat.JSONSchema.Schema["type"], "response_format.json_schema.schema = %#v, want nested schema", responseFormat.JSONSchema.Schema)
		verbosity := chatReq.ExtraFields.Lookup("verbosity")
		require.Equal(t, `"low"`, string(verbosity), "verbosity = %s, want \"low\"", verbosity)
	})

	t.Run("json_object passes through", func(t *testing.T) {
		req := &core.ResponsesRequest{
			Model: "test-model",
			Input: "Hello",
			Text:  map[string]any{"format": map[string]any{"type": "json_object"}},
		}

		chatReq, err := ConvertResponsesRequestToChat(req)
		require.NoError(t, err)
		got := string(chatReq.ExtraFields.Lookup("response_format"))
		require.Equal(t, `{"type":"json_object"}`, got)
	})

	t.Run("plain text produces no response_format", func(t *testing.T) {
		req := &core.ResponsesRequest{
			Model: "test-model",
			Input: "Hello",
			Text:  map[string]any{"format": map[string]any{"type": "text"}},
		}

		chatReq, err := ConvertResponsesRequestToChat(req)
		require.NoError(t, err)
		raw := chatReq.ExtraFields.Lookup("response_format")
		require.Nil(t, raw)
	})
}

func TestConvertResponsesRequestToChat_RejectsUnknownInputItemTypes(t *testing.T) {
	var req core.ResponsesRequest
	err := json.Unmarshal([]byte(`{
		"model":"test-model",
		"input":[{"type":"computer_call","id":"cc_123"}]
	}`), &req)
	require.NoError(t, err)

	_, err = ConvertResponsesRequestToChat(&req)
	require.Error(t, err)
	require.Contains(t, err.Error(), `unsupported input item type "computer_call"`)
}

// Reasoning from an ordinary assistant turn is accepted but omitted because
// chat providers do not need it on the following user turn.
func TestConvertResponsesRequestToChat_DropsReasoningWithoutToolCall(t *testing.T) {
	var req core.ResponsesRequest
	err := json.Unmarshal([]byte(`{
		"model":"test-model",
		"input":[
			{"type":"message","role":"user","content":"hello"},
			{"type":"reasoning","id":"rs_123","summary":[{"type":"summary_text","text":"thinking..."}]},
			{"type":"message","role":"assistant","content":"hi there"}
		]
	}`), &req)
	require.NoError(t, err)

	chatReq, err := ConvertResponsesRequestToChat(&req)
	require.NoError(t, err)
	require.Len(t, chatReq.Messages, 2)
	require.Equal(t, "user", chatReq.Messages[0].Role)
	require.Equal(t, "assistant", chatReq.Messages[1].Role, "Messages = %#v, want [user, assistant]", chatReq.Messages)
	got := chatReq.Messages[1].ExtraFields.Lookup("reasoning_content")
	require.Nil(t, got)
}

// DeepSeek requires reasoning_content to be replayed on the assistant message
// that made a tool call. Codex echoes Responses output items back as input, so
// the reasoning item and function-call item must be reassembled here.
func TestConvertResponsesRequestToChat_ReplaysReasoningWithToolCall(t *testing.T) {
	var req core.ResponsesRequest
	err := json.Unmarshal([]byte(`{
		"model":"deepseek-v4-pro",
		"input":[
			{"type":"message","role":"user","content":"weather?"},
			{"type":"reasoning","id":"rs_123","summary":[],"content":[{"type":"reasoning_text","text":"Need to check the weather."}]},
			{"type":"message","id":"msg_123","role":"assistant","content":"I'll check."},
			{"type":"function_call","call_id":"call_123","name":"lookup_weather","arguments":"{\"city\":\"Warsaw\"}"},
			{"type":"function_call_output","call_id":"call_123","output":"sunny"}
		]
	}`), &req)
	require.NoError(t, err)

	chatReq, err := ConvertResponsesRequestToChat(&req)
	require.NoError(t, err)
	require.Len(t, chatReq.Messages, 3)

	assistant := chatReq.Messages[1]
	require.Equal(t, "assistant", assistant.Role)
	require.Equal(t, "I'll check.", core.ExtractTextContent(assistant.Content))
	require.Len(t, assistant.ToolCalls, 1, "assistant message = %#v, want merged text and tool call", assistant)

	var reasoning string
	err = json.Unmarshal(assistant.ExtraFields.Lookup("reasoning_content"), &reasoning)
	require.NoError(t, err)
	require.Equal(t, "Need to check the weather.", reasoning)
	require.Equal(t, "tool", chatReq.Messages[2].Role)
	require.Equal(t, "call_123", chatReq.Messages[2].ToolCallID, "tool message = %#v", chatReq.Messages[2])
}

func TestConvertResponsesRequestToChat_NormalizesDeveloperRole(t *testing.T) {
	tests := map[string]any{
		"typed": []core.ResponsesInputElement{{Type: "message", Role: "developer", Content: "Be concise."}},
		"map":   []any{map[string]any{"type": "message", "role": "developer", "content": "Be concise."}},
	}
	for name, input := range tests {
		t.Run(name, func(t *testing.T) {
			chatReq, err := ConvertResponsesRequestToChat(&core.ResponsesRequest{Model: "test-model", Input: input})
			require.NoError(t, err)
			require.Len(t, chatReq.Messages, 1)
			require.Equal(t, "system", chatReq.Messages[0].Role)
		})
	}
}

func TestConvertResponsesRequestToChat_DoesNotMergeAssistantMessagesWithExtraFields(t *testing.T) {
	req := &core.ResponsesRequest{
		Model: "test-model",
		Input: []core.ResponsesInputElement{
			{
				Type:        "message",
				Role:        "assistant",
				Content:     "first",
				ExtraFields: core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{"x_first": json.RawMessage(`true`)}),
			},
			{
				Type:        "message",
				Role:        "assistant",
				Content:     "second",
				ExtraFields: core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{"x_second": json.RawMessage(`true`)}),
			},
		},
	}

	chatReq, err := ConvertResponsesRequestToChat(req)
	require.NoError(t, err)
	require.Len(t, chatReq.Messages, 2)
	require.NotNil(t, chatReq.Messages[0].ExtraFields.Lookup("x_first"))
	require.NotNil(t, chatReq.Messages[1].ExtraFields.Lookup("x_second"))
}

func TestConvertResponsesRequestToChat_RejectsWhitespaceOnlyMediaFields(t *testing.T) {
	tests := []struct {
		name  string
		input any
	}{
		{
			name: "typed image url",
			input: []core.ResponsesInputElement{
				{
					Type: "message",
					Role: "user",
					Content: []core.ContentPart{
						{
							Type:     "image_url",
							ImageURL: &core.ImageURLContent{URL: "   "},
						},
					},
				},
			},
		},
		{
			name: "map input audio",
			input: []any{
				map[string]any{
					"type": "message",
					"role": "user",
					"content": []map[string]any{
						{
							"type":        "input_audio",
							"input_audio": map[string]any{"data": "  ", "format": "wav"},
						},
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ConvertResponsesRequestToChat(&core.ResponsesRequest{
				Model: "test-model",
				Input: tt.input,
			})
			require.Error(t, err)
			require.Contains(t, err.Error(), "unsupported content")
		})
	}
}

func TestConvertResponsesRequestToChat_PreservesOpaqueExtras(t *testing.T) {
	req := &core.ResponsesRequest{
		Model: "test-model",
		Input: []core.ResponsesInputElement{
			{
				Role: "user",
				Content: []core.ContentPart{
					{
						Type: "input_text",
						Text: "Describe this",
						ExtraFields: core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{
							"cache_control": json.RawMessage(`{"type":"ephemeral"}`),
						}),
					},
				},
				ExtraFields: core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{
					"x_message_hint": json.RawMessage(`true`),
				}),
			},
		},
		ExtraFields: core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{
			"response_format": json.RawMessage(`{"type":"json_schema"}`),
		}),
	}

	chatReq, err := ConvertResponsesRequestToChat(req)
	require.NoError(t, err)
	require.NotNil(t, chatReq.ExtraFields.Lookup("response_format"))
	require.Len(t, chatReq.Messages, 1)
	require.NotNil(t, chatReq.Messages[0].ExtraFields.Lookup("x_message_hint"))

	parts, ok := chatReq.Messages[0].Content.([]core.ContentPart)
	require.True(t, ok, "Messages[0].Content type = %T, want []core.ContentPart to preserve part extras", chatReq.Messages[0].Content)
	require.NotNil(t, parts[0].ExtraFields.Lookup("cache_control"))
}

func TestConvertResponsesRequestToChat_PreservesUnknownMapFields(t *testing.T) {
	req := &core.ResponsesRequest{
		Model: "test-model",
		Input: []any{
			map[string]any{
				"type":      "function_call",
				"call_id":   "call_123",
				"name":      "lookup_weather",
				"arguments": `{"city":"Warsaw"}`,
				"x_trace":   map[string]any{"attempt": 2},
			},
			map[string]any{
				"type":    "message",
				"role":    "user",
				"content": []map[string]any{{"type": "output_text", "text": "hello", "cache_control": map[string]any{"type": "ephemeral"}}},
				"x_meta":  "keep-me",
			},
			map[string]any{
				"type": "message",
				"role": "user",
				"content": []any{
					map[string]any{
						"type": "input_image",
						"image_url": map[string]string{
							"url":        "https://example.com/image.png",
							"detail":     "high",
							"media_type": "image/png",
							"x_nested":   "keep-image",
						},
					},
					map[string]any{
						"type": "input_audio",
						"input_audio": map[string]string{
							"data":     "aGVsbG8=",
							"format":   "wav",
							"x_nested": "keep-audio",
						},
					},
				},
			},
		},
	}

	chatReq, err := ConvertResponsesRequestToChat(req)
	require.NoError(t, err)
	require.Len(t, chatReq.Messages, 3)
	require.Len(t, chatReq.Messages[0].ToolCalls, 1)
	require.NotNil(t, chatReq.Messages[0].ToolCalls[0].ExtraFields.Lookup("x_trace"))
	require.NotNil(t, chatReq.Messages[1].ExtraFields.Lookup("x_meta"))

	parts, ok := chatReq.Messages[1].Content.([]core.ContentPart)
	require.True(t, ok, "Messages[1].Content type = %T, want []core.ContentPart to preserve mapped text-part extras", chatReq.Messages[1].Content)
	require.NotNil(t, parts[0].ExtraFields.Lookup("cache_control"))

	multimodalParts, ok := chatReq.Messages[2].Content.([]core.ContentPart)
	require.True(t, ok)
	require.Len(t, multimodalParts, 2, "Messages[2].Content = %#v, want []core.ContentPart len=2", chatReq.Messages[2].Content)
	require.NotNil(t, multimodalParts[0].ImageURL)
	require.NotNil(t, multimodalParts[0].ImageURL.ExtraFields.Lookup("x_nested"))
	require.NotNil(t, multimodalParts[1].InputAudio)
	require.NotNil(t, multimodalParts[1].InputAudio.ExtraFields.Lookup("x_nested"))
}

func TestConvertResponsesRequestToChat_InputAudioDataURIWithoutFormat(t *testing.T) {
	const dataURI = "data:audio/wav;base64,UklGRg=="
	req := &core.ResponsesRequest{
		Model: "mimo-v2.5-asr",
		Input: []any{
			map[string]any{
				"type": "message",
				"role": "user",
				"content": []any{
					map[string]any{
						"type":        "input_audio",
						"input_audio": map[string]any{"data": dataURI},
					},
				},
			},
		},
	}

	chatReq, err := ConvertResponsesRequestToChat(req)
	require.NoError(t, err)

	parts, ok := chatReq.Messages[0].Content.([]core.ContentPart)
	require.True(t, ok)
	require.Len(t, parts, 1)
	require.NotNil(t, parts[0].InputAudio, "Messages[0].Content = %#v, want one input_audio part", chatReq.Messages[0].Content)
	require.Equal(t, dataURI, parts[0].InputAudio.Data)
	require.Empty(t, parts[0].InputAudio.Format, "InputAudio = %+v, want data URI with empty format", parts[0].InputAudio)
}

func TestConvertChatResponseToResponses(t *testing.T) {
	resp := &core.ChatResponse{
		ID:      "chatcmpl-123",
		Object:  "chat.completion",
		Model:   "test-model",
		Created: 1677652288,
		Choices: []core.Choice{
			{
				Index: 0,
				Message: core.ResponseMessage{
					Role:    "assistant",
					Content: "Hello! How can I help you today?",
					ToolCalls: []core.ToolCall{
						{
							ID:   "call_123",
							Type: "function",
							Function: core.FunctionCall{
								Name:      "lookup_weather",
								Arguments: `{"city":"Warsaw"}`,
							},
						},
					},
				},
				FinishReason: "tool_calls",
			},
		},
		Usage: core.Usage{
			PromptTokens:     10,
			CompletionTokens: 20,
			TotalTokens:      30,
			PromptTokensDetails: &core.PromptTokensDetails{
				CachedTokens: 1,
			},
			CompletionTokensDetails: &core.CompletionTokensDetails{
				ReasoningTokens: 3,
			},
			RawUsage: map[string]any{"provider": "test"},
		},
	}

	result := ConvertChatResponseToResponses(resp)

	require.Len(t, result.Output, 2)
	require.Equal(t, "message", result.Output[0].Type)
	require.Equal(t, "function_call", result.Output[1].Type, "unexpected output items: %+v", result.Output)
	require.Equal(t, "call_123", result.Output[1].CallID)
	require.NotNil(t, result.Usage)
	require.NotNil(t, result.Usage.PromptTokensDetails)
	require.NotNil(t, result.Usage.CompletionTokensDetails)
	require.Equal(t, "test", result.Usage.RawUsage["provider"], "RawUsage = %+v, want provider=test", result.Usage.RawUsage)
}

func TestConvertChatResponseToResponses_PreservesRawReasoning(t *testing.T) {
	resp := &core.ChatResponse{
		ID:      "chatcmpl-reasoning",
		Model:   "deepseek-v4-pro",
		Created: 1,
		Choices: []core.Choice{{
			Message: core.ResponseMessage{
				Role:    "assistant",
				Content: "done",
				ExtraFields: core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{
					"reasoning_content": json.RawMessage(`"raw trace"`),
				}),
			},
		}},
	}

	result := ConvertChatResponseToResponses(resp)
	require.Len(t, result.Output, 2)
	require.Equal(t, "reasoning", result.Output[0].Type)
	require.Equal(t, "message", result.Output[1].Type)

	reasoning := result.Output[0]
	require.Len(t, reasoning.Content, 1)
	require.Equal(t, "reasoning_text", reasoning.Content[0].Type)
	require.Equal(t, "raw trace", reasoning.Content[0].Text)
	require.NotNil(t, reasoning.ExtraFields.Lookup("summary"))
}

func TestConvertChatResponseToResponses_PreservesStructuredAssistantContent(t *testing.T) {
	resp := &core.ChatResponse{
		ID:      "chatcmpl-structured",
		Object:  "chat.completion",
		Model:   "test-model",
		Created: 1677652288,
		Choices: []core.Choice{
			{
				Index: 0,
				Message: core.ResponseMessage{
					Role: "assistant",
					Content: []core.ContentPart{
						{Type: "text", Text: "Here is the result."},
						{
							Type: "image_url",
							ImageURL: &core.ImageURLContent{
								URL:         "https://example.com/result.png",
								ExtraFields: core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{"x_image": json.RawMessage(`true`)}),
							},
						},
						{
							Type: "input_audio",
							InputAudio: &core.InputAudioContent{
								Data:        "YWJj",
								Format:      "wav",
								ExtraFields: core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{"x_audio": json.RawMessage(`true`)}),
							},
						},
					},
				},
				FinishReason: "stop",
			},
		},
	}

	result := ConvertChatResponseToResponses(resp)

	require.Len(t, result.Output, 1)
	require.Equal(t, "message", result.Output[0].Type)
	require.Len(t, result.Output[0].Content, 3)
	require.Equal(t, "output_text", result.Output[0].Content[0].Type)
	require.Equal(t, "Here is the result.", result.Output[0].Content[0].Text, "unexpected text content item: %+v", result.Output[0].Content[0])
	require.Equal(t, "input_image", result.Output[0].Content[1].Type, "expected preserved non-text content item, got %+v", result.Output[0].Content[1])
	require.NotNil(t, result.Output[0].Content[1].ImageURL)
	require.Equal(t, "https://example.com/result.png", result.Output[0].Content[1].ImageURL.URL, "unexpected preserved image content item: %+v", result.Output[0].Content[1])
	require.NotNil(t, result.Output[0].Content[1].ImageURL.ExtraFields.Lookup("x_image"), "image extra missing after conversion: %+v", result.Output[0].Content[1].ImageURL)
	require.Equal(t, "input_audio", result.Output[0].Content[2].Type, "expected preserved audio content item, got %+v", result.Output[0].Content[2])
	require.NotNil(t, result.Output[0].Content[2].InputAudio)
	require.Equal(t, "wav", result.Output[0].Content[2].InputAudio.Format, "unexpected preserved audio content item: %+v", result.Output[0].Content[2])
	require.NotNil(t, result.Output[0].Content[2].InputAudio.ExtraFields.Lookup("x_audio"), "audio extra missing after conversion: %+v", result.Output[0].Content[2].InputAudio)
}

func TestConvertResponsesRequestToChat_RejectsNonSerializableFunctionCallOutputMap(t *testing.T) {
	_, err := ConvertResponsesRequestToChat(&core.ResponsesRequest{
		Model: "test-model",
		Input: []any{
			map[string]any{
				"type":    "function_call_output",
				"call_id": "call_123",
				"output":  math.Inf(1),
			},
		},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "function_call_output.output must be JSON-serializable")
}

func TestExtractContentFromInput(t *testing.T) {
	tests := []struct {
		name     string
		input    any
		expected string
	}{
		{name: "string input", input: "Hello world", expected: "Hello world"},
		{
			name: "nested content",
			input: []map[string]any{
				{
					"type": "message",
					"content": []map[string]any{
						{"type": "output_text", "text": "Hello"},
						{"type": "wrapper", "content": []any{map[string]any{"type": "output_text", "text": "world"}}},
					},
				},
			},
			expected: "Hello world",
		},
		{name: "unsupported type", input: 12345, expected: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractContentFromInput(tt.input)
			require.Equal(t, tt.expected, got, "ExtractContentFromInput(%v)", tt.input)
		})
	}
}

func TestConvertResponsesRequestToChat_ClonesStreamOptions(t *testing.T) {
	req := &core.ResponsesRequest{
		Model:         "test-model",
		Input:         "hello",
		Stream:        true,
		StreamOptions: &core.StreamOptions{IncludeUsage: false},
	}

	chatReq, err := ConvertResponsesRequestToChat(req)
	require.NoError(t, err)
	require.NotNil(t, chatReq.StreamOptions)
	require.NotSame(t, req.StreamOptions, chatReq.StreamOptions)
	require.False(t, chatReq.StreamOptions.IncludeUsage)
}

func TestStreamResponsesViaChat_InjectsUsageWhenPolicyEnabled(t *testing.T) {
	provider := &capturingChatProvider{
		streamData: "data: [DONE]\n\n",
	}
	req := &core.ResponsesRequest{
		Model:         "gemini-2.0-flash",
		Input:         "hello",
		Stream:        true,
		StreamOptions: &core.StreamOptions{IncludeUsage: false},
	}
	ctx := core.WithEnforceReturningUsageData(context.Background(), true)

	stream, err := StreamResponsesViaChat(ctx, provider, req, "gemini")
	require.NoError(t, err)

	defer func() {
		_ = stream.Close()
	}()

	require.NotNil(t, provider.capturedReq)
	require.NotNil(t, provider.capturedReq.StreamOptions)
	require.True(t, provider.capturedReq.StreamOptions.IncludeUsage)
	require.NotNil(t, req.StreamOptions)
	require.False(t, req.StreamOptions.IncludeUsage)
}

func TestStreamResponsesViaChat_DoesNotInjectUsageWhenPolicyDisabled(t *testing.T) {
	provider := &capturingChatProvider{
		streamData: "data: [DONE]\n\n",
	}
	req := &core.ResponsesRequest{
		Model:  "gemini-2.0-flash",
		Input:  "hello",
		Stream: true,
	}

	stream, err := StreamResponsesViaChat(context.Background(), provider, req, "gemini")
	require.NoError(t, err)

	defer func() {
		_ = stream.Close()
	}()

	require.NotNil(t, provider.capturedReq)
	require.Nil(t, provider.capturedReq.StreamOptions)
}

func TestResponsesViaChatRejectsEmptyChatResponse(t *testing.T) {
	tests := []struct {
		name        string
		chatResp    *core.ChatResponse
		wantMessage string
	}{
		{name: "nil response", wantMessage: "provider returned empty response"},
		{
			name:        "no choices",
			chatResp:    &core.ChatResponse{ID: "chatcmpl-1"},
			wantMessage: "provider returned no choices",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := &capturingChatProvider{chatResp: tt.chatResp}

			resp, err := ResponsesViaChat(context.Background(), provider, &core.ResponsesRequest{Model: "m", Input: "hi"}, "groq")
			require.Nil(t, resp)

			var gatewayErr *core.GatewayError
			require.ErrorAs(t, err, &gatewayErr)
			assert.Equal(t, http.StatusBadGateway, gatewayErr.HTTPStatusCode())
			assert.Equal(t, tt.wantMessage, gatewayErr.Message)
			assert.Equal(t, "groq", gatewayErr.Provider)
		})
	}
}

func TestConvertResponsesRequestToChat_DropsResponsesOnlyTextMembers(t *testing.T) {
	tests := []struct {
		name  string
		input any
	}{
		{name: "map", input: []any{
			map[string]any{
				"type": "message",
				"role": "assistant",
				"content": []any{map[string]any{
					"type": "output_text", "text": "OK", "annotations": []any{}, "logprobs": []any{},
					"cache_control": map[string]any{"type": "ephemeral"},
				}},
			},
		}},
		{name: "typed", input: []core.ResponsesInputElement{{
			Role: "assistant",
			Content: []core.ContentPart{{
				Type: "output_text",
				Text: "OK",
				ExtraFields: core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{
					"annotations":   json.RawMessage(`[]`),
					"logprobs":      json.RawMessage(`[]`),
					"cache_control": json.RawMessage(`{"type":"ephemeral"}`),
				}),
			}},
		}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			chatReq, err := ConvertResponsesRequestToChat(&core.ResponsesRequest{Model: "test-model", Input: tt.input})
			require.NoError(t, err)

			parts, ok := chatReq.Messages[0].Content.([]core.ContentPart)
			require.True(t, ok, "Content type = %T, want []core.ContentPart", chatReq.Messages[0].Content)

			extras := parts[0].ExtraFields
			require.Nil(t, extras.Lookup("annotations"))
			require.Nil(t, extras.Lookup("logprobs"), "Responses-only members forwarded to chat: %+v", parts[0])
			require.NotNil(t, extras.Lookup("cache_control"))
		})
	}
}

// Replayed Responses output items always carry an "id". Chat providers such as
// Groq and Fireworks reject an unknown "id" member on a message, so it must not
// survive the translation.
func TestConvertResponsesRequestToChat_DropsReplayedItemIDs(t *testing.T) {
	const replay = `[
		{"type":"message","id":"msg_1","status":"completed","role":"assistant",
		 "content":[{"type":"output_text","text":"Let me check.","annotations":[]}],
		 "cache_control":{"type":"ephemeral"}},
		{"type":"function_call","id":"fc_1","call_id":"call_1","name":"get_weather","arguments":"{}","status":"completed"},
		{"type":"function_call_output","id":"fco_1","call_id":"call_1","output":"18C","status":"completed",
		 "cache_control":{"type":"ephemeral"}}
	]`

	var typed []core.ResponsesInputElement
	err := json.Unmarshal([]byte(replay), &typed)
	require.NoError(t, err)

	var maps []any
	err = json.Unmarshal([]byte(replay), &maps)
	require.NoError(t, err)

	tests := []struct {
		name  string
		input any
	}{
		{name: "typed", input: typed},
		{name: "map", input: maps},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			chatReq, err := ConvertResponsesRequestToChat(&core.ResponsesRequest{Model: "test-model", Input: tt.input})
			require.NoError(t, err)
			require.Len(t, chatReq.Messages, 2)

			assistant, tool := chatReq.Messages[0], chatReq.Messages[1]
			require.Equal(t, "assistant", assistant.Role)
			require.Equal(t, "tool", tool.Role)
			assert.Nil(t, assistant.ExtraFields.Lookup("id"))
			assert.Nil(t, tool.ExtraFields.Lookup("id"))
			assert.Len(t, assistant.ToolCalls, 1)
			assert.Nil(t, assistant.ToolCalls[0].ExtraFields.Lookup("id"))
			got := // Only the Responses-only members go; call ids and other unknown
				// members still reach the provider.
				assistant.ToolCalls[0].ID
			assert.Equal(t, "call_1", got)
			got = tool.ToolCallID
			assert.Equal(t, "call_1", got)
			assert.NotNil(t, assistant.ExtraFields.Lookup("cache_control"))
			assert.NotNil(t, tool.ExtraFields.Lookup("cache_control"))
		})
	}
}
