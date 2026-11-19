package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/echotest"
	"github.com/enterpilot/gomodel/internal/geminiapi"
	"github.com/enterpilot/gomodel/internal/usage"
)

const geminiTestBody = `{"systemInstruction":{"parts":[{"text":"be brief"}]},"contents":[{"role":"user","parts":[{"text":"Hi"}]}]}`

func geminiTestProvider() *capturingProvider {
	return &capturingProvider{
		supportedModels: []string{"gemini-test"},
		response: &core.ChatResponse{
			ID:     "resp-1",
			Object: "chat.completion",
			Model:  "gemini-test",
			Choices: []core.Choice{{
				Message:      core.ResponseMessage{Role: "assistant", Content: "Hello back"},
				FinishReason: "stop",
			}},
			Usage: core.Usage{PromptTokens: 9, CompletionTokens: 3, TotalTokens: 12},
		},
		streamData: strings.Join([]string{
			`data: {"id":"resp-2","model":"gemini-test","choices":[{"index":0,"delta":{"content":"Hi!"},"finish_reason":null}]}`,
			`data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":4,"completion_tokens":1,"total_tokens":5}}`,
			`data: [DONE]`,
			"",
		}, "\n\n"),
	}
}

func geminiError(t *testing.T, rec *httptest.ResponseRecorder) geminiapi.ErrorObject {
	t.Helper()
	return echotest.Decode[geminiapi.ErrorResponse](t, rec).Error
}

func TestGeminiGenerateContent_NonStreaming(t *testing.T) {
	provider := geminiTestProvider()
	usageLog := &collectingUsageLogger{config: usage.Config{Enabled: true}}
	handler := NewHandler(provider, nil, usageLog, nil)

	c, rec := echotest.Post(t, "/v1beta/models/gemini-test:generateContent", geminiTestBody)
	require.NoError(t, handler.GeminiModelMethod(c))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	assert.JSONEq(t, `{
		"candidates":[{"content":{"parts":[{"text":"Hello back"}],"role":"model"},"finishReason":"STOP","index":0}],
		"usageMetadata":{"promptTokenCount":9,"candidatesTokenCount":3,"totalTokenCount":12},
		"modelVersion":"gemini-test","responseId":"resp-1"
	}`, rec.Body.String())

	// The request was translated to the canonical chat type, with the model
	// taken from the path.
	require.NotNil(t, provider.capturedChatReq)
	assert.Equal(t, core.RequestDialectGeminiGenerateContent, core.RequestDialectFromContext(provider.capturedChatCtx))
	assert.Equal(t, "gemini-test", provider.capturedChatReq.Model)
	assert.False(t, provider.capturedChatReq.Stream)
	require.Len(t, provider.capturedChatReq.Messages, 2)
	assert.Equal(t, "system", provider.capturedChatReq.Messages[0].Role)
	assert.Equal(t, "user", provider.capturedChatReq.Messages[1].Role)

	// Usage is recorded under the path template, not one endpoint per model.
	require.Len(t, usageLog.entries, 1)
	assert.Equal(t, core.GeminiGenerateContentEndpoint, usageLog.entries[0].Endpoint)
	assert.Equal(t, 9, usageLog.entries[0].InputTokens)
	assert.Equal(t, 3, usageLog.entries[0].OutputTokens)
}

