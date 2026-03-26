package server

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"

	"gomodel/internal/core"
)

type fallbackResolverStub struct {
	selectors []core.ModelSelector
}

func (s fallbackResolverStub) ResolveFallbacks(_ *core.RequestModelResolution, _ core.Operation) []core.ModelSelector {
	return append([]core.ModelSelector(nil), s.selectors...)
}

type fallbackProvider struct {
	chatResponses      map[string]*core.ChatResponse
	chatErrors         map[string]error
	responsesResponses map[string]*core.ResponsesResponse
	responsesErrors    map[string]error
	embeddingResponses map[string]*core.EmbeddingResponse
	embeddingErrors    map[string]error
	supportedModels    map[string]string
	chatCalls          []string
	responsesCalls     []string
	embeddingCalls     []string
}

func (p *fallbackProvider) ChatCompletion(_ context.Context, req *core.ChatRequest) (*core.ChatResponse, error) {
	key := requestSelector(req.Model, req.Provider)
	p.chatCalls = append(p.chatCalls, key)
	if err := p.chatErrors[key]; err != nil {
		return nil, err
	}
	return p.chatResponses[key], nil
}

func (p *fallbackProvider) StreamChatCompletion(_ context.Context, req *core.ChatRequest) (io.ReadCloser, error) {
	key := requestSelector(req.Model, req.Provider)
	p.chatCalls = append(p.chatCalls, key)
	if err := p.chatErrors[key]; err != nil {
		return nil, err
	}
	return io.NopCloser(strings.NewReader("data: [DONE]\n\n")), nil
}

func (p *fallbackProvider) ListModels(_ context.Context) (*core.ModelsResponse, error) {
	return &core.ModelsResponse{Object: "list"}, nil
}

func (p *fallbackProvider) Responses(_ context.Context, req *core.ResponsesRequest) (*core.ResponsesResponse, error) {
	key := requestSelector(req.Model, req.Provider)
	p.responsesCalls = append(p.responsesCalls, key)
	if err := p.responsesErrors[key]; err != nil {
		return nil, err
	}
	return p.responsesResponses[key], nil
}

func (p *fallbackProvider) StreamResponses(_ context.Context, req *core.ResponsesRequest) (io.ReadCloser, error) {
	key := requestSelector(req.Model, req.Provider)
	p.responsesCalls = append(p.responsesCalls, key)
	if err := p.responsesErrors[key]; err != nil {
		return nil, err
	}
	return io.NopCloser(strings.NewReader("data: [DONE]\n\n")), nil
}

func (p *fallbackProvider) Embeddings(_ context.Context, req *core.EmbeddingRequest) (*core.EmbeddingResponse, error) {
	key := requestSelector(req.Model, req.Provider)
	p.embeddingCalls = append(p.embeddingCalls, key)
	if err := p.embeddingErrors[key]; err != nil {
		return nil, err
	}
	return p.embeddingResponses[key], nil
}

func (p *fallbackProvider) Supports(model string) bool {
	selector, err := core.ParseModelSelector(model, "")
	if err == nil {
		model = selector.QualifiedModel()
	}
	_, ok := p.supportedModels[model]
	return ok
}

func (p *fallbackProvider) GetProviderType(model string) string {
	selector, err := core.ParseModelSelector(model, "")
	if err == nil {
		model = selector.QualifiedModel()
	}
	return p.supportedModels[model]
}

