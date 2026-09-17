package providertest

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ChatCompletionJSON is a minimal OpenAI-shaped chat completion reply whose
// model is Model and whose single choice says Reply.
const ChatCompletionJSON = `{
	"id":"chatcmpl-test",
	"object":"chat.completion",
	"created":1677652288,
	"model":"` + Model + `",
	"choices":[{"index":0,"message":{"role":"assistant","content":"` + Reply + `"},"finish_reason":"stop"}],
	"usage":{"prompt_tokens":5,"completion_tokens":1,"total_tokens":6}
}`

// ChatChunkSSE is a one-chunk chat completion stream ending in [DONE].
const ChatChunkSSE = "data: {\"id\":\"chatcmpl-test\",\"object\":\"chat.completion.chunk\",\"model\":\"" + Model + "\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"" + Reply + "\"}}]}\n\ndata: [DONE]\n\n"

// ResponsesJSON is a minimal Responses API reply whose single message says
// Reply.
const ResponsesJSON = `{
	"id":"` + ResponsesID + `",
	"object":"response",
	"model":"` + Model + `",
	"status":"completed",
	"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"` + Reply + `"}]}],
	"usage":{"input_tokens":5,"output_tokens":1,"total_tokens":6}
}`

// ResponsesID is the response ID the native Responses fixtures carry.
const ResponsesID = "resp-test"

// ResponsesSSE is a Responses API stream that opens with response.created,
// carries one text delta, and ends in [DONE].
const ResponsesSSE = "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"" + ResponsesID + "\",\"object\":\"response\",\"model\":\"" + Model + "\",\"status\":\"in_progress\"}}\n\n" +
	"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"" + Reply + "\"}\n\ndata: [DONE]\n\n"

// ModelsJSON lists Model as the only available model.
const ModelsJSON = `{"object":"list","data":[{"id":"` + Model + `","object":"model","owned_by":"test"}]}`

// EmbeddingsJSON is a one-vector embeddings reply.
const EmbeddingsJSON = `{"object":"list","data":[{"object":"embedding","index":0,"embedding":[0.1,0.2]}],"model":"` + Model + `","usage":{"prompt_tokens":2,"total_tokens":2}}`

// Model, Prompt, and Reply are the model ID, user text, and assistant text
// used by the fixtures and the contract requests.
const (
	Model  = "test-model"
	Prompt = "hi"
	Reply  = "hello"
)

// ChatCompatible describes a provider built on the shared OpenAI-compatible
// adapter so AssertChatCompatible can check the contract every such provider
// shares.
type ChatCompatible struct {
	// Registration is the provider's factory registration. When its
	// Discovery.AllowAPIKeyless is set, the helper also checks that keyless
	// requests carry no credentials.
	Registration providers.Registration
	// Type is the expected Registration.Type.
	Type string
	// DefaultBaseURL is the expected Registration.Discovery.DefaultBaseURL.
	DefaultBaseURL string
	// New builds the provider on the given base URL and HTTP client.
	New func(apiKey, baseURL string, client *http.Client, hooks llmclient.Hooks) core.Provider
	// NativeResponses reports whether the provider forwards Responses API
	// requests to the upstream /responses endpoint. When false the helper
	// expects them translated to chat completions.
	NativeResponses bool
	// Embeddings reports whether the provider forwards embeddings upstream.
	// When false the helper asserts a typed invalid-request error and no
	// upstream call.
	Embeddings bool
	// SkipEmbeddings leaves embeddings to the provider's own tests, for
	// providers that call a native, non-OpenAI embeddings endpoint.
	SkipEmbeddings bool
	// AuthHeader and AuthPrefix describe how the API key is sent. They
	// default to Authorization and "Bearer ".
	AuthHeader string
	AuthPrefix string
}

// countingTransport records how many requests travelled through it, so the
// contract can tell a provider that used the client it was given from one
// that fell back to a default client the test server also answers.
type countingTransport struct {
	base  http.RoundTripper
	calls atomic.Int64
}

func (t *countingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.calls.Add(1)
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(req)
}

