package cohere

import (
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
)

// sseBody joins SSE lines the way Cohere's stream emits them.
func sseBody(lines ...string) string {
	return strings.Join(lines, "\n")
}

func TestChatCompletionTranslatesRequestAndResponse(t *testing.T) {
	var operation string
	server, capture := providertest.JSONServer(t, http.StatusOK, `{
		"id":"cohere-id",
		"finish_reason":"TOOL_CALL",
		"message":{
			"role":"assistant",
			"content":[
				{"type":"thinking","thinking":"check the weather"},
				{"type":"text","text":"I will check."}
			],
			"tool_plan":"Use the weather tool.",
			"tool_calls":[
				{"id":"call-2","type":"function","function":{"name":"weather","arguments":"{\"city\":\"Warsaw\"}"}}
			],
			"citations":[{"start":0,"end":1,"text":"I"}]
		},
		"usage":{
			"billed_units":{"input_tokens":10,"output_tokens":3},
			"tokens":{"input_tokens":12,"image_tokens":5,"output_tokens":4},
			"cached_tokens":2
		}
	}`)

	provider := NewWithHTTPClient("test-key", server.URL, server.Client(), llmclient.Hooks{
		OnRequestStart: func(ctx context.Context, info llmclient.RequestInfo) context.Context {
			operation = info.Operation
			return ctx
		},
	})
	req := &core.ChatRequest{
		Model: "command-a-03-2025",
		Messages: []core.Message{
			{Role: "developer", Content: []core.ContentPart{{Type: "text", Text: "Be brief."}}},
			{Role: "user", Content: []core.ContentPart{
				{Type: "text", Text: "Weather?"},
				{Type: "image_url", ImageURL: &core.ImageURLContent{URL: "https://example.com/map.png", Detail: "low"}},
			}},
			{
				Role:    "assistant",
				Content: "",
				ToolCalls: []core.ToolCall{{
					ID:   "call-1",
					Type: "function",
					Function: core.FunctionCall{
						Name:      "weather",
						Arguments: `{"city":"Warsaw"}`,
					},
				}},
			},
			{Role: "tool", ToolCallID: "call-1", Content: "sunny"},
		},
		Tools: []map[string]any{{
			"type": "function",
			"function": map[string]any{
				"name":        "weather",
				"description": "Get weather",
				"parameters":  map[string]any{"type": "object"},
			},
		}},
		ToolChoice: map[string]any{
			"type":     "function",
			"function": map[string]any{"name": "weather"},
		},
		ExtraFields: core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{
			"stop":                  json.RawMessage(`"END"`),
			"max_completion_tokens": json.RawMessage(`250`),
			"response_format": json.RawMessage(`{
				"type":"json_schema",
				"json_schema":{
					"name":"weather",
					"strict":true,
					"schema":{"type":"object","properties":{"summary":{"type":"string"}}}
				}
			}`),
			"top_k":    json.RawMessage(`20`),
			"thinking": json.RawMessage(`{"type":"enabled","token_budget":500}`),
		}),
	}

	resp, err := provider.ChatCompletion(context.Background(), req)
	require.NoError(t, err)

	sent := capture.Last(t)
	assert.Equal(t, "/v2/chat", sent.Path)
	assert.Equal(t, "Bearer test-key", sent.Header.Get("Authorization"))
	assert.Equal(t, "GoModel", sent.Header.Get("X-Client-Name"))
	assert.Equal(t, llmclient.OperationChat, operation)

	captured := sent.JSON(t)
	assert.Equal(t, req.Model, captured["model"])
	require.Contains(t, captured, "stream")
	streamed, _ := captured["stream"].(bool)
	assert.False(t, streamed)

	messages := captured["messages"].([]any)
	assert.Equal(t, "system", messages[0].(map[string]any)["role"], "developer role maps to system")
	assert.Equal(t, "Be brief.", messages[0].(map[string]any)["content"], "developer content becomes a Cohere system string")

	userContent := messages[1].(map[string]any)["content"].([]any)
	image := userContent[1].(map[string]any)
	assert.Equal(t, "image_url", image["type"])

	toolResult := messages[3].(map[string]any)["content"].(string)
	assert.Equal(t, `{"result":"sunny"}`, toolResult)
	assert.Equal(t, "REQUIRED", captured["tool_choice"])
	assert.Equal(t, float64(250), captured["max_tokens"])
	assert.Equal(t, []any{"END"}, captured["stop_sequences"])
	assert.Equal(t, float64(20), captured["k"])

	responseFormat := captured["response_format"].(map[string]any)
	assert.Equal(t, "json_object", responseFormat["type"])
	jsonSchema, ok := responseFormat["json_schema"].(map[string]any)
	require.True(t, ok, "response_format.json_schema = %#v, want translated schema", responseFormat["json_schema"])
	assert.Equal(t, "object", jsonSchema["type"])
	assert.NotContains(t, responseFormat, "name")

	assert.Equal(t, "cohere-id", resp.ID)
	assert.Equal(t, req.Model, resp.Model)
	assert.Equal(t, "cohere", resp.Provider)
	require.Len(t, resp.Choices, 1)
	assert.Equal(t, "tool_calls", resp.Choices[0].FinishReason)
	assert.Equal(t, "I will check.", resp.Choices[0].Message.Content)
	assert.Equal(t, "check the weather", rawString(resp.Choices[0].Message.ExtraFields.Lookup("reasoning_content")))
	require.Len(t, resp.Choices[0].Message.ToolCalls, 1)
	assert.Equal(t, `{"city":"Warsaw"}`, resp.Choices[0].Message.ToolCalls[0].Function.Arguments)
	assert.Equal(t, 17, resp.Usage.PromptTokens)
	assert.Equal(t, 4, resp.Usage.CompletionTokens)
	assert.Equal(t, 21, resp.Usage.TotalTokens)
	require.NotNil(t, resp.Usage.PromptTokensDetails)
	assert.Equal(t, 2, resp.Usage.PromptTokensDetails.CachedTokens)
	assert.Equal(t, 5, resp.Usage.PromptTokensDetails.ImageTokens)
}

