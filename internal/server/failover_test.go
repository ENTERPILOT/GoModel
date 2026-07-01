package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"

	"gomodel/internal/auditlog"
	"gomodel/internal/core"
)

type failoverResolverStub struct {
	selectors []core.ModelSelector
}

func (s failoverResolverStub) ResolveFailovers(_ *core.RequestModelResolution, _ core.Operation) []core.ModelSelector {
	return append([]core.ModelSelector(nil), s.selectors...)
}

type failoverProvider struct {
	chatResponses      map[string]*core.ChatResponse
	chatStreams        map[string]string
	chatErrors         map[string]error
	responsesResponses map[string]*core.ResponsesResponse
	responsesStreams   map[string]string
	responsesErrors    map[string]error
	embeddingResponses map[string]*core.EmbeddingResponse
	embeddingErrors    map[string]error
	supportedModels    map[string]string
	chatCalls          []string
	responsesCalls     []string
	embeddingCalls     []string
}

func (p *failoverProvider) ChatCompletion(_ context.Context, req *core.ChatRequest) (*core.ChatResponse, error) {
	key := requestSelector(req.Model, req.Provider)
	p.chatCalls = append(p.chatCalls, key)
	if err := p.chatErrors[key]; err != nil {
		return nil, err
	}
	return p.chatResponses[key], nil
}

func (p *failoverProvider) StreamChatCompletion(_ context.Context, req *core.ChatRequest) (io.ReadCloser, error) {
	key := requestSelector(req.Model, req.Provider)
	p.chatCalls = append(p.chatCalls, key)
	if err := p.chatErrors[key]; err != nil {
		return nil, err
	}
	if stream := p.chatStreams[key]; stream != "" {
		return io.NopCloser(strings.NewReader(stream)), nil
	}
	return io.NopCloser(strings.NewReader("data: [DONE]\n\n")), nil
}

func (p *failoverProvider) ListModels(_ context.Context) (*core.ModelsResponse, error) {
	return &core.ModelsResponse{Object: "list"}, nil
}

func (p *failoverProvider) Responses(_ context.Context, req *core.ResponsesRequest) (*core.ResponsesResponse, error) {
	key := requestSelector(req.Model, req.Provider)
	p.responsesCalls = append(p.responsesCalls, key)
	if err := p.responsesErrors[key]; err != nil {
		return nil, err
	}
	return p.responsesResponses[key], nil
}

func (p *failoverProvider) StreamResponses(_ context.Context, req *core.ResponsesRequest) (io.ReadCloser, error) {
	key := requestSelector(req.Model, req.Provider)
	p.responsesCalls = append(p.responsesCalls, key)
	if err := p.responsesErrors[key]; err != nil {
		return nil, err
	}
	if stream := p.responsesStreams[key]; stream != "" {
		return io.NopCloser(strings.NewReader(stream)), nil
	}
	return io.NopCloser(strings.NewReader("data: [DONE]\n\n")), nil
}

func (p *failoverProvider) Embeddings(_ context.Context, req *core.EmbeddingRequest) (*core.EmbeddingResponse, error) {
	key := requestSelector(req.Model, req.Provider)
	p.embeddingCalls = append(p.embeddingCalls, key)
	if err := p.embeddingErrors[key]; err != nil {
		return nil, err
	}
	return p.embeddingResponses[key], nil
}

func (p *failoverProvider) Supports(model string) bool {
	selector, err := core.ParseModelSelector(model, "")
	if err == nil {
		model = selector.QualifiedModel()
	}
	_, ok := p.supportedModels[model]
	return ok
}

func (p *failoverProvider) GetProviderType(model string) string {
	selector, err := core.ParseModelSelector(model, "")
	if err == nil {
		model = selector.QualifiedModel()
	}
	return p.supportedModels[model]
}