// AssertChatCompatible checks the contract shared by providers that embed
// the OpenAI-compatible adapter: registration metadata, constructor safety,
// and that chat, streaming, model listing, Responses, and embeddings reach
// the expected upstream paths with the API key attached.
func AssertChatCompatible(t *testing.T, p ChatCompatible) {
	t.Helper()
	if p.AuthHeader == "" {
		p.AuthHeader = "Authorization"
	}
	if p.AuthPrefix == "" && p.AuthHeader == "Authorization" {
		p.AuthPrefix = "Bearer "
	}
	const apiKey = "test-api-key"
	wantAuth := p.AuthPrefix + apiKey

	t.Run("registration", func(t *testing.T) {
		assert.Equal(t, p.Type, p.Registration.Type, "Registration.Type")
		require.NotNil(t, p.Registration.New, "Registration.New")
		assert.Equal(t, p.DefaultBaseURL, p.Registration.Discovery.DefaultBaseURL, "Registration.Discovery.DefaultBaseURL")
		provider := p.Registration.New(providers.ProviderConfig{APIKey: apiKey}, providers.ProviderOptions{})
		assert.NotNil(t, provider, "Registration.New returned nil")
	})

	t.Run("constructor tolerates nil client and zero hooks", func(t *testing.T) {
		provider := p.New(apiKey, "http://example.invalid", nil, llmclient.Hooks{})
		assert.NotNil(t, provider, "constructor with a nil HTTP client returned nil")
	})

	// The test server is reachable by http.DefaultClient too, so every other
	// assertion here would still pass if a constructor quietly ignored the
	// client it was handed. Count the requests that actually travelled through
	// the supplied transport.
	t.Run("sends through the supplied HTTP client", func(t *testing.T) {
		server, _ := JSONServer(t, http.StatusOK, ChatCompletionJSON)
		counted := &countingTransport{base: server.Client().Transport}
		provider := p.New(apiKey, server.URL, &http.Client{Transport: counted}, llmclient.Hooks{})
		require.NotNil(t, provider)
		_, err := provider.ChatCompletion(context.Background(), chatRequest())
		require.NoError(t, err)
		assert.Positive(t, counted.calls.Load(), "the provider must send through the client it was given, not a default one")
	})

	t.Run("chat completion via registered factory", func(t *testing.T) {
		server, capture := JSONServer(t, http.StatusOK, ChatCompletionJSON)
		provider := p.Registration.New(providers.ProviderConfig{APIKey: apiKey, BaseURL: server.URL}, providers.ProviderOptions{})
		resp, err := provider.ChatCompletion(context.Background(), chatRequest())
		require.NoError(t, err)
		req := capture.Last(t)
		assertUpstream(t, req, http.MethodPost, "/chat/completions", p.AuthHeader, wantAuth)
		assertChatRequest(t, req.JSON(t), false)
		assert.Equal(t, Model, resp.Model)
		require.Len(t, resp.Choices, 1)
		assert.Equal(t, Reply, resp.Choices[0].Message.Content)
		assert.Equal(t, 5, resp.Usage.PromptTokens, "usage prompt tokens")
		assert.Equal(t, 1, resp.Usage.CompletionTokens, "usage completion tokens")
		assert.Equal(t, 6, resp.Usage.TotalTokens, "usage total tokens")
	})

	if p.Registration.Discovery.AllowAPIKeyless {
		t.Run("keyless requests carry no credentials", func(t *testing.T) {
			server, capture := JSONServer(t, http.StatusOK, ChatCompletionJSON)
			provider := p.Registration.New(providers.ProviderConfig{BaseURL: server.URL}, providers.ProviderOptions{})
			require.NotNil(t, provider)
			_, err := provider.ChatCompletion(context.Background(), chatRequest())
			require.NoError(t, err)
			assert.Empty(t, capture.Last(t).Header.Get(p.AuthHeader), "upstream %s header", p.AuthHeader)
		})
	}

	t.Run("stream chat completion", func(t *testing.T) {
		server, capture := SSEServer(t, ChatChunkSSE)
		provider := p.New(apiKey, server.URL, server.Client(), llmclient.Hooks{})
		stream, err := provider.StreamChatCompletion(context.Background(), chatRequest())
		require.NoError(t, err)
		body := readAll(t, stream)
		req := capture.Last(t)
		assertUpstream(t, req, http.MethodPost, "/chat/completions", p.AuthHeader, wantAuth)
		assertChatRequest(t, req.JSON(t), true)
		assertStreamBody(t, body, Reply, "data: [DONE]")
	})

	t.Run("list models", func(t *testing.T) {
		server, capture := JSONServer(t, http.StatusOK, ModelsJSON)
		provider := p.New(apiKey, server.URL, server.Client(), llmclient.Hooks{})
		resp, err := provider.ListModels(context.Background())
		require.NoError(t, err)
		// Some providers probe per-model details after listing, so the
		// listing is the first upstream request rather than the last.
		requests := capture.All()
		require.NotEmpty(t, requests)
		assertUpstream(t, requests[0], http.MethodGet, "/models", p.AuthHeader, wantAuth)
		require.Len(t, resp.Data, 1)
		assert.Equal(t, Model, resp.Data[0].ID)
	})

	// Responses reach the upstream either translated to chat completions or
	// forwarded to its own /responses endpoint; only the path, fixtures, and
	// request shape differ.
	// Translation keeps the chat completion ID on the reply but mints a resp_
	// ID for the stream; native forwarding keeps the upstream ID for both.
	responsesMode, responsesPath := "translate to chat completions", "/chat/completions"
	responsesReply, responsesStream := ChatCompletionJSON, ChatChunkSSE
	checkResponsesRequest := assertChatRequest
	wantResponseID := "chatcmpl-test"
	checkStreamID := func(t testing.TB, id string) {
		assert.True(t, strings.HasPrefix(id, "resp_"), "response.created id = %q, want a generated resp_ ID", id)
	}
	if p.NativeResponses {
		responsesMode, responsesPath = "forward to the upstream responses endpoint", "/responses"
		responsesReply, responsesStream = ResponsesJSON, ResponsesSSE
		checkResponsesRequest = assertResponsesRequest
		wantResponseID = ResponsesID
		checkStreamID = func(t testing.TB, id string) {
			assert.Equal(t, ResponsesID, id, "response.created id")
		}
	}

	t.Run("responses "+responsesMode, func(t *testing.T) {
		server, capture := JSONServer(t, http.StatusOK, responsesReply)
		provider := p.New(apiKey, server.URL, server.Client(), llmclient.Hooks{})
		resp, err := provider.Responses(context.Background(), responsesRequest())
		require.NoError(t, err)
		req := capture.Last(t)
		assertUpstream(t, req, http.MethodPost, responsesPath, p.AuthHeader, wantAuth)
		checkResponsesRequest(t, req.JSON(t), false)
		assertResponse(t, resp)
		assert.Equal(t, wantResponseID, resp.ID, "response id")
	})

	t.Run("stream responses "+responsesMode, func(t *testing.T) {
		server, capture := SSEServer(t, responsesStream)
		provider := p.New(apiKey, server.URL, server.Client(), llmclient.Hooks{})
		stream, err := provider.StreamResponses(context.Background(), responsesRequest())
		require.NoError(t, err)
		body := readAll(t, stream)
		req := capture.Last(t)
		assertUpstream(t, req, http.MethodPost, responsesPath, p.AuthHeader, wantAuth)
		checkResponsesRequest(t, req.JSON(t), true)
		assertStreamBody(t, body, "response.output_text.delta", Reply, "data: [DONE]")
		createdAt := strings.Index(string(body), `"type":"response.created"`)
		deltaAt := strings.Index(string(body), `"type":"response.output_text.delta"`)
		require.GreaterOrEqual(t, createdAt, 0, "response.created event")
		require.GreaterOrEqual(t, deltaAt, 0, "response.output_text.delta event")
		assert.Less(t, createdAt, deltaAt, "response.created must precede output events")
		created := responseCreated(t, body)
		checkStreamID(t, fmt.Sprint(created["id"]))
		assert.Equal(t, Model, created["model"], "response.created model")
	})

	if p.SkipEmbeddings {
		return
	}
	t.Run("embeddings", func(t *testing.T) {
		server, capture := JSONServer(t, http.StatusOK, EmbeddingsJSON)
		provider := p.New(apiKey, server.URL, server.Client(), llmclient.Hooks{})
		resp, err := provider.Embeddings(context.Background(), &core.EmbeddingRequest{Model: Model, Input: Prompt})
		if !p.Embeddings {
			AssertUnsupported(t, err)
			assert.Zero(t, capture.Count(), "embeddings must not be forwarded upstream")
			return
		}
		require.NoError(t, err)
		req := capture.Last(t)
		assertUpstream(t, req, http.MethodPost, "/embeddings", p.AuthHeader, wantAuth)
		sent := req.JSON(t)
		assert.Equal(t, Model, sent["model"], "embeddings request model")
		assert.Equal(t, Prompt, sent["input"], "embeddings request input")
		require.Len(t, resp.Data, 1)
		var vector []float64
		require.NoError(t, json.Unmarshal(resp.Data[0].Embedding, &vector))
		assert.Equal(t, []float64{0.1, 0.2}, vector)
	})
}

