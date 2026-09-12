package providertest

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers"
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

// ModelsJSON lists Model as the only available model.
const ModelsJSON = `{"object":"list","data":[{"id":"` + Model + `","object":"model","owned_by":"test"}]}`

// EmbeddingsJSON is a one-vector embeddings reply.
const EmbeddingsJSON = `{"object":"list","data":[{"object":"embedding","index":0,"embedding":[0.1,0.2]}],"model":"` + Model + `","usage":{"prompt_tokens":2,"total_tokens":2}}`

// Model and Reply are the model ID and assistant text used by the fixtures.
const (
	Model = "test-model"
	Reply = "hello"
)

// ChatCompatible describes a provider built on the shared OpenAI-compatible
// adapter so AssertChatCompatible can check the contract every such provider
// shares.
type ChatCompatible struct {
	// Registration is the provider's factory registration.
	Registration providers.Registration
	// Type is the expected Registration.Type.
	Type string
	// DefaultBaseURL is the expected Registration.Discovery.DefaultBaseURL.
	DefaultBaseURL string
	// New is the provider's NewWithHTTPClient constructor.
	New func(apiKey, baseURL string, client *http.Client, hooks llmclient.Hooks) core.Provider
	// Embeddings reports whether the provider forwards embeddings upstream.
	// When false the helper asserts a typed invalid-request error and no
	// upstream call.
	Embeddings bool
	// AuthHeader and AuthPrefix describe how the API key is sent. They
	// default to Authorization and "Bearer ".
	AuthHeader string
	AuthPrefix string
}

// AssertChatCompatible checks the contract shared by providers that embed
// the OpenAI-compatible adapter: registration metadata, constructor safety,
// and that chat, streaming, model listing, Responses translation, and
// embeddings reach the expected upstream paths with the API key attached.
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
		if p.Registration.Type != p.Type {
			t.Errorf("Registration.Type = %q, want %q", p.Registration.Type, p.Type)
		}
		if p.Registration.New == nil {
			t.Fatal("Registration.New is nil")
		}
		if got := p.Registration.Discovery.DefaultBaseURL; got != p.DefaultBaseURL {
			t.Errorf("Registration.Discovery.DefaultBaseURL = %q, want %q", got, p.DefaultBaseURL)
		}
		provider := p.Registration.New(providers.ProviderConfig{APIKey: apiKey}, providers.ProviderOptions{})
		if provider == nil {
			t.Fatal("Registration.New returned nil")
		}
	})

	t.Run("constructor tolerates nil client and zero hooks", func(t *testing.T) {
		if provider := p.New(apiKey, "http://example.invalid", nil, llmclient.Hooks{}); provider == nil {
			t.Fatal("NewWithHTTPClient(nil client) returned nil")
		}
	})

	t.Run("chat completion", func(t *testing.T) {
		server, capture := JSONServer(t, http.StatusOK, ChatCompletionJSON)
		provider := p.New(apiKey, server.URL, server.Client(), llmclient.Hooks{})
		resp, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
			Model:    Model,
			Messages: []core.Message{{Role: "user", Content: "hi"}},
		})
		if err != nil {
			t.Fatalf("ChatCompletion() error = %v", err)
		}
		req := capture.Last(t)
		assertUpstream(t, req, http.MethodPost, "/chat/completions", p.AuthHeader, wantAuth)
		if got := req.JSON(t)["model"]; got != Model {
			t.Errorf("request model = %#v, want %q", got, Model)
		}
		if resp.Model != Model || len(resp.Choices) != 1 || resp.Choices[0].Message.Content != Reply {
			t.Errorf("unexpected response: %+v", resp)
		}
	})

	t.Run("stream chat completion", func(t *testing.T) {
		server, capture := SSEServer(t, ChatChunkSSE)
		provider := p.New(apiKey, server.URL, server.Client(), llmclient.Hooks{})
		stream, err := provider.StreamChatCompletion(context.Background(), &core.ChatRequest{
			Model:    Model,
			Messages: []core.Message{{Role: "user", Content: "hi"}},
		})
		if err != nil {
			t.Fatalf("StreamChatCompletion() error = %v", err)
		}
		defer stream.Close()
		body, err := io.ReadAll(stream)
		if err != nil {
			t.Fatalf("ReadAll() error = %v", err)
		}
		req := capture.Last(t)
		assertUpstream(t, req, http.MethodPost, "/chat/completions", p.AuthHeader, wantAuth)
		if sent := req.JSON(t); sent["model"] != Model || sent["stream"] != true {
			t.Errorf("stream request body = %#v, want model %q and stream=true", sent, Model)
		}
		if !strings.Contains(string(body), "data: [DONE]") {
			t.Errorf("stream body = %q, want SSE terminator", body)
		}
	})

	t.Run("list models", func(t *testing.T) {
		server, capture := JSONServer(t, http.StatusOK, ModelsJSON)
		provider := p.New(apiKey, server.URL, server.Client(), llmclient.Hooks{})
		resp, err := provider.ListModels(context.Background())
		if err != nil {
			t.Fatalf("ListModels() error = %v", err)
		}
		assertUpstream(t, capture.Last(t), http.MethodGet, "/models", p.AuthHeader, wantAuth)
		if len(resp.Data) != 1 || resp.Data[0].ID != Model {
			t.Errorf("models = %+v, want one model %q", resp.Data, Model)
		}
	})

	t.Run("responses translate to chat completions", func(t *testing.T) {
		server, capture := JSONServer(t, http.StatusOK, ChatCompletionJSON)
		provider := p.New(apiKey, server.URL, server.Client(), llmclient.Hooks{})
		resp, err := provider.Responses(context.Background(), &core.ResponsesRequest{Model: Model, Input: "hi"})
		if err != nil {
			t.Fatalf("Responses() error = %v", err)
		}
		req := capture.Last(t)
		assertUpstream(t, req, http.MethodPost, "/chat/completions", p.AuthHeader, wantAuth)
		if got := req.JSON(t)["model"]; got != Model {
			t.Errorf("request model = %#v, want %q", got, Model)
		}
		if resp.Object != "response" || resp.Status != "completed" {
			t.Errorf("response object/status = %q/%q, want response/completed", resp.Object, resp.Status)
		}
	})

	t.Run("embeddings", func(t *testing.T) {
		server, capture := JSONServer(t, http.StatusOK, EmbeddingsJSON)
		provider := p.New(apiKey, server.URL, server.Client(), llmclient.Hooks{})
		resp, err := provider.Embeddings(context.Background(), &core.EmbeddingRequest{Model: Model, Input: "hi"})
		if !p.Embeddings {
			AssertUnsupported(t, err)
			if capture.Count() != 0 {
				t.Errorf("upstream received %d requests, want 0 (embeddings must not be forwarded)", capture.Count())
			}
			return
		}
		if err != nil {
			t.Fatalf("Embeddings() error = %v", err)
		}
		assertUpstream(t, capture.Last(t), http.MethodPost, "/embeddings", p.AuthHeader, wantAuth)
		if len(resp.Data) != 1 {
			t.Errorf("embeddings = %+v, want one vector", resp.Data)
		}
	})
}

