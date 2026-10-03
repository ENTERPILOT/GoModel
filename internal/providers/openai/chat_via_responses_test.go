package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
	"github.com/enterpilot/gomodel/internal/streaming"
)

const responsesJSON = `{"id":"resp_1","object":"response","created_at":1700000000,"model":"gpt-6-sol","status":"completed",
	"output":[{"type":"reasoning","id":"rs_1","summary":[]},
		{"type":"message","id":"msg_1","role":"assistant","content":[{"type":"output_text","text":"Checking.","annotations":[]}]},
		{"type":"function_call","id":"fc_1","call_id":"call_1","name":"get_weather","arguments":"{\"city\":\"Paris\"}"}],
	"usage":{"input_tokens":1000,"output_tokens":50,"total_tokens":1050,
		"input_tokens_details":{"cached_tokens":800,"cache_write_tokens":150},"output_tokens_details":{"reasoning_tokens":20}}}`

var weatherTool = []map[string]any{{"type": "function", "function": map[string]any{
	"name": "get_weather", "parameters": map[string]any{"type": "object"},
}}}

func newRoutingProvider(t *testing.T) (*Provider, *providertest.Capture) {
	t.Helper()
	server, capture := providertest.RouteServer(t, map[string]http.HandlerFunc{
		"/chat/completions": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, providertest.ChatCompletionJSON)
		},
		"/responses": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, responsesJSON)
		},
	})
	provider := New(providers.ProviderConfig{APIKey: testAPIKey, BaseURL: server.URL}, providertest.Options(llmclient.Hooks{})).(*Provider)
	return provider, capture
}

func toolMessages(content any) []core.Message {
	return []core.Message{
		{Role: "user", Content: "Take a screenshot"},
		{Role: "assistant", ToolCalls: []core.ToolCall{{ID: "call_1", Type: "function", Function: core.FunctionCall{Name: "screenshot", Arguments: "{}"}}}},
		{Role: "tool", ToolCallID: "call_1", Content: content},
	}
}

// A chat request goes to the Responses API only when Chat Completions would
// reject it or hide part of it from the model, and only when it translates
// exactly.
func TestChatCompletion_RoutesToResponsesOnlyWhenChatCannotServe(t *testing.T) {
	image := []core.ContentPart{
		{Type: "text", Text: "done"},
		{Type: "image_url", ImageURL: &core.ImageURLContent{URL: "data:image/png;base64,AAAA"}},
	}
	tests := []struct {
		name     string
		req      core.ChatRequest
		wantPath string
	}{
		{name: "tools without effort stay on chat", wantPath: "/chat/completions",
			req: core.ChatRequest{Model: "gpt-6-luna", Tools: weatherTool}},
		{name: "tools with effort on gpt-6", wantPath: "/responses",
			req: core.ChatRequest{Model: "gpt-6-luna", Tools: weatherTool, Reasoning: &core.Reasoning{Effort: "low"}}},
		{name: "tools with flat effort on gpt-5.6", wantPath: "/responses",
			req: core.ChatRequest{Model: "gpt-5.6-terra", Tools: weatherTool,
				ExtraFields: core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{"reasoning_effort": json.RawMessage(`"high"`)})}},
		{name: "tools with effort none stay on chat", wantPath: "/chat/completions",
			req: core.ChatRequest{Model: "gpt-6-luna", Tools: weatherTool, Reasoning: &core.Reasoning{Effort: "none"}}},
		{name: "tools on astra", wantPath: "/responses",
			req: core.ChatRequest{Model: "gpt-6-astra", Tools: weatherTool}},
		{name: "tools on gpt-6.1", wantPath: "/responses",
			req: core.ChatRequest{Model: "gpt-6.1-sol", Tools: weatherTool}},
		{name: "gpt-5.5 keeps tools with effort on chat", wantPath: "/chat/completions",
			req: core.ChatRequest{Model: "gpt-5.5", Tools: weatherTool, Reasoning: &core.Reasoning{Effort: "high"}}},
		{name: "effort without tools stays on chat", wantPath: "/chat/completions",
			req: core.ChatRequest{Model: "gpt-6-luna", Reasoning: &core.Reasoning{Effort: "high"}}},
		{name: "tool result with an image", wantPath: "/responses",
			req: core.ChatRequest{Model: "gpt-5.6-terra", Messages: toolMessages(image)}},
		{name: "text tool result stays on chat", wantPath: "/chat/completions",
			req: core.ChatRequest{Model: "gpt-5.6-terra", Messages: toolMessages("done")}},
		{name: "file by URL", wantPath: "/responses",
			req: core.ChatRequest{Model: "gpt-6-luna", Messages: []core.Message{{Role: "user", Content: []core.ContentPart{
				{Type: "file", File: &core.FileContent{FileURL: "https://example.com/a.pdf"}},
			}}}}},
		{name: "gpt-4o tool result with an image", wantPath: "/responses",
			req: core.ChatRequest{Model: "gpt-4o", Messages: toolMessages(image)}},
		{name: "custom model name stays on chat", wantPath: "/chat/completions",
			req: core.ChatRequest{Model: "llama-3.3-70b", Messages: toolMessages(image)}},
		{name: "untranslatable field stays on chat", wantPath: "/chat/completions",
			req: core.ChatRequest{Model: "gpt-6-astra", Tools: weatherTool,
				ExtraFields: core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{"n": json.RawMessage(`2`)})}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider, capture := newRoutingProvider(t)
			req := tt.req
			if req.Messages == nil {
				req.Messages = []core.Message{{Role: "user", Content: "Weather in Paris?"}}
			}
			_, err := provider.ChatCompletion(context.Background(), &req)
			require.NoError(t, err)
			assert.Equal(t, "/"+strings.TrimPrefix(tt.wantPath, "/"), capture.Last(t).Path)
		})
	}
}