func TestGeminiGenerateContent_Streaming(t *testing.T) {
	provider := geminiTestProvider()
	usageLog := &collectingUsageLogger{config: usage.Config{Enabled: true}}
	handler := NewHandler(provider, nil, usageLog, nil)

	c, rec := echotest.Post(t, "/v1beta/models/gemini-test:streamGenerateContent?alt=sse", geminiTestBody)
	require.NoError(t, handler.GeminiModelMethod(c))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "text/event-stream", rec.Header().Get("Content-Type"))

	assert.Equal(t,
		`data: {"candidates":[{"content":{"parts":[{"text":"Hi!"}],"role":"model"},"index":0}],"modelVersion":"gemini-test","responseId":"resp-2"}`+"\n\n"+
			`data: {"candidates":[{"content":{"parts":[{"text":""}],"role":"model"},"finishReason":"STOP","index":0}],`+
			`"usageMetadata":{"promptTokenCount":4,"candidatesTokenCount":1,"totalTokenCount":5},"modelVersion":"gemini-test","responseId":"resp-2"}`+"\n\n",
		rec.Body.String())

	// include_usage must be set so the converter sees the final usage chunk.
	require.NotNil(t, provider.capturedChatReq)
	assert.True(t, provider.capturedChatReq.Stream)
	require.NotNil(t, provider.capturedChatReq.StreamOptions)
	assert.True(t, provider.capturedChatReq.StreamOptions.IncludeUsage)

	require.Len(t, usageLog.entries, 1)
	assert.Equal(t, core.GeminiStreamGenerateContentEndpoint, usageLog.entries[0].Endpoint)
	assert.Equal(t, 4, usageLog.entries[0].InputTokens)
}

func TestGeminiGenerateContent_Errors(t *testing.T) {
	tests := []struct {
		name    string
		target  string
		body    string
		status  int
		rpc     string
		message string
	}{
		{
			name:   "stream without alt=sse",
			target: "/v1beta/models/gemini-test:streamGenerateContent", body: geminiTestBody,
			status: http.StatusBadRequest, rpc: "INVALID_ARGUMENT",
			message: "streamGenerateContent is served as server-sent events only: add alt=sse to the query",
		},
		{
			name:   "unsupported method",
			target: "/v1beta/models/gemini-test:countTokens", body: geminiTestBody,
			status: http.StatusNotFound, rpc: "NOT_FOUND",
			message: "Gemini API method countTokens is not supported; use generateContent or streamGenerateContent",
		},
		{
			name:   "no method",
			target: "/v1beta/models/gemini-test", body: geminiTestBody,
			status: http.StatusNotFound, rpc: "NOT_FOUND",
			message: "unknown API endpoint: POST /v1beta/models/gemini-test",
		},
		{
			name:   "malformed body",
			target: "/v1beta/models/gemini-test:generateContent", body: `{"contents":`,
			status: http.StatusBadRequest, rpc: "INVALID_ARGUMENT",
		},
		{
			name:   "untranslatable member",
			target: "/v1beta/models/gemini-test:generateContent", body: `{"contents":[{"parts":[{"text":"Hi"}]}],"tools":[{"googleSearch":{}}]}`,
			status: http.StatusBadRequest, rpc: "INVALID_ARGUMENT",
			message: "tools[0].googleSearch: built-in tools are not supported on this endpoint; only functionDeclarations translate",
		},
		{
			name:   "unknown model",
			target: "/v1beta/models/nope:generateContent", body: geminiTestBody,
			status: http.StatusNotFound, rpc: "NOT_FOUND",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := geminiTestProvider()
			handler := NewHandler(provider, nil, nil, nil)

			c, rec := echotest.Post(t, tt.target, tt.body)
			require.NoError(t, handler.GeminiModelMethod(c))

			// Every failure uses the Gemini error envelope, so SDK clients
			// raise their own typed errors.
			require.Equal(t, tt.status, rec.Code, rec.Body.String())
			got := geminiError(t, rec)
			assert.Equal(t, tt.status, got.Code)
			assert.Equal(t, tt.rpc, got.Status)
			if tt.message != "" {
				assert.Equal(t, tt.message, got.Message)
			}
			assert.NotEmpty(t, got.Message)
			assert.Nil(t, provider.capturedChatReq, "a rejected request must not reach a provider")
		})
	}
}

func TestGeminiGenerateContent_ProviderErrorUsesGeminiEnvelope(t *testing.T) {
	provider := geminiTestProvider()
	provider.err = core.NewRateLimitError("gemini", "quota exceeded")
	handler := NewHandler(provider, nil, nil, nil)

	c, rec := echotest.Post(t, "/v1beta/models/gemini-test:generateContent", geminiTestBody)
	require.NoError(t, handler.GeminiModelMethod(c))

	require.Equal(t, http.StatusTooManyRequests, rec.Code, rec.Body.String())
	got := geminiError(t, rec)
	assert.Equal(t, "RESOURCE_EXHAUSTED", got.Status)
	assert.Equal(t, "quota exceeded", got.Message)
}