func TestChatCompletion_FallsBackToAlternateModel(t *testing.T) {
	provider := &failoverProvider{
		chatResponses: map[string]*core.ChatResponse{
			"azure/gpt-4o": {
				ID:       "chatcmpl-failover",
				Object:   "chat.completion",
				Model:    "gpt-4o",
				Provider: "azure",
				Choices: []core.Choice{{
					Index:        0,
					Message:      core.ResponseMessage{Role: "assistant", Content: "failover ok"},
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

	handler := newHandler(provider, nil, nil, nil, nil, nil, failoverResolverStub{
		selectors: []core.ModelSelector{{Provider: "azure", Model: "gpt-4o"}},
	}, nil)

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	entry := &auditlog.LogEntry{Data: &auditlog.LogData{}}
	c.Set(string(auditlog.LogEntryKey), entry)

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
	if !strings.Contains(rec.Body.String(), "failover ok") {
		t.Fatalf("response body = %s, want failover response", rec.Body.String())
	}
	if !core.GetFailoverUsed(c.Request().Context()) {
		t.Fatal("expected request context to be marked as failover-used")
	}
	if entry.Data == nil || entry.Data.Failover == nil {
		t.Fatal("expected audit entry to capture failover details")
	}
	if got := entry.Data.Failover.TargetModel; got != "azure/gpt-4o" {
		t.Fatalf("failover target = %q, want %q", got, "azure/gpt-4o")
	}
	if got := len(entry.Data.Attempts); got != 2 {
		t.Fatalf("audit attempts = %d, want 2: %#v", got, entry.Data.Attempts)
	}
	if entry.Data.Attempts[0].Kind != auditlog.AttemptKindPrimary || entry.Data.Attempts[0].Success {
		t.Fatalf("primary audit attempt = %#v, want failed primary", entry.Data.Attempts[0])
	}
	if entry.Data.Attempts[0].StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("primary attempt status = %d, want %d", entry.Data.Attempts[0].StatusCode, http.StatusServiceUnavailable)
	}
	if entry.Data.Attempts[1].Kind != auditlog.AttemptKindFailover || !entry.Data.Attempts[1].Success {
		t.Fatalf("failover audit attempt = %#v, want successful failover", entry.Data.Attempts[1])
	}
	if entry.Data.Attempts[1].Model != "azure/gpt-4o" {
		t.Fatalf("failover attempt model = %q, want azure/gpt-4o", entry.Data.Attempts[1].Model)
	}
}

func TestChatCompletion_DoesNotFailoverOnNonAvailabilityError(t *testing.T) {
	provider := &failoverProvider{
		chatErrors: map[string]error{
			"gpt-4o": core.NewInvalidRequestError("temperature must be between 0 and 2", nil),
		},
		supportedModels: map[string]string{
			"gpt-4o":       "openai",
			"azure/gpt-4o": "azure",
		},
	}

	handler := newHandler(provider, nil, nil, nil, nil, nil, failoverResolverStub{
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

func TestChatCompletion_DoesNotFailoverWhenWorkflowPolicyDisablesFailover(t *testing.T) {
	provider := &failoverProvider{
		chatResponses: map[string]*core.ChatResponse{
			"azure/gpt-4o": {
				ID:       "chatcmpl-failover",
				Object:   "chat.completion",
				Model:    "gpt-4o",
				Provider: "azure",
				Choices: []core.Choice{{
					Index:        0,
					Message:      core.ResponseMessage{Role: "assistant", Content: "failover ok"},
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

	handler := newHandler(provider, nil, nil, nil, nil, requestWorkflowPolicyResolverFunc(func(core.WorkflowSelector) (*core.ResolvedWorkflowPolicy, error) {
		return &core.ResolvedWorkflowPolicy{
			VersionID: "workflow-failover-off",
			Features: core.WorkflowFeatures{
				Cache:      true,
				Audit:      true,
				Usage:      true,
				Guardrails: true,
				Failover:   false,
			},
		}, nil
	}), failoverResolverStub{
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
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusServiceUnavailable, rec.Body.String())
	}
	if len(provider.chatCalls) != 1 || provider.chatCalls[0] != "gpt-4o" {
		t.Fatalf("chat calls = %v, want only the primary model", provider.chatCalls)
	}
}

func TestChatCompletion_StreamFallsBackToAlternateModel(t *testing.T) {
	provider := &failoverProvider{
		chatStreams: map[string]string{
			"azure/gpt-4o": "data: {\"choices\":[{\"delta\":{\"content\":\"failover ok\"}}]}\n\ndata: [DONE]\n\n",
		},
		chatErrors: map[string]error{
			"gpt-4o": core.NewProviderError("openai", http.StatusServiceUnavailable, "model temporarily unavailable", nil),
		},
		supportedModels: map[string]string{
			"gpt-4o":       "openai",
			"azure/gpt-4o": "azure",
		},
	}

	handler := newHandler(provider, nil, nil, nil, nil, nil, failoverResolverStub{
		selectors: []core.ModelSelector{{Provider: "azure", Model: "gpt-4o"}},
	}, nil)

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-4o","stream":true,"messages":[{"role":"user","content":"hi"}]}`))
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
	if !strings.Contains(rec.Body.String(), "failover ok") {
		t.Fatalf("response body = %s, want failover stream content", rec.Body.String())
	}
	if !core.GetFailoverUsed(c.Request().Context()) {
		t.Fatal("expected request context to be marked as failover-used")
	}
}

func TestChatCompletion_StreamDoesNotFailoverWhenWorkflowPolicyDisablesFailover(t *testing.T) {
	provider := &failoverProvider{
		chatStreams: map[string]string{
			"azure/gpt-4o": "data: {\"choices\":[{\"delta\":{\"content\":\"failover ok\"}}]}\n\ndata: [DONE]\n\n",
		},
		chatErrors: map[string]error{
			"gpt-4o": core.NewProviderError("openai", http.StatusServiceUnavailable, "model temporarily unavailable", nil),
		},
		supportedModels: map[string]string{
			"gpt-4o":       "openai",
			"azure/gpt-4o": "azure",
		},
	}

	handler := newHandler(provider, nil, nil, nil, nil, requestWorkflowPolicyResolverFunc(func(core.WorkflowSelector) (*core.ResolvedWorkflowPolicy, error) {
		return &core.ResolvedWorkflowPolicy{
			VersionID: "workflow-failover-off",
			Features: core.WorkflowFeatures{
				Cache:      true,
				Audit:      true,
				Usage:      true,
				Guardrails: true,
				Failover:   false,
			},
		}, nil
	}), failoverResolverStub{
		selectors: []core.ModelSelector{{Provider: "azure", Model: "gpt-4o"}},
	}, nil)

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-4o","stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	if err := handler.ChatCompletion(c); err != nil {
		t.Fatalf("handler.ChatCompletion() error = %v", err)
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusServiceUnavailable, rec.Body.String())
	}
	if len(provider.chatCalls) != 1 || provider.chatCalls[0] != "gpt-4o" {
		t.Fatalf("chat calls = %v, want only the primary model", provider.chatCalls)
	}
}

func TestResponses_FallsBackToAlternateModel(t *testing.T) {
	provider := &failoverProvider{
		responsesResponses: map[string]*core.ResponsesResponse{
			"azure/gpt-4o": {
				ID:       "resp-failover",
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
						Text: "failover response",
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

	handler := newHandler(provider, nil, nil, nil, nil, nil, failoverResolverStub{
		selectors: []core.ModelSelector{{Provider: "azure", Model: "gpt-4o"}},
	}, nil)

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-4o","input":"hi"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	entry := &auditlog.LogEntry{Data: &auditlog.LogData{}}
	c.Set(string(auditlog.LogEntryKey), entry)

	if err := handler.Responses(c); err != nil {
		t.Fatalf("handler.Responses() error = %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if len(provider.responsesCalls) != 2 {
		t.Fatalf("responses calls = %v, want 2 attempts", provider.responsesCalls)
	}
	if provider.responsesCalls[0] != "gpt-4o" || provider.responsesCalls[1] != "azure/gpt-4o" {
		t.Fatalf("responses calls = %v, want [gpt-4o azure/gpt-4o]", provider.responsesCalls)
	}
	var resp core.ResponsesResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response body is not valid JSON: %v body=%s", err, rec.Body.String())
	}
	if resp.ID != "resp-failover" || resp.Provider != "azure" || resp.Model != "gpt-4o" || resp.Status != "completed" {
		t.Fatalf("response = %+v, want failover response metadata", resp)
	}
	if len(resp.Output) != 1 || len(resp.Output[0].Content) != 1 || resp.Output[0].Content[0].Text != "failover response" {
		t.Fatalf("response output = %+v, want failover response content", resp.Output)
	}
	if !core.GetFailoverUsed(c.Request().Context()) {
		t.Fatal("expected request context to be marked as failover-used")
	}
	if entry.Data == nil || entry.Data.Failover == nil {
		t.Fatal("expected audit entry to capture streaming failover details")
	}
	if got := entry.Data.Failover.TargetModel; got != "azure/gpt-4o" {
		t.Fatalf("streaming failover target = %q, want %q", got, "azure/gpt-4o")
	}
}

func TestResponses_StreamFallsBackToAlternateModel(t *testing.T) {
	provider := &failoverProvider{
		responsesStreams: map[string]string{
			"azure/gpt-4o": "data: {\"type\":\"response.output_text.delta\",\"delta\":\"failover response\"}\n\ndata: [DONE]\n\n",
		},
		responsesErrors: map[string]error{
			"gpt-4o": core.NewNotFoundError("model not found"),
		},
		supportedModels: map[string]string{
			"gpt-4o":       "openai",
			"azure/gpt-4o": "azure",
		},
	}

	handler := newHandler(provider, nil, nil, nil, nil, nil, failoverResolverStub{
		selectors: []core.ModelSelector{{Provider: "azure", Model: "gpt-4o"}},
	}, nil)

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-4o","stream":true,"input":"hi"}`))
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
	if provider.responsesCalls[0] != "gpt-4o" || provider.responsesCalls[1] != "azure/gpt-4o" {
		t.Fatalf("responses calls = %v, want [gpt-4o azure/gpt-4o]", provider.responsesCalls)
	}
	if !strings.Contains(rec.Body.String(), "failover response") {
		t.Fatalf("response body = %s, want failover stream content", rec.Body.String())
	}
	if !core.GetFailoverUsed(c.Request().Context()) {
		t.Fatal("expected request context to be marked as failover-used")
	}
}

func TestResponses_StreamDoesNotFailoverOnNonAvailabilityError(t *testing.T) {
	provider := &failoverProvider{
		responsesStreams: map[string]string{
			"azure/gpt-4o": "data: {\"type\":\"response.output_text.delta\",\"delta\":\"failover response\"}\n\ndata: [DONE]\n\n",
		},
		responsesErrors: map[string]error{
			"gpt-4o": core.NewInvalidRequestError("temperature must be between 0 and 2", nil),
		},
		supportedModels: map[string]string{
			"gpt-4o":       "openai",
			"azure/gpt-4o": "azure",
		},
	}

	handler := newHandler(provider, nil, nil, nil, nil, nil, failoverResolverStub{
		selectors: []core.ModelSelector{{Provider: "azure", Model: "gpt-4o"}},
	}, nil)

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-4o","stream":true,"input":"hi"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	if err := handler.Responses(c); err != nil {
		t.Fatalf("handler.Responses() error = %v", err)
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if len(provider.responsesCalls) != 1 || provider.responsesCalls[0] != "gpt-4o" {
		t.Fatalf("responses calls = %v, want only the primary model", provider.responsesCalls)
	}
	if core.GetFailoverUsed(c.Request().Context()) {
		t.Fatal("expected request context to remain unmarked for failover")
	}
}

func TestResponses_StreamDoesNotFailoverWhenWorkflowPolicyDisablesFailover(t *testing.T) {
	provider := &failoverProvider{
		responsesStreams: map[string]string{
			"azure/gpt-4o": "data: {\"type\":\"response.output_text.delta\",\"delta\":\"failover response\"}\n\ndata: [DONE]\n\n",
		},
		responsesErrors: map[string]error{
			"gpt-4o": core.NewProviderError("openai", http.StatusServiceUnavailable, "model temporarily unavailable", nil),
		},
		supportedModels: map[string]string{
			"gpt-4o":       "openai",
			"azure/gpt-4o": "azure",
		},
	}

	handler := newHandler(provider, nil, nil, nil, nil, requestWorkflowPolicyResolverFunc(func(core.WorkflowSelector) (*core.ResolvedWorkflowPolicy, error) {
		return &core.ResolvedWorkflowPolicy{
			VersionID: "workflow-failover-off",
			Features: core.WorkflowFeatures{
				Cache:      true,
				Audit:      true,
				Usage:      true,
				Guardrails: true,
				Failover:   false,
			},
		}, nil
	}), failoverResolverStub{
		selectors: []core.ModelSelector{{Provider: "azure", Model: "gpt-4o"}},
	}, nil)

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-4o","stream":true,"input":"hi"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	if err := handler.Responses(c); err != nil {
		t.Fatalf("handler.Responses() error = %v", err)
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusServiceUnavailable, rec.Body.String())
	}
	if len(provider.responsesCalls) != 1 || provider.responsesCalls[0] != "gpt-4o" {
		t.Fatalf("responses calls = %v, want only the primary model", provider.responsesCalls)
	}
	if core.GetFailoverUsed(c.Request().Context()) {
		t.Fatal("expected request context to remain unmarked for failover")
	}
}

func TestChatCompletion_DoesNotFailoverOnNonModelNotFound(t *testing.T) {
	provider := &failoverProvider{
		chatErrors: map[string]error{
			"gpt-4o": core.NewProviderError("openai", http.StatusNotFound, "endpoint not found", nil),
		},
		supportedModels: map[string]string{
			"gpt-4o":       "openai",
			"azure/gpt-4o": "azure",
		},
	}

	handler := newHandler(provider, nil, nil, nil, nil, nil, failoverResolverStub{
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
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusNotFound, rec.Body.String())
	}
	if len(provider.chatCalls) != 1 || provider.chatCalls[0] != "gpt-4o" {
		t.Fatalf("chat calls = %v, want only the primary model", provider.chatCalls)
	}
}
func TestEmbeddings_DoesNotFailover(t *testing.T) {
	provider := &failoverProvider{
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

	handler := newHandler(provider, nil, nil, nil, nil, nil, failoverResolverStub{
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