func TestChatCompletionReturnsCohereGenerationFailures(t *testing.T) {
	tests := []struct {
		finishReason string
		wantStatus   int
		wantMessage  string
	}{
		{finishReason: "ERROR", wantStatus: http.StatusBadGateway, wantMessage: "generation failed"},
		{finishReason: "TIMEOUT", wantStatus: http.StatusGatewayTimeout, wantMessage: "generation timed out"},
	}

	for _, tt := range tests {
		t.Run(tt.finishReason, func(t *testing.T) {
			server, _ := providertest.JSONServer(t, http.StatusOK, `{
				"id":"failed-generation",
				"finish_reason":"`+tt.finishReason+`",
				"message":{"role":"assistant","content":[]},
				"usage":{}
			}`)

			provider := NewWithHTTPClient("key", server.URL, server.Client(), llmclient.Hooks{})
			resp, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
				Model:    "command-a",
				Messages: []core.Message{{Role: "user", Content: "hello"}},
			})
			require.Nil(t, resp)

			var gatewayErr *core.GatewayError
			require.ErrorAs(t, err, &gatewayErr)
			assert.Equal(t, core.ErrorTypeProvider, gatewayErr.Type)
			assert.Equal(t, tt.wantStatus, gatewayErr.StatusCode)
			assert.Contains(t, strings.ToLower(gatewayErr.Message), tt.wantMessage)
		})
	}
}