// AssertUnsupported checks that err is the typed invalid-request error a
// provider returns for a surface it does not offer.
func AssertUnsupported(t testing.TB, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("error = nil, want typed unsupported error")
	}
	var gwErr *core.GatewayError
	if !errors.As(err, &gwErr) {
		t.Fatalf("error type = %T, want *core.GatewayError", err)
	}
	if gwErr.Type != core.ErrorTypeInvalidRequest {
		t.Errorf("error Type = %v, want %v", gwErr.Type, core.ErrorTypeInvalidRequest)
	}
	if gwErr.StatusCode != http.StatusBadRequest {
		t.Errorf("error StatusCode = %d, want %d", gwErr.StatusCode, http.StatusBadRequest)
	}
}

// AssertNoNativeSurfaces checks that provider does not advertise the optional
// native batch, file, or audio interfaces, so a provider built on the shared
// adapter cannot accidentally claim capabilities its upstream lacks.
func AssertNoNativeSurfaces(t testing.TB, provider any) {
	t.Helper()
	if _, ok := provider.(core.NativeBatchProvider); ok {
		t.Error("provider should not implement core.NativeBatchProvider")
	}
	if _, ok := provider.(core.NativeFileProvider); ok {
		t.Error("provider should not implement core.NativeFileProvider")
	}
	if _, ok := provider.(core.AudioProvider); ok {
		t.Error("provider should not implement core.AudioProvider")
	}
}

func assertUpstream(t testing.TB, req Recorded, method, path, authHeader, wantAuth string) {
	t.Helper()
	if req.Method != method {
		t.Errorf("upstream method = %s, want %s", req.Method, method)
	}
	if req.Path != path {
		t.Errorf("upstream path = %q, want %q", req.Path, path)
	}
	if got := req.Header.Get(authHeader); got != wantAuth {
		t.Errorf("upstream %s header = %q, want %q", authHeader, got, wantAuth)
	}
}