// The translated body carries every chat member in its Responses form: roles
// and content parts, assistant tool calls and tool results (attachments kept),
// non-strict function tools, the named tool choice, token limit, effort,
// structured output and pass-through fields. Temperature is dropped as on the
// reasoning chat path, and nothing is stored on OpenAI's side.
func TestChatToResponsesRequest_TranslatesEveryMember(t *testing.T) {
	maxTokens := 500
	temperature := 0.3
	req := &core.ChatRequest{
		Model:       "gpt-6-astra",
		MaxTokens:   &maxTokens,
		Temperature: &temperature,
		User:        "user-1",
		Reasoning:   &core.Reasoning{Effort: "low"},
		Tools:       weatherTool,
		ToolChoice:  map[string]any{"type": "function", "function": map[string]any{"name": "get_weather"}},
		Messages: []core.Message{
			{Role: "system", Content: "Be brief."},
			{Role: "user", Content: []core.ContentPart{
				{Type: "text", Text: "Look"},
				{Type: "image_url", ImageURL: &core.ImageURLContent{URL: "https://example.com/a.png", Detail: "original"}},
				{Type: "file", File: &core.FileContent{FileData: "data:application/pdf;base64,AAAA", Filename: "a.pdf"}},
			}},
			{Role: "assistant", Content: "Checking.", ToolCalls: []core.ToolCall{
				{ID: "call_1", Type: "function", Function: core.FunctionCall{Name: "get_weather", Arguments: `{"city":"Paris"}`}},
			}},
			{Role: "tool", ToolCallID: "call_1", Content: "Sunny"},
			{Role: "tool", ToolCallID: "call_2", Content: []core.ContentPart{
				{Type: "image_url", ImageURL: &core.ImageURLContent{URL: "data:image/png;base64,BBBB"}},
			}},
		},
		ExtraFields: core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{
			"prompt_cache_key": json.RawMessage(`"k1"`),
			"verbosity":        json.RawMessage(`"low"`),
			"response_format":  json.RawMessage(`{"type":"json_schema","json_schema":{"name":"p","strict":true,"schema":{"type":"object"}}}`),
		}),
	}

	got, err := chatToResponsesRequest(req)
	require.NoError(t, err)
	body, err := json.Marshal(got)
	require.NoError(t, err)
	assert.JSONEq(t, `{
		"model": "gpt-6-astra",
		"input": [
			{"role": "system", "content": [{"type": "input_text", "text": "Be brief."}]},
			{"role": "user", "content": [
				{"type": "input_text", "text": "Look"},
				{"type": "input_image", "image_url": "https://example.com/a.png", "detail": "original"},
				{"type": "input_file", "file_data": "data:application/pdf;base64,AAAA", "filename": "a.pdf"}]},
			{"role": "assistant", "content": [{"type": "output_text", "text": "Checking."}]},
			{"type": "function_call", "call_id": "call_1", "name": "get_weather", "arguments": "{\"city\":\"Paris\"}"},
			{"type": "function_call_output", "call_id": "call_1", "output": "Sunny"},
			{"type": "function_call_output", "call_id": "call_2", "output": [
				{"type": "input_image", "image_url": "data:image/png;base64,BBBB", "detail": "auto"}]}
		],
		"tools": [{"type": "function", "name": "get_weather", "parameters": {"type": "object"}, "strict": false}],
		"tool_choice": {"type": "function", "name": "get_weather"},
		"max_output_tokens": 500,
		"reasoning": {"effort": "low"},
		"text": {"verbosity": "low", "format": {"type": "json_schema", "name": "p", "strict": true, "schema": {"type": "object"}}},
		"store": false,
		"user": "user-1",
		"prompt_cache_key": "k1"
	}`, string(body))
}