func TestStreamChatCompletionConvertsCohereEvents(t *testing.T) {
	server, capture := providertest.SSEServer(t, sseBody(
		`event: message-start`,
		`data: {"type":"message-start","id":"stream-id","delta":{"message":{"role":"assistant","content":[],"tool_calls":[],"citations":[]}}}`,
		``,
		`event: content-delta`,
		`data: {"type":"content-delta","index":0,"delta":{"message":{"content":{"thinking":"consider"}}}}`,
		``,
		`event: content-delta`,
		`data: {"type":"content-delta","index":0,"delta":{"message":{"content":{"text":"Hello"}}}}`,
		``,
		`event: tool-plan-delta`,
		`data: {"type":"tool-plan-delta","delta":{"message":{"tool_plan":"Use lookup."}}}`,
		``,
		`event: tool-call-start`,
		`data: {"type":"tool-call-start","index":0,"delta":{"message":{"tool_calls":{"id":"call-1","type":"function","function":{"name":"lookup","arguments":""}}}}}`,
		``,
		`event: tool-call-delta`,
		`data: {"type":"tool-call-delta","index":0,"delta":{"message":{"tool_calls":{"function":{"arguments":"{\"q\":\"x\"}"}}}}}`,
		``,
		`event: citation-start`,
		`data: {"type":"citation-start","index":0,"delta":{"message":{"citations":{"start":0,"end":5,"text":"Hello","sources":[{"type":"document","id":"doc-1"}]}}}}`,
		``,
		`event: citation-end`,
		`data: {"type":"citation-end","index":0}`,
		``,
		`event: message-end`,
		`data: {"type":"message-end","delta":{"finish_reason":"TOOL_CALL","usage":{"tokens":{"input_tokens":8,"output_tokens":3},"cached_tokens":1}}}`,
		``,
	))

	provider := NewWithHTTPClient("key", server.URL, server.Client(), llmclient.Hooks{})
	stream, err := provider.StreamChatCompletion(context.Background(), &core.ChatRequest{
		Model:         "command-a",
		Messages:      []core.Message{{Role: "user", Content: "hello"}},
		StreamOptions: &core.StreamOptions{IncludeUsage: true},
	})
	require.NoError(t, err)
	defer stream.Close()

	body, err := io.ReadAll(stream)
	require.NoError(t, err)

	streamed, _ := capture.Last(t).JSON(t)["stream"].(bool)
	assert.True(t, streamed)

	output := string(body)
	for _, want := range []string{
		`"id":"stream-id"`,
		`"role":"assistant"`,
		`"reasoning_content":"consider"`,
		`"content":"Hello"`,
		`"tool_plan":"Use lookup."`,
		`"name":"lookup"`,
		`"arguments":"{\"q\":\"x\"}"`,
		`"citations":[{"start":0,"end":5,"text":"Hello","sources":[{"type":"document","id":"doc-1"}]}]`,
		`"finish_reason":"tool_calls"`,
		`"prompt_tokens":8`,
		`"cached_tokens":1`,
		"data: [DONE]",
	} {
		assert.Contains(t, output, want)
	}
}

func TestStreamChatCompletionReturnsGenerationFailure(t *testing.T) {
	server, _ := providertest.SSEServer(t, sseBody(
		`data: {"type":"message-start","id":"stream-id","delta":{"message":{"role":"assistant"}}}`,
		``,
		`data: {"type":"message-end","delta":{"finish_reason":"TIMEOUT"}}`,
		``,
	))

	provider := NewWithHTTPClient("key", server.URL, server.Client(), llmclient.Hooks{})
	stream, err := provider.StreamChatCompletion(context.Background(), &core.ChatRequest{
		Model:    "command-a",
		Messages: []core.Message{{Role: "user", Content: "hello"}},
	})
	require.NoError(t, err)
	defer stream.Close()

	body, err := io.ReadAll(stream)
	require.NoError(t, err)

	output := string(body)
	assert.Contains(t, output, `"type":"provider_error"`)
	assert.Contains(t, output, `"message":"Cohere generation timed out"`)
	assert.NotContains(t, output, `"finish_reason":"stop"`, "stream fabricated a successful stop finish")
}

func TestStreamResponsesPropagatesGenerationFailure(t *testing.T) {
	server, _ := providertest.SSEServer(t, sseBody(
		`data: {"type":"message-start","id":"stream-id","delta":{"message":{"role":"assistant"}}}`,
		``,
		`data: {"type":"message-end","delta":{"finish_reason":"ERROR","error":"capacity exhausted"}}`,
		``,
	))

	provider := NewWithHTTPClient("key", server.URL, server.Client(), llmclient.Hooks{})
	stream, err := provider.StreamResponses(context.Background(), &core.ResponsesRequest{
		Model: "command-a",
		Input: "hello",
	})
	require.NoError(t, err)
	defer stream.Close()

	body, err := io.ReadAll(stream)
	require.NoError(t, err)

	output := string(body)
	assert.Contains(t, output, "event: response.failed")
	assert.Contains(t, output, `"status":"failed"`)
	assert.Contains(t, output, `"message":"capacity exhausted"`)
	assert.NotContains(t, output, "event: response.completed", "Responses stream fabricated response.completed")
}

