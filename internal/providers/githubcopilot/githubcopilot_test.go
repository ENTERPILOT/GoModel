package githubcopilot

import (
	"context"
	"io"
	"net/http"
	"testing"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
)

// A model the contract fixtures do not list is served on /chat/completions,
// so the shared contract covers the chat-routed surface. Embeddings are
// covered below: Copilot needs list input, which the contract does not send.
func TestChatCompatibleContract(t *testing.T) {
	providertest.AssertChatCompatible(t, providertest.ChatCompatible{
		Registration:   Registration,
		Type:           "github-copilot",
		DefaultBaseURL: "https://api.githubcopilot.com",
		New: func(apiKey, baseURL string, client *http.Client, hooks llmclient.Hooks) core.Provider {
			return newTestProvider(apiKey, baseURL, client, hooks)
		},
		SkipEmbeddings: true,
	})
	providertest.AssertNoNativeSurfaces(t, New(providers.ProviderConfig{APIKey: "gho_test"}, providers.ProviderOptions{}))
}

// modelsJSON lists one model per routing case: a Responses-only GPT model, a
// Claude model served on chat completions and Messages, an older GPT model
// without supported_endpoints, and a model the account has not enabled.
const modelsJSON = `{"object":"list","data":[
	{"id":"gpt-5.6-luna","name":"GPT-5.6 Luna","vendor":"OpenAI","supported_endpoints":["/responses","ws:/responses"],"policy":{"state":"enabled"},
	 "capabilities":{"type":"chat","family":"gpt-5.6-luna","limits":{"max_context_window_tokens":328000,"max_output_tokens":128000},
	 "supports":{"tool_calls":true,"vision":true,"streaming":true,"reasoning_effort":["low","medium","high"]}}},
	{"id":"claude-haiku-4.5","name":"Claude Haiku 4.5","vendor":"Anthropic","supported_endpoints":["/chat/completions","/v1/messages"],"policy":{"state":"enabled"},
	 "capabilities":{"type":"chat","family":"claude-haiku-4.5","limits":{"max_context_window_tokens":144000,"max_output_tokens":32000},"supports":{"tool_calls":true}}},
	{"id":"gpt-4.1","name":"GPT-4.1","vendor":"Azure OpenAI","capabilities":{"type":"chat","family":"gpt-4.1","supports":{}}},
	{"id":"text-embedding-3-small","name":"Embedding V3 small","vendor":"Azure OpenAI","capabilities":{"type":"embeddings","family":"text-embedding-3-small","supports":{}}},
	{"id":"claude-opus-5.5","name":"Claude Opus 5.5","vendor":"Anthropic","supported_endpoints":["/v1/messages","/chat/completions"],"policy":{"state":"disabled"},
	 "capabilities":{"type":"chat","family":"claude-opus-5.5","supports":{}}}
]}`

// chatJSON is shaped like a Copilot reply: no object type, and a padding
// member on the message.
const chatJSON = `{"id":"chatcmpl-1","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"hello","padding":"abcdef"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":1,"total_tokens":6}}`

const embeddingsJSON = `{"data":[{"embedding":[0.1,0.2],"index":0,"object":"embedding"}],"usage":{"prompt_tokens":1,"total_tokens":1}}`

const responsesJSON = `{"id":"resp_1","object":"response","model":"gpt-5.6-luna","status":"completed",
	"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hello"}]}],
	"usage":{"input_tokens":5,"output_tokens":1,"total_tokens":6}}`

const responsesSSE = "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"model\":\"gpt-5.6-luna\",\"status\":\"in_progress\"}}\n\n" +
	"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg_1\",\"delta\":\"hello\"}\n\n" +
	"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"model\":\"gpt-5.6-luna\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"hello\"}]}],\"usage\":{\"input_tokens\":5,\"output_tokens\":1,\"total_tokens\":6}}}\n\n"

// copilotServer fakes the Copilot API: /models lists modelsJSON, and the
// inference endpoints answer with fixed replies, as SSE when streaming.
func copilotServer(t *testing.T) (*Provider, *providertest.Capture) {
	t.Helper()
	reply := func(body, stream string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			raw, _ := io.ReadAll(r.Body)
			if stream != "" && containsStreamTrue(raw) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, stream)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, body)
		}
	}
	server, capture := providertest.RouteServer(t, map[string]http.HandlerFunc{
		"/models":           reply(modelsJSON, ""),
		"/chat/completions": reply(chatJSON, providertest.ChatChunkSSE),
		"/responses":        reply(responsesJSON, responsesSSE),
		"/embeddings":       reply(embeddingsJSON, ""),
	})
	return newTestProvider("gho_test", server.URL, server.Client(), llmclient.Hooks{}), capture
}

