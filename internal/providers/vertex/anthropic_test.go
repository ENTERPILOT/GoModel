package vertex

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const anthropicBasePath = "/v1/projects/prod-ai/locations/eu/publishers/anthropic"

func anthropicModeProvider(t *testing.T, serverURL string, client *http.Client) *Provider {
	t.Helper()
	cfg := testConfig()
	cfg.APIMode = "anthropic"
	cfg.BaseURL = serverURL + "/v1/projects/prod-ai/locations/eu/endpoints/openapi"
	provider := newProvider(cfg, providers.ProviderOptions{}, authedTestClient(client), true)
	require.NotNil(t, provider.messages)
	return provider
}

func chatRequest(stream bool) *core.ChatRequest {
	return &core.ChatRequest{
		Model:    "claude-sonnet-5",
		Stream:   stream,
		Messages: []core.Message{{Role: "user", Content: "hi"}},
	}
}

// assertRawPredictRequest checks the request Vertex receives: model in the
// path, anthropic_version in the body, the user's message intact, and only
// the Google bearer token.
func assertRawPredictRequest(t *testing.T, req providertest.Recorded, verb string) {
	t.Helper()
	assert.Equal(t, http.MethodPost, req.Method)
	assert.Equal(t, anthropicBasePath+"/models/claude-sonnet-5:"+verb, req.Path)
	assert.Equal(t, "Bearer vertex-token", req.Header.Get("Authorization"))
	assert.Empty(t, req.Header.Get("x-api-key"))
	assert.Empty(t, req.Header.Get("anthropic-version"))
	body := req.JSON(t)
	assert.NotContains(t, body, "model")
	assert.Equal(t, vertexAnthropicVersion, body["anthropic_version"])
	messages, ok := body["messages"].([]any)
	require.True(t, ok, "messages must be an array")
	require.Len(t, messages, 1)
	assert.Equal(t, map[string]any{"role": "user", "content": "hi"}, messages[0])
}

const anthropicMessageJSON = `{
	"id": "msg_1",
	"type": "message",
	"role": "assistant",
	"model": "claude-sonnet-5",
	"content": [{"type": "text", "text": "ok"}],
	"stop_reason": "end_turn",
	"usage": {"input_tokens": 3, "output_tokens": 1}
}`

func TestAnthropicModeChatUsesRawPredict(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, anthropicMessageJSON)
	provider := anthropicModeProvider(t, server.URL, server.Client())

	resp, err := provider.ChatCompletion(context.Background(), chatRequest(false))
	require.NoError(t, err)

	assertRawPredictRequest(t, capture.Last(t), "rawPredict")
	assert.Equal(t, "vertex", resp.Provider)
	require.Len(t, resp.Choices, 1)
	assert.Equal(t, "ok", resp.Choices[0].Message.Content)
	assert.Equal(t, 3, resp.Usage.PromptTokens)
}

func TestAnthropicModeResponsesUsesRawPredict(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, anthropicMessageJSON)
	provider := anthropicModeProvider(t, server.URL, server.Client())

	resp, err := provider.Responses(context.Background(), &core.ResponsesRequest{
		Model: "claude-sonnet-5",
		Input: "hi",
	})
	require.NoError(t, err)

	assertRawPredictRequest(t, capture.Last(t), "rawPredict")
	require.NotEmpty(t, resp.Output)
	require.NotEmpty(t, resp.Output[0].Content)
	assert.Equal(t, "ok", resp.Output[0].Content[0].Text)
}

func TestAnthropicModeStreamUsesStreamRawPredict(t *testing.T) {
	server, capture := providertest.SSEServer(t, `event: message_start
data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-5","content":[],"stop_reason":null,"usage":{"input_tokens":3,"output_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}

event: message_stop
data: {"type":"message_stop"}

`)
	provider := anthropicModeProvider(t, server.URL, server.Client())

	stream, err := provider.StreamChatCompletion(context.Background(), chatRequest(true))
	require.NoError(t, err)
	body, err := io.ReadAll(stream)
	require.NoError(t, err)
	require.NoError(t, stream.Close())

	assertRawPredictRequest(t, capture.Last(t), "streamRawPredict")
	assert.Equal(t, true, capture.Last(t).JSON(t)["stream"])
	assert.Contains(t, string(body), `"content":"ok"`)
	assert.Contains(t, string(body), `"provider":"vertex"`)
	assert.Contains(t, string(body), "data: [DONE]")
}

func TestAnthropicModeListModelsDefersToConfiguredModels(t *testing.T) {
	provider := anthropicModeProvider(t, "https://vertex.invalid", http.DefaultClient)

	_, err := provider.ListModels(context.Background())
	assert.ErrorIs(t, err, core.ErrModelListingUnsupported)
}

func TestVertexAnthropicBaseURL(t *testing.T) {
	root := "https://aiplatform.eu.rep.googleapis.com/v1/projects/p/locations/eu"
	tests := []struct {
		name string
		cfg  providers.ProviderConfig
		want string
	}{
		{"openai compatible base", providers.ProviderConfig{BaseURL: root + "/endpoints/openapi"}, root + "/publishers/anthropic"},
		{"native base", providers.ProviderConfig{BaseURL: root + "/publishers/google/"}, root + "/publishers/anthropic"},
		{"anthropic base", providers.ProviderConfig{BaseURL: root + "/publishers/anthropic"}, root + "/publishers/anthropic"},
		{
			"project and location",
			providers.ProviderConfig{VertexProject: "p", VertexLocation: "us-east5"},
			"https://aiplatform.googleapis.com/v1/projects/p/locations/us-east5/publishers/anthropic",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, vertexAnthropicBaseURL(tt.cfg))
		})
	}
}

func TestRawPredictURL(t *testing.T) {
	messagesURL, err := url.Parse("https://vertex.example" + anthropicBasePath + "/messages")
	require.NoError(t, err)
	tests := []struct {
		name   string
		model  string
		stream bool
		want   string
	}{
		{"bare id", "claude-sonnet-5", false, anthropicBasePath + "/models/claude-sonnet-5:rawPredict"},
		{"stream", "claude-sonnet-5", true, anthropicBasePath + "/models/claude-sonnet-5:streamRawPredict"},
		{"publisher prefix", "anthropic/claude-sonnet-5", false, anthropicBasePath + "/models/claude-sonnet-5:rawPredict"},
		{"resource name", "publishers/anthropic/models/claude-sonnet-5", false, anthropicBasePath + "/models/claude-sonnet-5:rawPredict"},
		{"version suffix", "claude-sonnet-4@20250514", false, anthropicBasePath + "/models/claude-sonnet-4@20250514:rawPredict"},
		{"separators stay in one segment", "a/../b?x", false, anthropicBasePath + "/models/a%2F..%2Fb%3Fx:rawPredict"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := rawPredictURL(messagesURL, tt.model, tt.stream)
			assert.Equal(t, tt.want, got.EscapedPath())
			assert.Equal(t, "vertex.example", got.Host)
		})
	}
}