func TestStreamResponsesPropagatesAdapterFailures(t *testing.T) {
	tests := []struct {
		name               string
		body               string
		contentLengthExtra int
		wantMessage        string
	}{
		{
			name: "malformed event",
			body: sseBody(
				`data: {"type":"message-start","id":"stream-id","delta":{"message":{"role":"assistant"}}}`,
				``,
				`data: {not-json}`,
				``,
			),
			wantMessage: "failed to parse Cohere stream event",
		},
		{
			name: "upstream read failure",
			body: sseBody(
				`data: {"type":"message-start","id":"stream-id","delta":{"message":{"role":"assistant"}}}`,
				``,
				`data: {"type":"content-delta","delta":{"message":{"content":{"text":"partial"}}}}`,
				``,
			),
			contentLengthExtra: 100,
			wantMessage:        "failed to read Cohere stream",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// An overstated Content-Length makes the client's read fail
			// partway, which must surface as a terminal failure event.
			server, _ := providertest.Server(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				if tt.contentLengthExtra > 0 {
					w.Header().Set("Content-Length", strconv.Itoa(len(tt.body)+tt.contentLengthExtra))
				}
				_, _ = io.WriteString(w, tt.body)
			})

			provider := NewWithHTTPClient("key", server.URL, server.Client(), llmclient.Hooks{})
			stream, err := provider.StreamResponses(context.Background(), &core.ResponsesRequest{
				Model: "command-a",
				Input: "hello",
			})
			require.NoError(t, err)
			defer stream.Close()

			body, err := io.ReadAll(stream)
			require.NoError(t, err)

			output := string(body)
			assert.Contains(t, output, "event: response.failed")
			assert.Contains(t, output, `"status":"failed"`)
			assert.Contains(t, output, `"message":"`+tt.wantMessage+`"`)
			assert.Contains(t, output, "data: [DONE]")
			assert.NotContains(t, output, "event: response.completed", "Responses stream fabricated response.completed")
		})
	}
}

func TestStreamChatCompletionDoesNotTurnIncompleteStreamIntoSuccess(t *testing.T) {
	server, _ := providertest.SSEServer(t, "data: {\"type\":\"content-delta\",\"delta\":{\"message\":{\"content\":{\"text\":\"partial\"}}}}\n\n")

	provider := NewWithHTTPClient("key", server.URL, server.Client(), llmclient.Hooks{})
	stream, err := provider.StreamChatCompletion(context.Background(), &core.ChatRequest{
		Model:    "command-a",
		Messages: []core.Message{{Role: "user", Content: "hello"}},
	})
	require.NoError(t, err)
	defer stream.Close()

	body, err := io.ReadAll(stream)
	require.NoError(t, err)

	output := string(body)
	assert.Contains(t, output, `"message":"Cohere stream ended before message-end"`)
	assert.NotContains(t, output, `"finish_reason":"stop"`, "stream fabricated a successful stop finish")
	assert.Contains(t, output, "data: [DONE]")
}

func TestEmbeddingsTranslatesOpenAIShape(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, `{
		"id":"embed-id",
		"response_type":"embeddings_by_type",
		"embeddings":{"float":[[0.1,0.2],[0.3,0.4]]},
		"meta":{"billed_units":{"input_tokens":7}}
	}`)

	provider := NewWithHTTPClient("key", server.URL, server.Client(), llmclient.Hooks{})
	dimensions := 2
	resp, err := provider.Embeddings(context.Background(), &core.EmbeddingRequest{
		Model:      "embed-v4.0",
		Input:      []any{"one", "two"},
		Dimensions: &dimensions,
		ExtraFields: core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{
			"truncate": json.RawMessage(`"END"`),
		}),
	})
	require.NoError(t, err)

	sent := capture.Last(t)
	assert.Equal(t, "/v2/embed", sent.Path)
	captured := sent.JSON(t)
	assert.Equal(t, "search_document", captured["input_type"])
	assert.Equal(t, float64(2), captured["output_dimension"])
	assert.Equal(t, []any{"float"}, captured["embedding_types"])
	assert.Equal(t, "END", captured["truncate"])

	assert.Equal(t, "list", resp.Object)
	assert.Equal(t, "embed-v4.0", resp.Model)
	assert.Equal(t, "cohere", resp.Provider)
	require.Len(t, resp.Data, 2)
	assert.Equal(t, `[0.3,0.4]`, string(resp.Data[1].Embedding))
	assert.Equal(t, 7, resp.Usage.PromptTokens)
	assert.Equal(t, 7, resp.Usage.TotalTokens)
}

func TestEmbeddingsSupportsBase64(t *testing.T) {
	resp := fromCohereEmbedResponse(&embedResponse{
		Embeddings: embedVectors{Base64: []string{"AAAA", "BBBB"}},
	}, &core.EmbeddingRequest{Model: "embed-v4.0", EncodingFormat: "base64"})
	assert.Equal(t, `"BBBB"`, string(resp.Data[1].Embedding))
}