func containsStreamTrue(body []byte) bool {
	var req struct {
		Stream bool `json:"stream"`
	}
	return json.Unmarshal(body, &req) == nil && req.Stream
}

// inferencePaths returns the paths of the recorded requests other than the
// model listing.
func inferencePaths(capture *providertest.Capture) []string {
	var paths []string
	for _, req := range capture.All() {
		if req.Path != "/models" {
			paths = append(paths, req.Path)
		}
	}
	return paths
}

func chatRequest(model string) *core.ChatRequest {
	return &core.ChatRequest{Model: model, Messages: []core.Message{{Role: "user", Content: "hi"}}}
}

func TestChatCompletion_RoutesByModelEndpoints(t *testing.T) {
	tests := []struct {
		model    string
		wantPath string
	}{
		{model: "gpt-5.6-luna", wantPath: "/responses"},
		{model: "claude-haiku-4.5", wantPath: "/chat/completions"},
		{model: "gpt-4.1", wantPath: "/chat/completions"},
		{model: "not-listed", wantPath: "/chat/completions"},
	}
	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			provider, capture := copilotServer(t)
			resp, err := provider.ChatCompletion(context.Background(), chatRequest(tt.model))
			require.NoError(t, err)
			assert.Equal(t, []string{tt.wantPath}, inferencePaths(capture))
			require.Len(t, resp.Choices, 1)
			assert.Equal(t, "hello", core.ExtractTextContent(resp.Choices[0].Message.Content))
			assert.Equal(t, 6, resp.Usage.TotalTokens)
		})
	}
}

func TestChatCompletion_NormalizesCopilotReply(t *testing.T) {
	provider, _ := copilotServer(t)
	resp, err := provider.ChatCompletion(context.Background(), chatRequest("claude-haiku-4.5"))
	require.NoError(t, err)

	assert.Equal(t, "chat.completion", resp.Object)
	require.Len(t, resp.Choices, 1)
	assert.Empty(t, resp.Choices[0].Message.ExtraFields.Lookup("padding"), "padding must not reach the client")
}

func TestEmbeddings_WrapsStringInputAndNormalizesReply(t *testing.T) {
	tests := []struct {
		name      string
		input     any
		wantInput any
	}{
		{name: "string", input: "hi", wantInput: []any{"hi"}},
		{name: "list", input: []string{"hi", "there"}, wantInput: []any{"hi", "there"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider, capture := copilotServer(t)
			req := &core.EmbeddingRequest{Model: "text-embedding-3-small", Input: tt.input}
			resp, err := provider.Embeddings(context.Background(), req)
			require.NoError(t, err)

			sent := capture.Last(t)
			assert.Equal(t, "/embeddings", sent.Path)
			assert.Equal(t, tt.wantInput, sent.JSON(t)["input"])
			assert.Equal(t, tt.input, req.Input, "the caller's request must not be mutated")
			assert.Equal(t, "list", resp.Object)
			assert.Equal(t, "text-embedding-3-small", resp.Model)
			require.Len(t, resp.Data, 1)
		})
	}
}

func TestStreamChatCompletion_ResponsesOnlyModelStreamsChatChunks(t *testing.T) {
	provider, capture := copilotServer(t)
	stream, err := provider.StreamChatCompletion(context.Background(), chatRequest("gpt-5.6-luna"))
	require.NoError(t, err)
	body, err := io.ReadAll(stream)
	require.NoError(t, err)
	require.NoError(t, stream.Close())

	assert.Equal(t, []string{"/responses"}, inferencePaths(capture))
	assert.Equal(t, true, capture.Last(t).JSON(t)["stream"])
	assert.Contains(t, string(body), `"object":"chat.completion.chunk"`)
	assert.Contains(t, string(body), `"content":"hello"`)
	assert.Contains(t, string(body), "data: [DONE]")
}

func TestChatCompletion_ResponsesOnlyModelRejectsUntranslatableRequest(t *testing.T) {
	provider, capture := copilotServer(t)
	req := chatRequest("gpt-5.6-luna")
	req.ExtraFields = core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{"logit_bias": json.RawMessage(`{"1":1}`)})

	_, err := provider.ChatCompletion(context.Background(), req)

	var gwErr *core.GatewayError
	require.ErrorAs(t, err, &gwErr)
	assert.Equal(t, core.ErrorTypeInvalidRequest, gwErr.Type)
	assert.Empty(t, inferencePaths(capture), "an untranslatable request must not reach the upstream")
}

