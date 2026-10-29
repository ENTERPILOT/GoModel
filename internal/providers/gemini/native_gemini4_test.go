package gemini

import (
	"context"
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

func TestFinishReasonFromGemini(t *testing.T) {
	tests := []struct {
		reason       string
		hasToolCalls bool
		want         string
	}{
		{reason: "STOP", want: "stop"},
		{reason: "MAX_TOKENS", want: "length"},
		{reason: "CONTINUATION", want: "length"},
		{reason: "STOP", hasToolCalls: true, want: "tool_calls"},
		{reason: "IMAGE_RECITATION", want: "content_filter"},
		{reason: "PROHIBITED_CONTENT", want: "content_filter"},
		{reason: "MALFORMED_FUNCTION_CALL", want: "malformed_function_call"},
		{reason: "", want: ""},
		{reason: "FINISH_REASON_UNSPECIFIED", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.reason, func(t *testing.T) {
			assert.Equal(t, tt.want, finishReasonFromGemini(tt.reason, tt.hasToolCalls))
		})
	}
}

func TestSupportsExplicitCaching(t *testing.T) {
	assert.True(t, supportsExplicitCaching("gemini-2.5-pro"))
	assert.True(t, supportsExplicitCaching("gemini-3.8-flash"))
	assert.True(t, supportsExplicitCaching("gemma-4-31b-it"))
	assert.False(t, supportsExplicitCaching("gemini-4-argon"))
	assert.False(t, supportsExplicitCaching("models/gemini-4-carbon"))
}

// Gemini 4 has no cachedContents ("Explicit caching is not supported"), so a
// cache plan must not cost a failing round trip before every request.
func TestPrepareCachedContentSkipsGemini4(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusBadRequest, `{"error":{"message":"Explicit caching is not supported for models/gemini-4-argon."}}`)
	p := newTestProvider("key", server.Client(), llmclient.Hooks{})
	p.SetBaseURL(server.URL)
	body := &geminiGenerateContentRequest{
		SystemInstruction: &geminiContent{Parts: []geminiPart{{Text: "system"}}},
		Contents:          []geminiContent{{Role: "user", Parts: []geminiPart{{Text: "live"}}}},
	}
	p.prepareCachedContent(context.Background(), &core.ChatRequest{Model: "gemini-4-argon", PromptCachePlan: &core.PromptCachePlan{Key: "plan"}}, body)
	assert.Equal(t, 0, capture.Count())
	assert.Empty(t, body.CachedContent)
	assert.NotNil(t, body.SystemInstruction)
}

func TestChatCompletion_NativeForwardsStoreOnAIStudioOnly(t *testing.T) {
	t.Setenv(useNativeAPIEnvVar, "true")
	const reply = `{"responseId":"r","candidates":[{"content":{"role":"model","parts":[{"text":"hi"}]},"finishReason":"STOP"}]}`

	var req core.ChatRequest
	require.NoError(t, json.Unmarshal([]byte(`{"model":"gemini-4-argon","messages":[{"role":"user","content":"hi"}],"store":false}`), &req))

	t.Run("ai studio sends store", func(t *testing.T) {
		server, capture := providertest.JSONServer(t, http.StatusOK, reply)
		provider := newTestProvider("key", nil, llmclient.Hooks{})
		provider.SetModelsURL(server.URL)
		_, err := provider.ChatCompletion(context.Background(), &req)
		require.NoError(t, err)
		sent := capture.Last(t).JSON(t)
		assert.Equal(t, false, sent["store"])
	})
	t.Run("vertex has no store member", func(t *testing.T) {
		server, capture := providertest.JSONServer(t, http.StatusOK, reply)
		provider := NewVertexWithHTTPClient(providers.ProviderConfig{BaseURL: server.URL + "/publishers/google", APIMode: "native"}, providertest.Options(llmclient.Hooks{}), server.Client())
		_, err := provider.ChatCompletion(context.Background(), &req)
		require.NoError(t, err)
		assert.NotContains(t, capture.Last(t).JSON(t), "store")
	})
	t.Run("no store member when the client set none", func(t *testing.T) {
		server, capture := providertest.JSONServer(t, http.StatusOK, reply)
		provider := newTestProvider("key", nil, llmclient.Hooks{})
		provider.SetModelsURL(server.URL)
		_, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{Model: "gemini-4-argon", Messages: []core.Message{{Role: "user", Content: "hi"}}})
		require.NoError(t, err)
		assert.NotContains(t, capture.Last(t).JSON(t), "store")
	})
}

func TestGeminiModelSupportedMethods_NanoBananaAndGemma(t *testing.T) {
	chat, embed, image := geminiModelSupportedMethods("gemini-nano-banana-2.1", []string{"generateContent", "countTokens"})
	assert.True(t, chat)
	assert.False(t, embed)
	assert.True(t, image, "Nano Banana IDs carry no -image marker")

	chat, _, image = geminiModelSupportedMethods("gemma-4-31b-it", nil)
	assert.True(t, chat, "Gemma is served by generateContent")
	assert.False(t, image)
	assert.True(t, isGeminiExposedModel("gemma-4-31b-it"))
}

func TestImageFailureMessage_ReportsFinishReason(t *testing.T) {
	resp := &geminiGenerateContentResponse{Candidates: []geminiCandidate{{
		FinishReason:  "IMAGE_RECITATION",
		FinishMessage: "Unable to show the generated image.",
	}}}
	assert.Equal(t, "Gemini returned no images: IMAGE_RECITATION (Unable to show the generated image.)", imageFailureMessage(resp, ""))

	stop := &geminiGenerateContentResponse{Candidates: []geminiCandidate{{FinishReason: "STOP"}}}
	assert.Equal(t, "Gemini returned no images: I cannot draw that", imageFailureMessage(stop, "I cannot draw that"))
}