// OpenAI rejects inline file data without a filename, so a file that arrives
// without one is named for its media type.
func TestChatToResponsesRequest_NamesUnnamedInlineFiles(t *testing.T) {
	got, err := chatToResponsesRequest(&core.ChatRequest{Model: "gpt-6-astra", Messages: toolMessages([]core.ContentPart{
		{Type: "file", File: &core.FileContent{FileData: "data:application/pdf;base64,AAAA"}},
	})})
	require.NoError(t, err)
	items := got.Input.([]any)
	output := items[len(items)-1].(map[string]any)["output"].([]any)
	assert.Equal(t, map[string]any{"type": "input_file", "file_data": "data:application/pdf;base64,AAAA", "filename": "document.pdf"}, output[0])
}

func TestChatToResponsesRequest_RejectsWhatItCannotCarry(t *testing.T) {
	tests := []struct {
		name string
		req  core.ChatRequest
	}{
		{name: "unknown member", req: core.ChatRequest{Model: "gpt-6-astra",
			ExtraFields: core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{"logprobs": json.RawMessage(`true`)})}},
		{name: "audio input", req: core.ChatRequest{Model: "gpt-6-astra", Messages: []core.Message{{Role: "user", Content: []core.ContentPart{
			{Type: "input_audio", InputAudio: &core.InputAudioContent{Data: "AAAA", Format: "wav"}},
		}}}}},
		{name: "custom tool", req: core.ChatRequest{Model: "gpt-6-astra",
			Tools: []map[string]any{{"type": "custom", "custom": map[string]any{"name": "grep"}}}}},
		{name: "unknown role", req: core.ChatRequest{Model: "gpt-6-astra", Messages: []core.Message{{Role: "function", Content: "x"}}}},
		{name: "speaker name", req: core.ChatRequest{Model: "gpt-6-astra", Messages: []core.Message{{Role: "user", Content: "x",
			ExtraFields: core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{"name": json.RawMessage(`"ada"`)})}}}},
		{name: "part member", req: core.ChatRequest{Model: "gpt-6-astra", Messages: []core.Message{{Role: "user", Content: []core.ContentPart{
			{Type: "text", Text: "x", ExtraFields: core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{"x_note": json.RawMessage(`1`)})},
		}}}}},
		{name: "assistant image without text", req: core.ChatRequest{Model: "gpt-6-astra", Messages: []core.Message{{Role: "assistant", Content: []core.ContentPart{
			{Type: "image_url", ImageURL: &core.ImageURLContent{URL: "https://example.com/a.png"}},
		}}}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := chatToResponsesRequest(&tt.req)
			require.ErrorIs(t, err, errNotTranslatable)
		})
	}
}