func TestGeminiModels(t *testing.T) {
	window := 1000
	provider := &mockProvider{modelsResponse: &core.ModelsResponse{Object: "list", Data: []core.Model{
		{ID: "gemini/gemini-test", Object: "model", Metadata: &core.ModelMetadata{DisplayName: "Gemini Test", ContextWindow: &window}},
		{ID: "openai/gpt-test", Object: "model"},
	}}}
	handler := NewHandler(provider, nil, nil, nil)

	c, rec := echotest.Get(t, "/v1beta/models")
	require.NoError(t, handler.ListGeminiModels(c))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	list := echotest.Decode[geminiapi.ModelsList](t, rec)
	require.Len(t, list.Models, 2)
	assert.Equal(t, "models/gemini/gemini-test", list.Models[0].Name)
	assert.Equal(t, "Gemini Test", list.Models[0].DisplayName)
	assert.Equal(t, 1000, list.Models[0].InputTokenLimit)
	assert.Equal(t, "models/openai/gpt-test", list.Models[1].Name)
	assert.Empty(t, list.NextPageToken)

	c, rec = echotest.Get(t, "/v1beta/models/openai/gpt-test", echotest.WithPath("/v1beta/models/*"), echotest.WithPathValue("*", "openai/gpt-test"))
	require.NoError(t, handler.GetGeminiModel(c))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "models/openai/gpt-test", echotest.Decode[geminiapi.ModelInfo](t, rec).Name)

	c, rec = echotest.Get(t, "/v1beta/models/nope", echotest.WithPath("/v1beta/models/*"), echotest.WithPathValue("*", "nope"))
	require.NoError(t, handler.GetGeminiModel(c))
	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
	assert.Equal(t, "NOT_FOUND", geminiError(t, rec).Status)
}