func TestChatCompletion_FallsBackToAlternateModel(t *testing.T) {
	provider := &fallbackProvider{
		chatResponses: map[string]*core.ChatResponse{
			"azure/gpt-4o": {
				ID:       "chatcmpl-fallback",
				Object:   "chat.completion",
				Model:    "gpt-4o",
				Provider: "azure",
				Choices: []core.Choice{{
					Index:        0,
					Message:      core.ResponseMessage{Role: "assistant", Content: "fallback ok"},
					FinishReason: "stop",
				}},
			},
		},
		chatErrors: map[string]error{
			"gpt-4o": core.NewProviderError("openai", http.StatusServiceUnavailable, "model temporarily unavailable", nil),
		},
		supportedModels: map[string]string{
			"gpt-4o":       "openai",
			"azure/gpt-4o": "azure",
		},
	}

	handler := newHandler(provider, nil, nil, nil, nil, fallbackResolverStub{
		selectors: []core.ModelSelector{{Provider: "azure", Model: "gpt-4o"}},
	}, nil)

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	if err := handler.ChatCompletion(c); err != nil {
		t.Fatalf("handler.ChatCompletion() error = %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if len(provider.chatCalls) != 2 {
		t.Fatalf("chat calls = %v, want 2 attempts", provider.chatCalls)
	}
	if provider.chatCalls[0] != "gpt-4o" || provider.chatCalls[1] != "azure/gpt-4o" {
		t.Fatalf("chat calls = %v, want [gpt-4o azure/gpt-4o]", provider.chatCalls)
	}
	if !strings.Contains(rec.Body.String(), "fallback ok") {
		t.Fatalf("response body = %s, want fallback response", rec.Body.String())
	}
	if !core.GetFallbackUsed(c.Request().Context()) {
		t.Fatal("expected request context to be marked as fallback-used")
	}
}

func TestChatCompletion_DoesNotFallbackOnNonAvailabilityError(t *testing.T) {
	provider := &fallbackProvider{
		chatErrors: map[string]error{
			"gpt-4o": core.NewInvalidRequestError("temperature must be between 0 and 2", nil),
		},
		supportedModels: map[string]string{
			"gpt-4o":       "openai",
			"azure/gpt-4o": "azure",
		},
	}

	handler := newHandler(provider, nil, nil, nil, nil, fallbackResolverStub{
		selectors: []core.ModelSelector{{Provider: "azure", Model: "gpt-4o"}},
	}, nil)

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	if err := handler.ChatCompletion(c); err != nil {
		t.Fatalf("handler.ChatCompletion() error = %v", err)
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if len(provider.chatCalls) != 1 || provider.chatCalls[0] != "gpt-4o" {
		t.Fatalf("chat calls = %v, want only the primary model", provider.chatCalls)
	}
}

func TestResponses_FallsBackToAlternateModel(t *testing.T) {
	provider := &fallbackProvider{
		responsesResponses: map[string]*core.ResponsesResponse{
			"azure/gpt-4o": {
				ID:       "resp-fallback",
				Object:   "response",
				Model:    "gpt-4o",
				Provider: "azure",
				Status:   "completed",
				Output: []core.ResponsesOutputItem{{
					ID:     "out-1",
					Type:   "message",
					Role:   "assistant",
					Status: "completed",
					Content: []core.ResponsesContentItem{{
						Type: "output_text",
						Text: "fallback response",
					}},
				}},
			},
		},
		responsesErrors: map[string]error{
			"gpt-4o": core.NewNotFoundError("model not found"),
		},
		supportedModels: map[string]string{
			"gpt-4o":       "openai",
			"azure/gpt-4o": "azure",
		},
	}

	handler := newHandler(provider, nil, nil, nil, nil, fallbackResolverStub{
		selectors: []core.ModelSelector{{Provider: "azure", Model: "gpt-4o"}},
	}, nil)

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-4o","input":"hi"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	if err := handler.Responses(c); err != nil {
		t.Fatalf("handler.Responses() error = %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if len(provider.responsesCalls) != 2 {
		t.Fatalf("responses calls = %v, want 2 attempts", provider.responsesCalls)
	}
}

func TestEmbeddings_DoesNotFallback(t *testing.T) {
	provider := &fallbackProvider{
		embeddingResponses: map[string]*core.EmbeddingResponse{
			"azure/text-embedding-3-small": {
				Object:   "list",
				Model:    "text-embedding-3-small",
				Provider: "azure",
				Data: []core.EmbeddingData{{
					Object:    "embedding",
					Embedding: []byte(`[0.1,0.2]`),
					Index:     0,
				}},
			},
		},
		embeddingErrors: map[string]error{
			"text-embedding-3-small": core.NewProviderError("openai", http.StatusServiceUnavailable, "model temporarily unavailable", nil),
		},
		supportedModels: map[string]string{
			"text-embedding-3-small":       "openai",
			"azure/text-embedding-3-small": "azure",
		},
	}

	handler := newHandler(provider, nil, nil, nil, nil, fallbackResolverStub{
		selectors: []core.ModelSelector{{Provider: "azure", Model: "text-embedding-3-small"}},
	}, nil)

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/v1/embeddings", strings.NewReader(`{"model":"text-embedding-3-small","input":"hi"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	if err := handler.Embeddings(c); err != nil {
		t.Fatalf("handler.Embeddings() error = %v", err)
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusServiceUnavailable, rec.Body.String())
	}
	if len(provider.embeddingCalls) != 1 || provider.embeddingCalls[0] != "text-embedding-3-small" {
		t.Fatalf("embedding calls = %v, want only the primary model", provider.embeddingCalls)
	}
}

func requestSelector(model, provider string) string {
	selector, err := core.ParseModelSelector(model, provider)
	if err != nil {
		return strings.TrimSpace(model)
	}
	return selector.QualifiedModel()
}
