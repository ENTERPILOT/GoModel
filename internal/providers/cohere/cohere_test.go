package cohere

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers"
)

func TestChatCompletionTranslatesRequestAndResponse(t *testing.T) {
	var captured map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/chat" {
			t.Errorf("path = %q, want /v2/chat", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("Authorization = %q", got)
		}
		if got := r.Header.Get("X-Client-Name"); got != "GoModel" {
			t.Errorf("X-Client-Name = %q", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
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
				"tokens":{"input_tokens":12,"output_tokens":4},
				"cached_tokens":2
			}
		}`)
	}))
	defer server.Close()

	provider := NewWithHTTPClient("test-key", server.URL, server.Client(), llmclient.Hooks{})
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
	if err != nil {
		t.Fatalf("ChatCompletion() error = %v", err)
	}

	if captured["model"] != req.Model || captured["stream"] != false {
		t.Fatalf("request model/stream = %#v/%#v", captured["model"], captured["stream"])
	}
	messages := captured["messages"].([]any)
	if messages[0].(map[string]any)["role"] != "system" {
		t.Fatalf("developer role = %#v, want system", messages[0])
	}
	if messages[0].(map[string]any)["content"] != "Be brief." {
		t.Fatalf("developer content = %#v, want a Cohere system string", messages[0])
	}
	userContent := messages[1].(map[string]any)["content"].([]any)
	image := userContent[1].(map[string]any)
	if image["type"] != "image_url" {
		t.Fatalf("image content = %#v", image)
	}
	toolResult := messages[3].(map[string]any)["content"].(string)
	if toolResult != `{"result":"sunny"}` {
		t.Fatalf("tool result = %q", toolResult)
	}
	if captured["tool_choice"] != "REQUIRED" {
		t.Fatalf("tool_choice = %#v", captured["tool_choice"])
	}
	if captured["max_tokens"] != float64(250) {
		t.Fatalf("max_tokens = %#v", captured["max_tokens"])
	}
	if got := captured["stop_sequences"].([]any)[0]; got != "END" {
		t.Fatalf("stop_sequences = %#v", captured["stop_sequences"])
	}
	if captured["k"] != float64(20) {
		t.Fatalf("k = %#v", captured["k"])
	}
	responseFormat := captured["response_format"].(map[string]any)
	if responseFormat["type"] != "json_object" {
		t.Fatalf("response_format.type = %#v, want json_object", responseFormat["type"])
	}
	jsonSchema, ok := responseFormat["json_schema"].(map[string]any)
	if !ok || jsonSchema["type"] != "object" {
		t.Fatalf("response_format.json_schema = %#v, want translated schema", responseFormat["json_schema"])
	}
	if _, leaked := responseFormat["name"]; leaked {
		t.Fatalf("response_format leaked OpenAI wrapper fields: %#v", responseFormat)
	}

	if resp.ID != "cohere-id" || resp.Model != req.Model || resp.Provider != "cohere" {
		t.Fatalf("response identity = %#v", resp)
	}
	if resp.Choices[0].FinishReason != "tool_calls" {
		t.Fatalf("finish_reason = %q", resp.Choices[0].FinishReason)
	}
	if resp.Choices[0].Message.Content != "I will check." {
		t.Fatalf("content = %#v", resp.Choices[0].Message.Content)
	}
	if got := rawString(resp.Choices[0].Message.ExtraFields.Lookup("reasoning_content")); got != "check the weather" {
		t.Fatalf("reasoning_content = %q", got)
	}
	if len(resp.Choices[0].Message.ToolCalls) != 1 ||
		resp.Choices[0].Message.ToolCalls[0].Function.Arguments != `{"city":"Warsaw"}` {
		t.Fatalf("tool calls = %#v", resp.Choices[0].Message.ToolCalls)
	}
	if resp.Usage.PromptTokens != 12 || resp.Usage.CompletionTokens != 4 || resp.Usage.TotalTokens != 16 {
		t.Fatalf("usage = %#v", resp.Usage)
	}
	if resp.Usage.PromptTokensDetails == nil || resp.Usage.PromptTokensDetails.CachedTokens != 2 {
		t.Fatalf("prompt token details = %#v", resp.Usage.PromptTokensDetails)
	}
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
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, `{
					"id":"failed-generation",
					"finish_reason":"`+tt.finishReason+`",
					"message":{"role":"assistant","content":[]},
					"usage":{}
				}`)
			}))
			defer server.Close()

			provider := NewWithHTTPClient("key", server.URL, server.Client(), llmclient.Hooks{})
			resp, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
				Model:    "command-a",
				Messages: []core.Message{{Role: "user", Content: "hello"}},
			})
			if resp != nil {
				t.Fatalf("ChatCompletion() response = %#v, want nil", resp)
			}
			gatewayErr, ok := err.(*core.GatewayError)
			if !ok {
				t.Fatalf("ChatCompletion() error = %#v, want GatewayError", err)
			}
			if gatewayErr.Type != core.ErrorTypeProvider || gatewayErr.StatusCode != tt.wantStatus {
				t.Fatalf("ChatCompletion() error = %#v, want provider status %d", gatewayErr, tt.wantStatus)
			}
			if !strings.Contains(strings.ToLower(gatewayErr.Message), tt.wantMessage) {
				t.Fatalf("ChatCompletion() message = %q, want %q", gatewayErr.Message, tt.wantMessage)
			}
		})
	}
}

func TestStreamChatCompletionConvertsCohereEvents(t *testing.T) {
	var streamValue any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		streamValue = body["stream"]
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, strings.Join([]string{
			`event: message-start`,
			`data: {"type":"message-start","id":"stream-id","delta":{"message":{"role":"assistant"}}}`,
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
		}, "\n"))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("key", server.URL, server.Client(), llmclient.Hooks{})
	stream, err := provider.StreamChatCompletion(context.Background(), &core.ChatRequest{
		Model:         "command-a",
		Messages:      []core.Message{{Role: "user", Content: "hello"}},
		StreamOptions: &core.StreamOptions{IncludeUsage: true},
	})
	if err != nil {
		t.Fatalf("StreamChatCompletion() error = %v", err)
	}
	defer stream.Close()
	body, err := io.ReadAll(stream)
	if err != nil {
		t.Fatalf("read stream: %v", err)
	}
	output := string(body)

	if streamValue != true {
		t.Fatalf("upstream stream = %#v", streamValue)
	}
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
		if !strings.Contains(output, want) {
			t.Errorf("stream missing %q:\n%s", want, output)
		}
	}
}

func TestStreamChatCompletionReturnsGenerationFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, strings.Join([]string{
			`data: {"type":"message-start","id":"stream-id","delta":{"message":{"role":"assistant"}}}`,
			``,
			`data: {"type":"message-end","delta":{"finish_reason":"TIMEOUT"}}`,
			``,
		}, "\n"))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("key", server.URL, server.Client(), llmclient.Hooks{})
	stream, err := provider.StreamChatCompletion(context.Background(), &core.ChatRequest{
		Model:    "command-a",
		Messages: []core.Message{{Role: "user", Content: "hello"}},
	})
	if err != nil {
		t.Fatalf("StreamChatCompletion() error = %v", err)
	}
	defer stream.Close()

	body, err := io.ReadAll(stream)
	if err != nil {
		t.Fatalf("read stream: %v", err)
	}
	output := string(body)
	if !strings.Contains(output, `"type":"provider_error"`) ||
		!strings.Contains(output, `"message":"Cohere generation timed out"`) {
		t.Fatalf("stream = %s, want provider timeout error", output)
	}
	if strings.Contains(output, `"finish_reason":"stop"`) {
		t.Fatalf("stream fabricated a successful stop finish:\n%s", output)
	}
}

func TestStreamResponsesPropagatesGenerationFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, strings.Join([]string{
			`data: {"type":"message-start","id":"stream-id","delta":{"message":{"role":"assistant"}}}`,
			``,
			`data: {"type":"message-end","delta":{"finish_reason":"ERROR","error":"capacity exhausted"}}`,
			``,
		}, "\n"))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("key", server.URL, server.Client(), llmclient.Hooks{})
	stream, err := provider.StreamResponses(context.Background(), &core.ResponsesRequest{
		Model: "command-a",
		Input: "hello",
	})
	if err != nil {
		t.Fatalf("StreamResponses() error = %v", err)
	}
	defer stream.Close()

	body, err := io.ReadAll(stream)
	if err != nil {
		t.Fatalf("read stream: %v", err)
	}
	output := string(body)
	if !strings.Contains(output, "event: response.failed") ||
		!strings.Contains(output, `"status":"failed"`) ||
		!strings.Contains(output, `"message":"capacity exhausted"`) {
		t.Fatalf("Responses stream = %s, want response.failed", output)
	}
	if strings.Contains(output, "event: response.completed") {
		t.Fatalf("Responses stream fabricated response.completed:\n%s", output)
	}
}

func TestStreamChatCompletionDoesNotTurnIncompleteStreamIntoSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"content-delta\",\"delta\":{\"message\":{\"content\":{\"text\":\"partial\"}}}}\n\n")
	}))
	defer server.Close()

	provider := NewWithHTTPClient("key", server.URL, server.Client(), llmclient.Hooks{})
	stream, err := provider.StreamChatCompletion(context.Background(), &core.ChatRequest{
		Model:    "command-a",
		Messages: []core.Message{{Role: "user", Content: "hello"}},
	})
	if err != nil {
		t.Fatalf("StreamChatCompletion() error = %v", err)
	}
	defer stream.Close()
	body, err := io.ReadAll(stream)
	if err != nil {
		t.Fatalf("read stream: %v", err)
	}
	output := string(body)
	if !strings.Contains(output, `"message":"Cohere stream ended before message-end"`) {
		t.Fatalf("stream = %s, want incomplete-stream error", output)
	}
	if strings.Contains(output, `"finish_reason":"stop"`) {
		t.Fatalf("stream fabricated a successful stop finish:\n%s", output)
	}
	if !strings.Contains(output, "data: [DONE]") {
		t.Fatalf("stream missing DONE marker:\n%s", output)
	}
}

func TestEmbeddingsTranslatesOpenAIShape(t *testing.T) {
	var captured map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/embed" {
			t.Errorf("path = %q, want /v2/embed", r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&captured)
		_, _ = io.WriteString(w, `{
			"id":"embed-id",
			"response_type":"embeddings_by_type",
			"embeddings":{"float":[[0.1,0.2],[0.3,0.4]]},
			"meta":{"billed_units":{"input_tokens":7}}
		}`)
	}))
	defer server.Close()

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
	if err != nil {
		t.Fatalf("Embeddings() error = %v", err)
	}

	if captured["input_type"] != "search_document" {
		t.Fatalf("input_type = %#v", captured["input_type"])
	}
	if captured["output_dimension"] != float64(2) {
		t.Fatalf("output_dimension = %#v", captured["output_dimension"])
	}
	if captured["embedding_types"].([]any)[0] != "float" {
		t.Fatalf("embedding_types = %#v", captured["embedding_types"])
	}
	if captured["truncate"] != "END" {
		t.Fatalf("truncate = %#v", captured["truncate"])
	}
	if resp.Object != "list" || resp.Model != "embed-v4.0" || resp.Provider != "cohere" {
		t.Fatalf("response = %#v", resp)
	}
	if len(resp.Data) != 2 || string(resp.Data[1].Embedding) != `[0.3,0.4]` {
		t.Fatalf("embedding data = %#v", resp.Data)
	}
	if resp.Usage.PromptTokens != 7 || resp.Usage.TotalTokens != 7 {
		t.Fatalf("usage = %#v", resp.Usage)
	}
}

func TestEmbeddingsSupportsBase64(t *testing.T) {
	resp := fromCohereEmbedResponse(&embedResponse{
		Embeddings: embedVectors{Base64: []string{"AAAA", "BBBB"}},
	}, &core.EmbeddingRequest{Model: "embed-v4.0", EncodingFormat: "base64"})
	if got := string(resp.Data[1].Embedding); got != `"BBBB"` {
		t.Fatalf("base64 embedding = %s", got)
	}
}

func TestListModelsFiltersUnsupportedEndpointsAndRotatesKeys(t *testing.T) {
	var (
		mu      sync.Mutex
		headers []string
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" || r.URL.Query().Get("page_size") != "1000" {
			t.Errorf("models URL = %s", r.URL.String())
		}
		mu.Lock()
		headers = append(headers, r.Header.Get("Authorization"))
		mu.Unlock()
		_, _ = io.WriteString(w, `{"models":[
			{"name":"command-a","endpoints":["chat"],"context_length":128000},
			{"name":"embed-v4.0","endpoints":["embed"]},
			{"name":"rerank-v3.5","endpoints":["rerank"]},
			{"name":"legacy-unknown"}
		]}`)
	}))
	defer server.Close()

	provider := NewWithHTTPClient("first", server.URL, server.Client(), llmclient.Hooks{})
	provider.keys = providers.NewKeyring("first", "second")
	for range 2 {
		resp, err := provider.ListModels(context.Background())
		if err != nil {
			t.Fatalf("ListModels() error = %v", err)
		}
		if len(resp.Data) != 3 {
			t.Fatalf("models = %#v", resp.Data)
		}
		if resp.Data[0].Metadata == nil || resp.Data[0].Metadata.ContextWindow == nil ||
			*resp.Data[0].Metadata.ContextWindow != 128000 {
			t.Fatalf("model metadata = %#v", resp.Data[0].Metadata)
		}
	}
	if len(headers) != 2 || headers[0] != "Bearer first" || headers[1] != "Bearer second" {
		t.Fatalf("Authorization headers = %#v", headers)
	}
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

func TestProviderImplementsNativePassthrough(t *testing.T) {
	provider := NewWithHTTPClient("key", "https://example.com", nil, llmclient.Hooks{})
	var _ core.PassthroughProvider = provider
}

func assertInvalidRequest(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("error = nil, want invalid request")
	}
	gatewayErr, ok := err.(*core.GatewayError)
	if !ok || gatewayErr.Type != core.ErrorTypeInvalidRequest {
		t.Fatalf("error = %#v, want invalid request", err)
	}
}