// The whole middleware chain: Gemini credentials authenticate, the workflow
// resolves the model named in the path, and the handler answers in the Gemini
// shape.
func TestGeminiAPI_ThroughServer(t *testing.T) {
	const masterKey = "secret-key-123"
	serve := func(t *testing.T, method, target string, header http.Header) (*httptest.ResponseRecorder, *capturingProvider) {
		t.Helper()
		provider := geminiTestProvider()
		provider.modelsResponse = &core.ModelsResponse{Object: "list", Data: []core.Model{{ID: "gemini-test", Object: "model"}}}
		srv := New(provider, &Config{MasterKey: masterKey})
		var body *strings.Reader
		if method == http.MethodPost {
			body = strings.NewReader(geminiTestBody)
		} else {
			body = strings.NewReader("")
		}
		req := httptest.NewRequest(method, target, body)
		req.Header = header
		if method == http.MethodPost {
			req.Header.Set("Content-Type", "application/json")
		}
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		return rec, provider
	}
	const generate = "/v1beta/models/gemini-test:generateContent"

	t.Run("x-goog-api-key header", func(t *testing.T) {
		rec, provider := serve(t, http.MethodPost, generate, http.Header{"X-Goog-Api-Key": {masterKey}})
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		assert.Contains(t, rec.Body.String(), `"text":"Hello back"`)
		require.NotNil(t, provider.capturedChatReq)
		assert.Equal(t, "gemini-test", provider.capturedChatReq.Model)
	})

	t.Run("key query parameter", func(t *testing.T) {
		rec, _ := serve(t, http.MethodPost, "/v1beta/models/gemini-test:streamGenerateContent?alt=sse&key="+masterKey, http.Header{})
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		assert.Contains(t, rec.Body.String(), `"finishReason":"STOP"`)
	})

	t.Run("bearer token", func(t *testing.T) {
		rec, _ := serve(t, http.MethodPost, generate, http.Header{"Authorization": {"Bearer " + masterKey}})
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	})

	t.Run("model list", func(t *testing.T) {
		rec, _ := serve(t, http.MethodGet, "/v1beta/models?key="+masterKey, http.Header{})
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		assert.Contains(t, rec.Body.String(), `"name":"models/gemini-test"`)
	})

	denied := []struct {
		name    string
		method  string
		target  string
		header  http.Header
		message string
	}{
		{name: "no credential", method: http.MethodPost, target: generate, message: "missing credentials: send 'x-goog-api-key: <token>' or 'Authorization: Bearer <token>'"},
		{name: "wrong header key", method: http.MethodPost, target: generate, header: http.Header{"X-Goog-Api-Key": {"wrong"}}, message: "invalid master key"},
		{name: "wrong query key", method: http.MethodPost, target: generate + "?key=wrong", message: "invalid master key"},
		{name: "unknown route", method: http.MethodGet, target: "/v1beta/cachedContents", message: "missing credentials: send 'x-goog-api-key: <token>' or 'Authorization: Bearer <token>'"},
	}
	for _, tt := range denied {
		t.Run(tt.name, func(t *testing.T) {
			header := tt.header
			if header == nil {
				header = http.Header{}
			}
			rec, provider := serve(t, tt.method, tt.target, header)
			require.Equal(t, http.StatusUnauthorized, rec.Code, rec.Body.String())
			got := geminiError(t, rec)
			assert.Equal(t, "UNAUTHENTICATED", got.Status)
			assert.Equal(t, tt.message, got.Message)
			assert.Nil(t, provider.capturedChatReq)
		})
	}

	t.Run("unknown route answers in the Gemini envelope", func(t *testing.T) {
		rec, _ := serve(t, http.MethodGet, "/v1beta/cachedContents", http.Header{"X-Goog-Api-Key": {masterKey}})
		require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
		assert.Equal(t, "NOT_FOUND", geminiError(t, rec).Status)
	})

	// The Gemini credentials are read on the Gemini routes only: no other
	// route starts accepting a key in its URL.
	for name, target := range map[string]string{
		"query key":  "/v1/models?key=" + masterKey,
		"header key": "/v1/models",
	} {
		t.Run("other routes ignore the "+name, func(t *testing.T) {
			rec, _ := serve(t, http.MethodGet, target, http.Header{"X-Goog-Api-Key": {masterKey}})
			require.Equal(t, http.StatusUnauthorized, rec.Code, rec.Body.String())
		})
	}
}

func TestAuthMiddleware_StripsGeminiQueryKey(t *testing.T) {
	const masterKey = "secret-key-123"
	tests := []struct {
		name      string
		target    string
		wantQuery string
	}{
		{name: "key removed, other parameters kept", target: "/v1beta/models/m:streamGenerateContent?alt=sse&key=" + masterKey, wantQuery: "alt=sse"},
		{name: "only the key", target: "/v1beta/models?key=" + masterKey, wantQuery: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var seen string
			handler := AuthMiddleware(masterKey, nil)(func(c *echo.Context) error {
				seen = c.Request().URL.RawQuery
				return c.String(http.StatusOK, "ok")
			})
			c, rec := echotest.Get(t, tt.target)
			require.NoError(t, handler(c))
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			assert.Equal(t, tt.wantQuery, seen)
		})
	}

	t.Run("a rejected key is removed too", func(t *testing.T) {
		c, rec := echotest.Get(t, "/v1beta/models?key=wrong")
		handler := AuthMiddleware(masterKey, nil)(func(c *echo.Context) error { return c.String(http.StatusOK, "ok") })
		require.NoError(t, handler(c))
		require.Equal(t, http.StatusUnauthorized, rec.Code)
		assert.Empty(t, c.Request().URL.RawQuery)
	})

	t.Run("other routes keep their query", func(t *testing.T) {
		c, rec := echotest.Get(t, "/v1/files?key=abc", echotest.WithHeader("Authorization", "Bearer "+masterKey))
		handler := AuthMiddleware(masterKey, nil)(func(c *echo.Context) error { return c.String(http.StatusOK, "ok") })
		require.NoError(t, handler(c))
		require.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "key=abc", c.Request().URL.RawQuery)
	})
}