func TestChatCompletion_ViaResponsesRendersChatCompletion(t *testing.T) {
	provider, capture := newRoutingProvider(t)
	resp, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
		Model: "gpt-6-sol", Tools: weatherTool, Reasoning: &core.Reasoning{Effort: "low"},
		Messages: []core.Message{{Role: "user", Content: "Weather in Paris?"}},
	})
	require.NoError(t, err)
	assert.Equal(t, "/responses", capture.Last(t).Path)

	require.Len(t, resp.Choices, 1)
	choice := resp.Choices[0]
	assert.Equal(t, "tool_calls", choice.FinishReason)
	assert.Equal(t, "Checking.", choice.Message.Content)
	require.Len(t, choice.Message.ToolCalls, 1)
	assert.Equal(t, core.ToolCall{ID: "call_1", Type: "function",
		Function: core.FunctionCall{Name: "get_weather", Arguments: `{"city":"Paris"}`}}, choice.Message.ToolCalls[0])
	assert.Equal(t, "chat.completion", resp.Object)
	assert.Equal(t, int64(1700000000), resp.Created)
	assert.Equal(t, 1000, resp.Usage.PromptTokens)
	assert.Equal(t, 50, resp.Usage.CompletionTokens)
	require.NotNil(t, resp.Usage.PromptTokensDetails)
	assert.Equal(t, 800, resp.Usage.PromptTokensDetails.CachedTokens)
	assert.Equal(t, 150, resp.Usage.PromptTokensDetails.CacheWriteTokens)
	require.NotNil(t, resp.Usage.CompletionTokensDetails)
	assert.Equal(t, 20, resp.Usage.CompletionTokensDetails.ReasoningTokens)
}

func TestChatResponseFromResponses_StatusMapping(t *testing.T) {
	t.Run("max output tokens is length", func(t *testing.T) {
		resp, err := chatResponseFromResponses(&core.ResponsesResponse{Status: "incomplete",
			IncompleteDetails: &core.ResponsesIncompleteDetails{Reason: "max_output_tokens"}}, "openai")
		require.NoError(t, err)
		assert.Equal(t, "length", resp.Choices[0].FinishReason)
		assert.Empty(t, resp.Choices[0].Message.Content)
	})
	t.Run("refusal is the refusal member", func(t *testing.T) {
		var resp core.ResponsesResponse
		require.NoError(t, json.Unmarshal([]byte(`{"id":"resp_1","status":"completed","output":[{"type":"message","id":"m1","role":"assistant",
			"content":[{"type":"refusal","refusal":"I can't help with that."}]}]}`), &resp))
		chat, err := chatResponseFromResponses(&resp, "openai")
		require.NoError(t, err)
		body, err := json.Marshal(chat.Choices[0].Message)
		require.NoError(t, err)
		assert.JSONEq(t, `{"role":"assistant","content":"","refusal":"I can't help with that."}`, string(body))
		assert.Equal(t, "stop", chat.Choices[0].FinishReason)
	})
	t.Run("interrupted turn is an error", func(t *testing.T) {
		_, err := chatResponseFromResponses(&core.ResponsesResponse{Status: "incomplete",
			IncompleteDetails: &core.ResponsesIncompleteDetails{Reason: "interrupted"},
			Output:            []core.ResponsesOutputItem{{Type: "function_call", CallID: "call_1", Name: "f", Arguments: "{"}}}, "openai")
		var gwErr *core.GatewayError
		require.ErrorAs(t, err, &gwErr)
		assert.Contains(t, gwErr.Message, "interrupted")
	})
	t.Run("failed is an error with the upstream message", func(t *testing.T) {
		_, err := chatResponseFromResponses(&core.ResponsesResponse{Status: "failed",
			Error: &core.ResponsesError{Code: "server_error", Message: "boom"}}, "openai")
		var gwErr *core.GatewayError
		require.ErrorAs(t, err, &gwErr)
		assert.Contains(t, gwErr.Message, "boom")
	})
}

