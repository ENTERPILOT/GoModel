package gemini

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
)

var _ core.ChatTokenCounter = (*Provider)(nil)

func newCountTokensProvider(baseURL, apiMode string) *Provider {
	cfg := providers.ProviderConfig{APIKey: "key", BaseURL: baseURL, APIMode: apiMode}
	return New(cfg, providertest.Options(llmclient.Hooks{})).(*Provider)
}

// The count comes from the Gemini API's countTokens for the request as the
// native translation renders it, wrapped in generateContentRequest and without
// the members that do not make up the input. Nothing else is called: counting
// creates no cached content.
func TestCountChatTokens(t *testing.T) {
	for _, model := range []string{"gemini-4-argon", "models/gemini-4-argon", "google/gemini-4-argon"} {
		t.Run(model, func(t *testing.T) {
			server, capture := providertest.JSONServer(t, http.StatusOK,
				`{"totalTokens":36,"promptTokensDetails":[{"modality":"TEXT","tokenCount":36}]}`)
			provider := newCountTokensProvider(server.URL, geminiAPIModeNative)

			maxTokens := 1
			got, err := provider.CountChatTokens(context.Background(), &core.ChatRequest{
				Model:      model,
				MaxTokens:  &maxTokens,
				Reasoning:  &core.Reasoning{Effort: "low"},
				ToolChoice: "required",
				Tools: []map[string]any{{"type": "function", "function": map[string]any{
					"name": "get_weather", "parameters": map[string]any{"type": "object"},
				}}},
				Messages: []core.Message{
					{Role: "system", Content: "Be terse."},
					{Role: "user", Content: []core.ContentPart{
						{Type: "text", Text: "What is this?"},
						{Type: "image_url", ImageURL: &core.ImageURLContent{URL: "data:image/png;base64,aGk="}},
					}},
				},
				ExtraFields: core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{
					"cached_content": json.RawMessage(`"cachedContents/abc"`),
					"store":          json.RawMessage(`false`),
				}),
			})
			require.NoError(t, err)
			assert.Equal(t, 36, got)
			assert.Equal(t, 1, capture.Count())

			sent := capture.Last(t)
			assert.Equal(t, http.MethodPost, sent.Method)
			assert.Equal(t, "/models/gemini-4-argon:countTokens", sent.Path)
			assert.Equal(t, "key", sent.Header.Get("x-goog-api-key"))

			var body map[string]map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(sent.Body, &body))
			require.Len(t, body, 1, "only the wrapped request is sent")
			counted := body["generateContentRequest"]
			assert.JSONEq(t, `"models/gemini-4-argon"`, string(counted["model"]))
			assert.JSONEq(t, `{"parts":[{"text":"Be terse."}]}`, string(counted["system_instruction"]))
			assert.JSONEq(t, `[{"role":"user","parts":[{"text":"What is this?"},{"inline_data":{"mime_type":"image/png","data":"aGk="}}]}]`,
				string(counted["contents"]))
			assert.JSONEq(t, `[{"functionDeclarations":[{"name":"get_weather","parametersJsonSchema":{"type":"object"}}]}]`,
				string(counted["tools"]))
			assert.JSONEq(t, `{"maxOutputTokens":1,"thinkingConfig":{"thinkingLevel":"low"}}`, string(counted["generationConfig"]))
			assert.Len(t, counted, 5, "tool config, cached content and store are left out")
		})
	}
}

// Without a counting call for the mode, backend or request, the caller is told
// to estimate and nothing is sent upstream.
func TestCountChatTokens_Unsupported(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, `{"totalTokens":36}`)
	textRequest := &core.ChatRequest{Model: "gemini-4-argon", Messages: []core.Message{{Role: "user", Content: "hi"}}}
	tests := []struct {
		name     string
		provider *Provider
		req      *core.ChatRequest
	}{
		{name: "openai compatible mode", provider: newCountTokensProvider(server.URL, geminiAPIModeOpenAICompatible), req: textRequest},
		{name: "vertex backend", provider: newVertexTestProvider(server, true), req: textRequest},
		{name: "nil request", provider: newCountTokensProvider(server.URL, geminiAPIModeNative)},
		{
			name:     "request the native translation refuses",
			provider: newCountTokensProvider(server.URL, geminiAPIModeNative),
			req: &core.ChatRequest{Model: "gemini-4-argon", Messages: []core.Message{{Role: "user", Content: []core.ContentPart{
				{Type: "image_url", ImageURL: &core.ImageURLContent{URL: "https://example.com/image.png"}},
			}}}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := tt.provider.CountChatTokens(context.Background(), tt.req)
			require.ErrorIs(t, err, core.ErrMessagesTokenCountUnsupported)
		})
	}
	assert.Zero(t, capture.Count())
}

// An upstream failure is returned as the provider's error, which the Messages
// handler answers with its estimate; a reply without a count is unsupported
// rather than a count of zero.
func TestCountChatTokens_UpstreamFailure(t *testing.T) {
	req := &core.ChatRequest{Model: "gemini-4-argon", Messages: []core.Message{{Role: "user", Content: "hi"}}}

	server, _ := providertest.JSONServer(t, http.StatusBadRequest, `{"error":{"code":400,"message":"Invalid JSON payload","status":"INVALID_ARGUMENT"}}`)
	_, err := newCountTokensProvider(server.URL, geminiAPIModeNative).CountChatTokens(context.Background(), req)
	require.Error(t, err)
	require.NotErrorIs(t, err, core.ErrMessagesTokenCountUnsupported)
	var gatewayErr *core.GatewayError
	require.ErrorAs(t, err, &gatewayErr)
	assert.Equal(t, http.StatusBadRequest, gatewayErr.HTTPStatusCode())

	server, _ = providertest.JSONServer(t, http.StatusOK, `{}`)
	_, err = newCountTokensProvider(server.URL, geminiAPIModeNative).CountChatTokens(context.Background(), req)
	require.ErrorIs(t, err, core.ErrMessagesTokenCountUnsupported)
}