func TestResponses_RoutesByModelEndpoints(t *testing.T) {
	tests := []struct {
		model    string
		wantPath string
	}{
		{model: "gpt-5.6-luna", wantPath: "/responses"},
		{model: "claude-haiku-4.5", wantPath: "/chat/completions"},
		{model: "gpt-4.1", wantPath: "/chat/completions"},
	}
	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			provider, capture := copilotServer(t)
			resp, err := provider.Responses(context.Background(), &core.ResponsesRequest{Model: tt.model, Input: "hi"})
			require.NoError(t, err)
			assert.Equal(t, []string{tt.wantPath}, inferencePaths(capture))
			assert.Equal(t, "completed", resp.Status)
		})
	}
}

func TestStreamResponses_ChatModelTranslatesToChatCompletions(t *testing.T) {
	provider, capture := copilotServer(t)
	stream, err := provider.StreamResponses(context.Background(), &core.ResponsesRequest{Model: "claude-haiku-4.5", Input: "hi"})
	require.NoError(t, err)
	body, err := io.ReadAll(stream)
	require.NoError(t, err)
	require.NoError(t, stream.Close())

	assert.Equal(t, []string{"/chat/completions"}, inferencePaths(capture))
	assert.Contains(t, string(body), "response.output_text.delta")
}

func TestListModels_SkipsDisabledModelsAndReportsMetadata(t *testing.T) {
	provider, capture := copilotServer(t)
	resp, err := provider.ListModels(context.Background())
	require.NoError(t, err)

	ids := make([]string, 0, len(resp.Data))
	byID := map[string]core.Model{}
	for _, model := range resp.Data {
		ids = append(ids, model.ID)
		byID[model.ID] = model
	}
	assert.Equal(t, []string{"gpt-5.6-luna", "claude-haiku-4.5", "gpt-4.1", "text-embedding-3-small"}, ids)

	luna := byID["gpt-5.6-luna"]
	assert.Equal(t, "OpenAI", luna.OwnedBy)
	require.NotNil(t, luna.Metadata)
	assert.Equal(t, "GPT-5.6 Luna", luna.Metadata.DisplayName)
	require.NotNil(t, luna.Metadata.ContextWindow)
	assert.Equal(t, 328000, *luna.Metadata.ContextWindow)
	require.NotNil(t, luna.Metadata.MaxOutputTokens)
	assert.Equal(t, 128000, *luna.Metadata.MaxOutputTokens)
	assert.Equal(t, map[string]bool{"tools": true, "vision": true, "streaming": true, "reasoning": true}, luna.Metadata.Capabilities)
	assert.Equal(t, []core.ModelCategory{core.CategoryEmbedding}, byID["text-embedding-3-small"].Metadata.Categories)

	// The listing also taught the provider each model's endpoints.
	_, err = provider.ChatCompletion(context.Background(), chatRequest("gpt-5.6-luna"))
	require.NoError(t, err)
	assert.Equal(t, []string{"/responses"}, inferencePaths(capture))
}

func TestRoutes_UnlistedModelGoesToChatAndRefetchesAtMostOncePerInterval(t *testing.T) {
	provider, capture := copilotServer(t)

	for range 3 {
		_, err := provider.ChatCompletion(context.Background(), chatRequest("unlisted"))
		require.NoError(t, err)
	}

	assert.Equal(t, 1, listingCalls(capture))
	assert.Equal(t, []string{"/chat/completions", "/chat/completions", "/chat/completions"}, inferencePaths(capture))
}

func TestRoutes_FailedListingRoutesToChatAndRetriesAtMostOncePerInterval(t *testing.T) {
	server, capture := providertest.RouteServer(t, map[string]http.HandlerFunc{
		"/models": func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		},
		"/chat/completions": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, chatJSON)
		},
	})
	provider := newTestProvider("gho_test", server.URL, server.Client(), llmclient.Hooks{})

	for range 3 {
		_, err := provider.ChatCompletion(context.Background(), chatRequest("claude-haiku-4.5"))
		require.NoError(t, err)
	}

	assert.Equal(t, 1, listingCalls(capture), "a failing listing is retried at most once per interval")
	assert.Equal(t, []string{"/chat/completions", "/chat/completions", "/chat/completions"}, inferencePaths(capture))
}

func TestRoutes_RefetchSurvivesCallerCancellation(t *testing.T) {
	provider, capture := copilotServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := provider.ChatCompletion(ctx, chatRequest("gpt-5.6-luna"))
	require.Error(t, err)

	_, err = provider.ChatCompletion(context.Background(), chatRequest("gpt-5.6-luna"))
	require.NoError(t, err)
	assert.Equal(t, 1, listingCalls(capture))
	assert.Equal(t, []string{"/responses"}, inferencePaths(capture))
}

func listingCalls(capture *providertest.Capture) int {
	calls := 0
	for _, req := range capture.All() {
		if req.Path == "/models" {
			calls++
		}
	}
	return calls
}