// sse renders events as Responses SSE data lines, compacting each so a
// fixture written across lines stays one line, as on the wire.
func sse(events ...string) string {
	var b strings.Builder
	for _, event := range events {
		var compact bytes.Buffer
		if err := json.Compact(&compact, []byte(event)); err != nil {
			panic(err)
		}
		b.WriteString("event: x\ndata: " + compact.String() + "\n\n")
	}
	return b.String()
}

func readChatChunks(t *testing.T, stream io.ReadCloser) ([]map[string]any, string, error) {
	t.Helper()
	defer func() { _ = stream.Close() }()
	body, err := io.ReadAll(stream)
	var chunks []map[string]any
	for line := range strings.SplitSeq(string(body), "\n") {
		data, ok := strings.CutPrefix(line, "data: ")
		if !ok || data == "[DONE]" {
			continue
		}
		var chunk map[string]any
		require.NoError(t, json.Unmarshal([]byte(data), &chunk))
		chunks = append(chunks, chunk)
	}
	return chunks, string(body), err
}

func TestStreamChatCompletion_ViaResponsesStreamsChatChunks(t *testing.T) {
	upstream := sse(
		`{"type":"response.created","response":{"id":"resp_1","created_at":1700000000,"model":"gpt-6-astra","status":"in_progress","output":[]}}`,
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"reasoning","id":"rs_1","summary":[]}}`,
		`{"type":"response.output_text.delta","item_id":"msg_1","delta":"Check"}`,
		`{"type":"response.output_text.delta","item_id":"msg_1","delta":"ing."}`,
		`{"type":"response.output_item.added","output_index":2,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"get_weather","arguments":""}}`,
		`{"type":"response.function_call_arguments.delta","item_id":"fc_1","delta":"{\"city\":"}`,
		`{"type":"response.output_item.added","output_index":3,"item":{"type":"function_call","id":"fc_2","call_id":"call_2","name":"get_weather","arguments":""}}`,
		`{"type":"response.function_call_arguments.delta","item_id":"fc_2","delta":"{\"city\":\"Oslo\"}"}`,
		`{"type":"response.function_call_arguments.delta","item_id":"fc_1","delta":"\"Paris\"}"}`,
		`{"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[],
			"usage":{"input_tokens":1000,"output_tokens":50,"total_tokens":1050,"input_tokens_details":{"cached_tokens":800,"cache_write_tokens":150}}}}`,
	)
	server, capture := providertest.SSEServer(t, upstream)
	provider := New(providers.ProviderConfig{APIKey: testAPIKey, BaseURL: server.URL}, providertest.Options(llmclient.Hooks{})).(*Provider)

	stream, err := provider.StreamChatCompletion(context.Background(), &core.ChatRequest{
		Model: "gpt-6-astra", Tools: weatherTool, StreamOptions: &core.StreamOptions{IncludeUsage: true},
		Messages: []core.Message{{Role: "user", Content: "Weather in Paris and Oslo?"}},
	})
	require.NoError(t, err)
	chunks, body, err := readChatChunks(t, stream)
	require.NoError(t, err)
	assert.Equal(t, "/responses", capture.Last(t).Path)
	assert.Equal(t, true, capture.Last(t).JSON(t)["stream"])
	assert.True(t, strings.HasSuffix(body, "data: [DONE]\n\n"), "stream must end with [DONE]")

	var text string
	args := map[float64]string{}
	ids := map[float64]string{}
	var finish any
	var usage map[string]any
	for _, chunk := range chunks {
		assert.Equal(t, "resp_1", chunk["id"])
		assert.Equal(t, "gpt-6-astra", chunk["model"])
		choice := chunk["choices"].([]any)[0].(map[string]any)
		delta := choice["delta"].(map[string]any)
		if content, ok := delta["content"].(string); ok {
			text += content
		}
		for _, call := range asSlice(delta["tool_calls"]) {
			call := call.(map[string]any)
			index := call["index"].(float64)
			if id, ok := call["id"].(string); ok {
				ids[index] = id
			}
			args[index] += call["function"].(map[string]any)["arguments"].(string)
		}
		if choice["finish_reason"] != nil {
			finish = choice["finish_reason"]
		}
		if u, ok := chunk["usage"].(map[string]any); ok {
			usage = u
		}
	}
	assert.Equal(t, "Checking.", text)
	assert.Equal(t, map[float64]string{0: "call_1", 1: "call_2"}, ids)
	assert.Equal(t, map[float64]string{0: `{"city":"Paris"}`, 1: `{"city":"Oslo"}`}, args)
	assert.Equal(t, "tool_calls", finish)
	require.NotNil(t, usage)
	assert.Equal(t, float64(1000), usage["prompt_tokens"])
	assert.Equal(t, map[string]any{"cached_tokens": float64(800), "cache_write_tokens": float64(150),
		"audio_tokens": float64(0), "text_tokens": float64(0), "image_tokens": float64(0)}, usage["prompt_tokens_details"])
}

func asSlice(value any) []any {
	items, _ := value.([]any)
	return items
}

func TestResponsesChatStream_EndsWithUpstreamErrorOrTruncation(t *testing.T) {
	created := `{"type":"response.created","response":{"id":"resp_1","model":"gpt-6-astra","status":"in_progress","output":[]}}`
	tests := []struct {
		name      string
		upstream  string
		wantError string // message of the final error chunk
		wantTrunc bool
	}{
		{name: "error event", upstream: sse(created, `{"type":"error","code":"server_error","message":"The server had an error"}`),
			wantError: "The server had an error"},
		{name: "failed response", upstream: sse(created, `{"type":"response.failed","response":{"id":"resp_1","status":"failed","output":[],"error":{"code":"server_error","message":"boom"}}}`),
			wantError: "boom"},
		{name: "interrupted response", upstream: sse(created, `{"type":"response.incomplete","response":{"id":"resp_1","status":"incomplete","output":[],"incomplete_details":{"reason":"interrupted"}}}`),
			wantError: "the response is incomplete: interrupted"},
		{name: "truncated", upstream: sse(created, `{"type":"response.output_text.delta","delta":"Hel"}`), wantTrunc: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stream := newResponsesChatStream(io.NopCloser(strings.NewReader(tt.upstream)), "openai", "gpt-6-astra", true)
			chunks, body, err := readChatChunks(t, stream)
			assert.NotContains(t, body, "[DONE]")
			if tt.wantTrunc {
				require.ErrorIs(t, err, streaming.ErrStreamIncomplete)
				return
			}
			require.NoError(t, err)
			last := chunks[len(chunks)-1]
			assert.Equal(t, tt.wantError, last["error"].(map[string]any)["message"])
		})
	}
}

// OpenAI rejects inline file data without a filename on Chat Completions too,
// so the chat path names such files without changing the caller's request.
func TestChatCompletion_NamesUnnamedInlineFiles(t *testing.T) {
	provider, capture := newRoutingProvider(t)
	file := &core.FileContent{FileData: "data:application/pdf;base64,AAAA"}
	req := &core.ChatRequest{Model: "gpt-5.5", Messages: []core.Message{{Role: "user", Content: []core.ContentPart{
		{Type: "file", File: file}, {Type: "text", Text: "Summarize"},
	}}}}

	_, err := provider.ChatCompletion(context.Background(), req)
	require.NoError(t, err)

	sent := capture.Last(t)
	assert.Equal(t, "/chat/completions", sent.Path)
	parts := sent.JSON(t)["messages"].([]any)[0].(map[string]any)["content"].([]any)
	assert.Equal(t, "document.pdf", parts[0].(map[string]any)["file"].(map[string]any)["filename"])
	assert.Empty(t, file.Filename, "the caller's request must not change")
}