// AssertUnsupported checks that err is the typed invalid-request error a
// provider returns for a surface it does not offer.
func AssertUnsupported(t testing.TB, err error) {
	t.Helper()
	require.Error(t, err, "want typed unsupported error")
	var gwErr *core.GatewayError
	require.ErrorAs(t, err, &gwErr)
	assert.Equal(t, core.ErrorTypeInvalidRequest, gwErr.Type)
	assert.Equal(t, http.StatusBadRequest, gwErr.HTTPStatusCode())
}

// AssertNoNativeSurfaces checks that provider does not advertise the optional
// native batch, file, or audio interfaces, so a provider built on the shared
// adapter cannot accidentally claim capabilities its upstream lacks.
func AssertNoNativeSurfaces(t testing.TB, provider any) {
	t.Helper()
	_, ok := provider.(core.NativeBatchProvider)
	assert.False(t, ok, "provider should not implement core.NativeBatchProvider")
	_, ok = provider.(core.NativeFileProvider)
	assert.False(t, ok, "provider should not implement core.NativeFileProvider")
	_, ok = provider.(core.AudioProvider)
	assert.False(t, ok, "provider should not implement core.AudioProvider")
}

func chatRequest() *core.ChatRequest {
	return &core.ChatRequest{
		Model:    Model,
		Messages: []core.Message{{Role: "user", Content: Prompt}},
	}
}