func TestListModelsFiltersUnsupportedEndpointsAndRotatesKeys(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, `{"models":[
		{"name":"command-a","endpoints":["chat"],"context_length":128000},
		{"name":"embed-v4.0","endpoints":["embed"]},
		{"name":"cohere-transcribe-03-2026","endpoints":["transcriptions"]},
		{"name":"rerank-v3.5","endpoints":["rerank"]},
		{"name":"legacy-unknown"}
	]}`)

	provider := NewWithHTTPClient("first", server.URL, server.Client(), llmclient.Hooks{})
	provider.keys = providers.NewKeyring("first", "second")
	for range 2 {
		resp, err := provider.ListModels(context.Background())
		require.NoError(t, err)
		require.Len(t, resp.Data, 4)
		require.NotNil(t, resp.Data[0].Metadata)
		require.NotNil(t, resp.Data[0].Metadata.ContextWindow)
		assert.Equal(t, 128000, *resp.Data[0].Metadata.ContextWindow)

		wantModes := map[string][]string{
			"command-a":                 {"chat"},
			"embed-v4.0":                {"embedding"},
			"cohere-transcribe-03-2026": {"audio_transcription"},
			"legacy-unknown":            nil,
		}
		for _, model := range resp.Data {
			want := wantModes[model.ID]
			if len(want) == 0 {
				if model.Metadata != nil {
					assert.Empty(t, model.Metadata.Modes, "%s Modes", model.ID)
				}
				continue
			}
			require.NotNil(t, model.Metadata, "%s Metadata", model.ID)
			assert.Equal(t, want, model.Metadata.Modes, "%s Modes", model.ID)
			assert.Equal(t, core.CategoriesForModes(want), model.Metadata.Categories, "%s Categories", model.ID)
		}
	}

	requests := capture.All()
	require.Len(t, requests, 2)
	for i, sent := range requests {
		assert.Equal(t, "/v1/models", sent.Path, "request %d", i)
		assert.Equal(t, "1000", sent.Query.Get("page_size"), "request %d", i)
	}
	assert.Equal(t, "Bearer first", requests[0].Header.Get("Authorization"))
	assert.Equal(t, "Bearer second", requests[1].Header.Get("Authorization"))
}

func TestInvalidCohereRequestsReturnClientErrors(t *testing.T) {
	_, err := toCohereChatRequest(&core.ChatRequest{
		Model:      "command-a",
		Messages:   []core.Message{{Role: "user", Content: "hello"}},
		ToolChoice: "required",
	}, false)
	assertInvalidRequest(t, err)

	_, err = toCohereChatRequest(&core.ChatRequest{
		Model:    "command-a",
		Messages: []core.Message{{Role: "user", Content: "hello"}},
		ExtraFields: core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{
			"response_format": json.RawMessage(`{"type":"json_schema","json_schema":{"name":"missing-schema"}}`),
		}),
	}, false)
	assertInvalidRequest(t, err)

	_, err = toCohereEmbedRequest(&core.EmbeddingRequest{
		Model: "embed-v4.0",
		Input: []any{"text", []any{1, 2}},
	})
	assertInvalidRequest(t, err)
}

func TestPassthroughForwardsNativeCohereRequest(t *testing.T) {
	const upstreamBody = `{"message":"rate limited"}`
	server, capture := providertest.Server(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Upstream-Request-ID", "cohere-request-1")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, upstreamBody)
	})

	provider := NewWithHTTPClient("test-key", server.URL, server.Client(), llmclient.Hooks{})
	resp, err := provider.Passthrough(context.Background(), &core.PassthroughRequest{
		Method:   http.MethodPost,
		Endpoint: "v2/rerank?priority=high",
		Body:     io.NopCloser(strings.NewReader(`{"model":"rerank-v3.5","query":"gateway"}`)),
		Headers:  http.Header{"X-Client-Trace": {"trace-123"}},
	})
	require.NoError(t, err)
	defer resp.Body.Close()

	sent := capture.Last(t)
	assert.Equal(t, "/v2/rerank", sent.Path)
	assert.Equal(t, "high", sent.Query.Get("priority"))
	assert.Equal(t, "Bearer test-key", sent.Header.Get("Authorization"))
	assert.Equal(t, "trace-123", sent.Header.Get("X-Client-Trace"))
	assert.Contains(t, string(sent.Body), `"rerank-v3.5"`)

	assert.Equal(t, http.StatusTooManyRequests, resp.StatusCode)
	assert.Equal(t, []string{"cohere-request-1"}, resp.Headers["X-Upstream-Request-Id"])

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, upstreamBody, string(body))
}

func assertInvalidRequest(t *testing.T, err error) {
	t.Helper()
	var gatewayErr *core.GatewayError
	require.ErrorAs(t, err, &gatewayErr)
	assert.Equal(t, core.ErrorTypeInvalidRequest, gatewayErr.Type)
}