func TestHeaders(t *testing.T) {
	image := []core.ContentPart{
		{Type: "text", Text: "what is this?"},
		{Type: "image_url", ImageURL: &core.ImageURLContent{URL: "data:image/png;base64,AAAA"}},
	}
	tests := []struct {
		name          string
		call          func(*Provider) error
		wantPath      string
		wantInitiator string
		wantVision    bool
	}{
		{
			name: "chat user turn",
			call: func(p *Provider) error {
				_, err := p.ChatCompletion(context.Background(), chatRequest("claude-haiku-4.5"))
				return err
			},
			wantPath: "/chat/completions", wantInitiator: "user",
		},
		{
			name: "chat tool result turn",
			call: func(p *Provider) error {
				_, err := p.ChatCompletion(context.Background(), &core.ChatRequest{Model: "claude-haiku-4.5", Messages: []core.Message{
					{Role: "user", Content: "weather?"},
					{Role: "assistant", ToolCalls: []core.ToolCall{{ID: "call_1", Type: "function", Function: core.FunctionCall{Name: "weather", Arguments: "{}"}}}},
					{Role: "tool", ToolCallID: "call_1", Content: "sunny"},
				}})
				return err
			},
			wantPath: "/chat/completions", wantInitiator: "agent",
		},
		{
			name: "chat image",
			call: func(p *Provider) error {
				_, err := p.ChatCompletion(context.Background(), &core.ChatRequest{Model: "claude-haiku-4.5", Messages: []core.Message{{Role: "user", Content: image}}})
				return err
			},
			wantPath: "/chat/completions", wantInitiator: "user", wantVision: true,
		},
		{
			name: "responses user turn",
			call: func(p *Provider) error {
				_, err := p.Responses(context.Background(), &core.ResponsesRequest{Model: "gpt-5.6-luna", Input: "hi"})
				return err
			},
			wantPath: "/responses", wantInitiator: "user",
		},
		{
			name: "responses tool result turn with image",
			call: func(p *Provider) error {
				_, err := p.Responses(context.Background(), &core.ResponsesRequest{Model: "gpt-5.6-luna", Input: []any{
					map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_image", "image_url": "data:image/png;base64,AAAA"}}},
					map[string]any{"type": "function_call", "call_id": "call_1", "name": "weather", "arguments": "{}"},
					map[string]any{"type": "function_call_output", "call_id": "call_1", "output": "sunny"},
				}})
				return err
			},
			wantPath: "/responses", wantInitiator: "agent", wantVision: true,
		},
		{
			name: "responses tool result carrying the only image",
			call: func(p *Provider) error {
				_, err := p.Responses(context.Background(), &core.ResponsesRequest{Model: "gpt-5.6-luna", Input: []any{
					map[string]any{"role": "user", "content": "describe the screenshot"},
					map[string]any{"type": "function_call", "call_id": "call_1", "name": "screenshot", "arguments": "{}"},
					map[string]any{"type": "function_call_output", "call_id": "call_1", "output": []any{
						map[string]any{"type": "input_image", "image_url": "data:image/png;base64,AAAA"},
					}},
				}})
				return err
			},
			wantPath: "/responses", wantInitiator: "agent", wantVision: true,
		},
		{
			name: "chat tool result carrying the only image on a Responses-only model",
			call: func(p *Provider) error {
				_, err := p.ChatCompletion(context.Background(), &core.ChatRequest{Model: "gpt-5.6-luna", Messages: []core.Message{
					{Role: "user", Content: "describe the screenshot"},
					{Role: "assistant", ToolCalls: []core.ToolCall{{ID: "call_1", Type: "function", Function: core.FunctionCall{Name: "screenshot", Arguments: "{}"}}}},
					{Role: "tool", ToolCallID: "call_1", Content: image},
				}})
				return err
			},
			wantPath: "/responses", wantInitiator: "agent", wantVision: true,
		},
		{
			name: "chat image on a Responses-only model",
			call: func(p *Provider) error {
				_, err := p.ChatCompletion(context.Background(), &core.ChatRequest{Model: "gpt-5.6-luna", Messages: []core.Message{{Role: "user", Content: image}}})
				return err
			},
			wantPath: "/responses", wantInitiator: "user", wantVision: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider, capture := copilotServer(t)
			require.NoError(t, tt.call(provider))
			req := capture.Last(t)
			assert.Equal(t, tt.wantPath, req.Path)
			assert.Equal(t, "Bearer gho_test", req.Header.Get("Authorization"))
			assert.Equal(t, "conversation-edits", req.Header.Get("Openai-Intent"))
			assert.Equal(t, tt.wantInitiator, req.Header.Get("X-Initiator"))
			if tt.wantVision {
				assert.Equal(t, "true", req.Header.Get("Copilot-Vision-Request"))
			} else {
				assert.Empty(t, req.Header.Get("Copilot-Vision-Request"))
			}
		})
	}
}