func responsesRequest() *core.ResponsesRequest {
	return &core.ResponsesRequest{Model: Model, Input: Prompt}
}

func readAll(t testing.TB, stream io.ReadCloser) []byte {
	t.Helper()
	require.NotNil(t, stream, "stream")
	defer stream.Close()
	body, err := io.ReadAll(stream)
	require.NoError(t, err)
	return body
}

// assertChatRequest checks a chat completions body: the model, the stream
// flag, and that the user prompt survived any translation.
func assertChatRequest(t testing.TB, sent map[string]any, stream bool) {
	t.Helper()
	assert.Equal(t, Model, sent["model"], "request model")
	if stream {
		assert.Equal(t, true, sent["stream"], "request stream")
	}
	messages, _ := sent["messages"].([]any)
	found := false
	for _, m := range messages {
		msg, _ := m.(map[string]any)
		if msg["role"] == "user" && msg["content"] == Prompt {
			found = true
		}
	}
	assert.True(t, found, "request messages = %#v, want a user message %q", sent["messages"], Prompt)
}

// assertResponsesRequest checks a forwarded Responses API body.
func assertResponsesRequest(t testing.TB, sent map[string]any, stream bool) {
	t.Helper()
	assert.Equal(t, Model, sent["model"], "request model")
	assert.Equal(t, Prompt, sent["input"], "request input")
	if stream {
		assert.Equal(t, true, sent["stream"], "request stream")
	}
}

// assertResponse checks a completed Responses API reply that says Reply,
// including the normalized model and token usage both fixtures carry.
func assertResponse(t testing.TB, resp *core.ResponsesResponse) {
	t.Helper()
	require.NotNil(t, resp)
	assert.Equal(t, "response", resp.Object)
	assert.Equal(t, "completed", resp.Status)
	assert.Equal(t, Model, resp.Model)
	assert.Equal(t, Reply, outputText(resp))
	require.NotNil(t, resp.Usage, "usage")
	assert.Equal(t, 5, resp.Usage.InputTokens, "usage input tokens")
	assert.Equal(t, 1, resp.Usage.OutputTokens, "usage output tokens")
	assert.Equal(t, 6, resp.Usage.TotalTokens, "usage total tokens")
}

// responseCreated returns the response object of the stream's
// response.created event.
func responseCreated(t testing.TB, body []byte) map[string]any {
	t.Helper()
	for line := range strings.SplitSeq(string(body), "\n") {
		data, ok := strings.CutPrefix(line, "data: ")
		if !ok {
			continue
		}
		var event map[string]any
		if json.Unmarshal([]byte(data), &event) != nil || event["type"] != "response.created" {
			continue
		}
		response, ok := event["response"].(map[string]any)
		require.True(t, ok, "response.created event = %s, want a response object", data)
		return response
	}
	require.FailNow(t, "stream has no response.created event", "stream body = %q", body)
	return nil
}

// assertStreamBody checks that each expected fragment appears in the stream.
func assertStreamBody(t testing.TB, body []byte, want ...string) {
	t.Helper()
	for _, fragment := range want {
		assert.Contains(t, string(body), fragment)
	}
}

// outputText concatenates the assistant output_text parts of a response.
func outputText(resp *core.ResponsesResponse) string {
	var text strings.Builder
	for _, item := range resp.Output {
		if item.Type != "message" {
			continue
		}
		for _, part := range item.Content {
			if part.Type == "output_text" {
				text.WriteString(part.Text)
			}
		}
	}
	return text.String()
}

func assertUpstream(t testing.TB, req Recorded, method, path, authHeader, wantAuth string) {
	t.Helper()
	assert.Equal(t, method, req.Method, "upstream method")
	assert.Equal(t, path, req.Path, "upstream path")
	assert.Equal(t, wantAuth, req.Header.Get(authHeader), "upstream %s header", authHeader)
}
