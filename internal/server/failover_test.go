package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/auditlog"
	"github.com/enterpilot/gomodel/internal/budget"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/echotest"
	"github.com/enterpilot/gomodel/internal/gateway"
	"github.com/enterpilot/gomodel/internal/ratelimit"
	"github.com/enterpilot/gomodel/internal/usage"
	"github.com/enterpilot/gomodel/internal/virtualmodels"
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
	embeddingCall      func(context.Context, *core.EmbeddingRequest) (*core.EmbeddingResponse, error)
	embeddingMu        sync.Mutex
	supportedModels    map[string]string
	providerNames      map[string]string
	chatCalls          []string
	responsesCalls     []string
	embeddingCalls     []string
	embeddingRequests  []*core.EmbeddingRequest
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

func (p *failoverProvider) Embeddings(ctx context.Context, req *core.EmbeddingRequest) (*core.EmbeddingResponse, error) {
	key := requestSelector(req.Model, req.Provider)
	p.embeddingMu.Lock()
	p.embeddingCalls = append(p.embeddingCalls, key)
	p.embeddingRequests = append(p.embeddingRequests, req)
	call := p.embeddingCall
	err := p.embeddingErrors[key]
	response := p.embeddingResponses[key]
	p.embeddingMu.Unlock()
	if call != nil {
		return call(ctx, req)
	}
	if err != nil {
		return nil, err
	}
	return response, nil
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

func (p *failoverProvider) GetProviderName(model string) string {
	selector, err := core.ParseModelSelector(model, "")
	if err != nil {
		return ""
	}
	if name := p.providerNames[selector.QualifiedModel()]; name != "" {
		return name
	}
	return selector.Provider
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

	c, rec := echotest.Post(t, "/v1/chat/completions", `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`)
	entry := &auditlog.LogEntry{Data: &auditlog.LogData{}}
	c.Set(string(auditlog.LogEntryKey), entry)
	err := handler.ChatCompletion(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, []string{"gpt-4o", "azure/gpt-4o"}, provider.chatCalls)
	require.Contains(t, rec.Body.String(), "failover ok")
	require.True(t, core.GetFailoverUsed(c.Request().Context()))
	require.NotNil(t, entry.Data)
	require.NotNil(t, entry.Data.Failover)
	require.Equal(t, "azure/gpt-4o", entry.Data.Failover.TargetModel)

	require.Len(t, entry.Data.Attempts, 2)
	require.Equal(t, auditlog.AttemptKindPrimary, entry.Data.Attempts[0].Kind)
	require.False(t, entry.Data.Attempts[0].Success, "primary attempt must be recorded as failed")
	require.Equal(t, http.StatusServiceUnavailable, entry.Data.Attempts[0].StatusCode)
	require.Equal(t, auditlog.AttemptKindFailover, entry.Data.Attempts[1].Kind)
	require.True(t, entry.Data.Attempts[1].Success, "failover attempt must be recorded as successful")
	require.Equal(t, "azure/gpt-4o", entry.Data.Attempts[1].Model)
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

	c, rec := echotest.Post(t, "/v1/chat/completions", `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`)
	err := handler.ChatCompletion(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	require.Equal(t, []string{"gpt-4o"}, provider.chatCalls)
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

	c, rec := echotest.Post(t, "/v1/chat/completions", `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`)
	err := handler.ChatCompletion(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusServiceUnavailable, rec.Code, rec.Body.String())
	require.Equal(t, []string{"gpt-4o"}, provider.chatCalls)
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

	c, rec := echotest.Post(t, "/v1/chat/completions", `{"model":"gpt-4o","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	err := handler.ChatCompletion(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, []string{"gpt-4o", "azure/gpt-4o"}, provider.chatCalls)
	require.Contains(t, rec.Body.String(), "failover ok")
	require.True(t, core.GetFailoverUsed(c.Request().Context()))
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

	c, rec := echotest.Post(t, "/v1/chat/completions", `{"model":"gpt-4o","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	err := handler.ChatCompletion(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusServiceUnavailable, rec.Code, rec.Body.String())
	require.Equal(t, []string{"gpt-4o"}, provider.chatCalls)
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

	c, rec := echotest.Post(t, "/v1/responses", `{"model":"gpt-4o","input":"hi"}`)
	entry := &auditlog.LogEntry{Data: &auditlog.LogData{}}
	c.Set(string(auditlog.LogEntryKey), entry)
	err := handler.Responses(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, []string{"gpt-4o", "azure/gpt-4o"}, provider.responsesCalls)

	resp := echotest.Decode[core.ResponsesResponse](t, rec)
	require.Equal(t, "resp-failover", resp.ID)
	require.Equal(t, "azure", resp.Provider)
	require.Equal(t, "gpt-4o", resp.Model)
	require.Equal(t, "completed", resp.Status)
	require.Len(t, resp.Output, 1)
	require.Len(t, resp.Output[0].Content, 1)
	require.Equal(t, "failover response", resp.Output[0].Content[0].Text)
	require.True(t, core.GetFailoverUsed(c.Request().Context()))
	require.NotNil(t, entry.Data)
	require.NotNil(t, entry.Data.Failover)
	require.Equal(t, "azure/gpt-4o", entry.Data.Failover.TargetModel)
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

	c, rec := echotest.Post(t, "/v1/responses", `{"model":"gpt-4o","stream":true,"input":"hi"}`)
	err := handler.Responses(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, []string{"gpt-4o", "azure/gpt-4o"}, provider.responsesCalls)
	require.Contains(t, rec.Body.String(), "failover response")
	require.True(t, core.GetFailoverUsed(c.Request().Context()))
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

	c, rec := echotest.Post(t, "/v1/responses", `{"model":"gpt-4o","stream":true,"input":"hi"}`)
	err := handler.Responses(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	require.Equal(t, []string{"gpt-4o"}, provider.responsesCalls)
	require.False(t, core.GetFailoverUsed(c.Request().Context()))
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

	c, rec := echotest.Post(t, "/v1/responses", `{"model":"gpt-4o","stream":true,"input":"hi"}`)
	err := handler.Responses(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusServiceUnavailable, rec.Code, rec.Body.String())
	require.Equal(t, []string{"gpt-4o"}, provider.responsesCalls)
	require.False(t, core.GetFailoverUsed(c.Request().Context()))
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

	c, rec := echotest.Post(t, "/v1/chat/completions", `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`)
	err := handler.ChatCompletion(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
	require.Equal(t, []string{"gpt-4o"}, provider.chatCalls)
}

func TestEmbeddings_FailoverPolicy(t *testing.T) {
	primary := "text-embedding-3-small"
	azure := "azure/text-embedding-3-small"
	together := "together/text-embedding-3-small"
	response := func(provider, model string) *core.EmbeddingResponse {
		return &core.EmbeddingResponse{
			Object: "list", Model: model, Provider: provider,
			Data: []core.EmbeddingData{{Object: "embedding", Embedding: []byte(`[0.1,0.2]`)}},
		}
	}
	serviceUnavailable := core.NewProviderError("openai", http.StatusServiceUnavailable, "model temporarily unavailable", nil)
	tests := []struct {
		name             string
		primaryResponse  *core.EmbeddingResponse
		primaryError     error
		failoverResponse *core.EmbeddingResponse
		failoverError    error
		disableFailover  bool
		denyFailover     bool
		attemptCap       int
		selectors        []core.ModelSelector
		wantCalls        []string
		wantStatus       int
		wantResponse     string
	}{
		{
			name:             "retryable upstream error",
			primaryError:     serviceUnavailable,
			failoverResponse: response("azure", "text-embedding-3-small"),
			selectors:        []core.ModelSelector{{Provider: "azure", Model: "text-embedding-3-small"}},
			wantCalls:        []string{primary, azure},
			wantStatus:       http.StatusOK,
			wantResponse:     "azure",
		},
		{
			name:            "primary success",
			primaryResponse: response("openai", "text-embedding-3-small"),
			selectors:       []core.ModelSelector{{Provider: "azure", Model: "text-embedding-3-small"}},
			wantCalls:       []string{primary},
			wantStatus:      http.StatusOK,
			wantResponse:    "openai",
		},
		{
			name:             "workflow policy disables failover",
			primaryError:     serviceUnavailable,
			failoverResponse: response("azure", "text-embedding-3-small"),
			disableFailover:  true,
			selectors:        []core.ModelSelector{{Provider: "azure", Model: "text-embedding-3-small"}},
			wantCalls:        []string{primary},
			wantStatus:       http.StatusServiceUnavailable,
		},
		{
			name:         "nonretryable client error",
			primaryError: core.NewInvalidRequestError("invalid dimensions", nil),
			selectors:    []core.ModelSelector{{Provider: "azure", Model: "text-embedding-3-small"}},
			wantCalls:    []string{primary},
			wantStatus:   http.StatusBadRequest,
		},
		{
			name:          "all targets fail",
			primaryError:  serviceUnavailable,
			failoverError: core.NewProviderError("azure", http.StatusBadGateway, "alternate unavailable", nil),
			selectors:     []core.ModelSelector{{Provider: "azure", Model: "text-embedding-3-small"}},
			wantCalls:     []string{primary, azure},
			wantStatus:    http.StatusBadGateway,
		},
		{
			name:             "nil primary response is a retryable technical error",
			failoverResponse: response("azure", "text-embedding-3-small"),
			selectors:        []core.ModelSelector{{Provider: "azure", Model: "text-embedding-3-small"}},
			wantCalls:        []string{primary, azure},
			wantStatus:       http.StatusOK,
			wantResponse:     "azure",
		},
		{
			name:             "disallowed target is skipped",
			primaryError:     serviceUnavailable,
			failoverResponse: response("azure", "text-embedding-3-small"),
			denyFailover:     true,
			selectors:        []core.ModelSelector{{Provider: "azure", Model: "text-embedding-3-small"}},
			wantCalls:        []string{primary},
			wantStatus:       http.StatusServiceUnavailable,
		},
		{
			name:             "attempt cap limits alternate calls",
			primaryError:     serviceUnavailable,
			failoverError:    core.NewProviderError("azure", http.StatusServiceUnavailable, "alternate unavailable", nil),
			failoverResponse: response("together", "text-embedding-3-small"),
			attemptCap:       1,
			selectors: []core.ModelSelector{
				{Provider: "azure", Model: "text-embedding-3-small"},
				{Provider: "together", Model: "text-embedding-3-small"},
			},
			wantCalls:  []string{primary, azure},
			wantStatus: http.StatusServiceUnavailable,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := &failoverProvider{
				embeddingResponses: map[string]*core.EmbeddingResponse{},
				embeddingErrors:    map[string]error{},
				supportedModels: map[string]string{
					primary:  "openai",
					azure:    "azure",
					together: "together",
				},
			}
			if tt.primaryResponse != nil {
				provider.embeddingResponses[primary] = tt.primaryResponse
			}
			if tt.primaryError != nil {
				provider.embeddingErrors[primary] = tt.primaryError
			}
			if tt.failoverResponse != nil {
				for _, selector := range tt.selectors {
					if selector.Provider == "azure" || selector.Provider == "together" {
						provider.embeddingResponses[selector.QualifiedModel()] = tt.failoverResponse
					}
				}
			}
			if tt.failoverError != nil {
				provider.embeddingErrors[azure] = tt.failoverError
			}

			var policyResolver RequestWorkflowPolicyResolver
			if tt.disableFailover {
				policyResolver = requestWorkflowPolicyResolverFunc(func(core.WorkflowSelector) (*core.ResolvedWorkflowPolicy, error) {
					return &core.ResolvedWorkflowPolicy{VersionID: "failover-disabled", Features: core.WorkflowFeatures{Failover: false}}, nil
				})
			}
			var authorizer RequestModelAuthorizer
			if tt.denyFailover {
				authorizer = &recordingModelAuthorizer{allow: func(selector core.ModelSelector) bool { return selector.Provider != "azure" }}
			}
			handler := newHandlerWithAuthorizer(provider, nil, nil, nil, nil, authorizer, policyResolver, failoverResolverStub{selectors: tt.selectors}, nil)
			if tt.attemptCap > 0 {
				handler.failoverPolicy = &gateway.FailoverPolicy{MaxAttempts: tt.attemptCap}
			}

			c, rec := echotest.Post(t, "/v1/embeddings", `{"model":"text-embedding-3-small","input":"hi"}`)
			entry := &auditlog.LogEntry{Data: &auditlog.LogData{}}
			c.Set(string(auditlog.LogEntryKey), entry)
			err := handler.Embeddings(c)
			require.NoError(t, err)
			require.Equal(t, tt.wantStatus, rec.Code, rec.Body.String())
			require.Equal(t, tt.wantCalls, provider.embeddingCalls)
			if tt.wantResponse != "" {
				var resp core.EmbeddingResponse
				require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
				require.Equal(t, tt.wantResponse, resp.Provider)
			}
			if len(tt.wantCalls) > 1 && tt.name == "retryable upstream error" {
				require.True(t, core.GetFailoverUsed(c.Request().Context()))
				require.NotNil(t, entry.Data.Failover)
				require.Equal(t, azure, entry.Data.Failover.TargetModel)
				require.Len(t, entry.Data.Attempts, 2)
			}
		})
	}
}

func TestEmbeddings_FailoverUsesInstanceMetadataAndForwardsRequest(t *testing.T) {
	primary := "primary-openai/text-embedding-3-small"
	backup := "backup-openai/text-embedding-3-small"
	provider := &failoverProvider{
		embeddingResponses: map[string]*core.EmbeddingResponse{
			backup: {Object: "list", Model: "text-embedding-3-small-v2", Provider: "openai"},
		},
		embeddingErrors: map[string]error{
			primary: core.NewProviderError("openai", http.StatusTooManyRequests, "rate limited", nil),
		},
		supportedModels: map[string]string{primary: "openai", backup: "openai"},
		providerNames:   map[string]string{primary: "primary-openai", backup: "backup-openai"},
	}
	logger := &usageCaptureLogger{config: usage.Config{Enabled: true}}
	handler := newHandler(provider, nil, logger, nil, nil, nil, failoverResolverStub{
		selectors: []core.ModelSelector{{Provider: "backup-openai", Model: "text-embedding-3-small"}},
	}, nil)

	c, rec := echotest.Post(t, "/v1/embeddings", `{"model":"text-embedding-3-small","provider":"primary-openai","input":["first","second"],"dimensions":256,"encoding_format":"base64","x_meta":{"source":"test"}}`)
	entry := &auditlog.LogEntry{Data: &auditlog.LogData{}}
	c.Set(string(auditlog.LogEntryKey), entry)
	require.NoError(t, handler.Embeddings(c))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, []string{primary, backup}, provider.embeddingCalls)

	var response core.EmbeddingResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response))
	require.Equal(t, "backup-openai", response.Provider)
	require.Equal(t, "text-embedding-3-small-v2", response.Model)
	require.Len(t, provider.embeddingRequests, 2)
	forwarded := provider.embeddingRequests[1]
	require.Equal(t, []any{"first", "second"}, forwarded.Input)
	require.Equal(t, "base64", forwarded.EncodingFormat)
	require.NotNil(t, forwarded.Dimensions)
	require.Equal(t, 256, *forwarded.Dimensions)
	require.JSONEq(t, `{"source":"test"}`, string(forwarded.ExtraFields.Lookup("x_meta")))

	require.True(t, core.GetFailoverUsed(c.Request().Context()))
	require.NotNil(t, entry.Data.Failover)
	require.Equal(t, backup, entry.Data.Failover.TargetModel)
	require.Len(t, entry.Data.Attempts, 2)
	require.Equal(t, "openai", entry.Data.Attempts[0].ProviderType)
	require.Equal(t, "primary-openai", entry.Data.Attempts[0].ProviderName)
	require.Equal(t, "openai", entry.Data.Attempts[1].ProviderType)
	require.Equal(t, "backup-openai", entry.Data.Attempts[1].ProviderName)

	entries := logger.Entries()
	require.Len(t, entries, 1)
	require.Equal(t, "openai", entries[0].Provider)
	require.Equal(t, "backup-openai", entries[0].ProviderName)
	require.Equal(t, "text-embedding-3-small-v2", entries[0].Model)
	require.Equal(t, backup, entries[0].RoutedModel)
}

func TestEmbeddings_FailoverStopsWhenRequestContextIsCanceled(t *testing.T) {
	provider := &failoverProvider{
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
	c, rec := echotest.Post(t, "/v1/embeddings", `{"model":"text-embedding-3-small","input":"hi"}`)
	ctx, cancel := context.WithCancel(c.Request().Context())
	cancel()
	c.SetRequest(c.Request().WithContext(ctx))

	require.NoError(t, handler.Embeddings(c))
	require.Equal(t, http.StatusServiceUnavailable, rec.Code, rec.Body.String())
	require.Equal(t, []string{"text-embedding-3-small"}, provider.embeddingCalls)
}

func TestEmbeddings_AdmissionDefersPrimaryRateLimitToFailover(t *testing.T) {
	primary := "primary-openai/text-embedding-3-small"
	backup := "backup-openai/text-embedding-3-small"
	provider := &failoverProvider{
		embeddingResponses: map[string]*core.EmbeddingResponse{
			primary: {Object: "list", Model: "text-embedding-3-small", Provider: "openai"},
			backup:  {Object: "list", Model: "text-embedding-3-small", Provider: "openai"},
		},
		supportedModels: map[string]string{primary: "openai", backup: "openai"},
		providerNames:   map[string]string{primary: "primary-openai", backup: "backup-openai"},
	}
	handler := newHandler(provider, nil, nil, nil, nil, nil, failoverResolverStub{
		selectors: []core.ModelSelector{{Provider: "backup-openai", Model: "text-embedding-3-small"}},
	}, nil)
	handler.rateLimiter = newTestRateLimitService(t,
		ratelimit.Rule{Scope: ratelimit.ScopeProvider, Subject: "primary-openai", PeriodSeconds: ratelimit.PeriodMinuteSeconds, MaxRequests: new(int64(1))},
		rateLimitRuleWithRequests("/", 10),
	)

	request := `{"model":"text-embedding-3-small","provider":"primary-openai","input":"hi"}`
	first, firstRec := echotest.Post(t, "/v1/embeddings", request)
	require.NoError(t, handler.Embeddings(first))
	require.Equal(t, http.StatusOK, firstRec.Code, firstRec.Body.String())

	second, secondRec := echotest.Post(t, "/v1/embeddings", request)
	entry := &auditlog.LogEntry{Data: &auditlog.LogData{}}
	second.Set(string(auditlog.LogEntryKey), entry)
	require.NoError(t, handler.Embeddings(second))
	require.Equal(t, http.StatusOK, secondRec.Code, secondRec.Body.String())
	require.Equal(t, []string{primary, backup}, provider.embeddingCalls)
	require.True(t, core.GetFailoverUsed(second.Request().Context()))
	require.Len(t, entry.Data.Attempts, 2)
	require.Equal(t, auditlog.AttemptKindPrimary, entry.Data.Attempts[0].Kind, "saturated primary is recorded but not sent upstream")
	require.Equal(t, auditlog.AttemptKindFailover, entry.Data.Attempts[1].Kind)
}

func TestEmbeddings_FailoverAdmissionReservesBackupRequestsAndCountsConsumerOnce(t *testing.T) {
	primary := "primary-openai/text-embedding-3-small"
	backup := "backup-openai/text-embedding-3-small"
	provider := &failoverProvider{
		embeddingResponses: map[string]*core.EmbeddingResponse{
			primary: {Object: "list", Model: "text-embedding-3-small", Provider: "openai"},
			backup:  {Object: "list", Model: "text-embedding-3-small", Provider: "openai"},
		},
		supportedModels: map[string]string{primary: "openai", backup: "openai"},
		providerNames:   map[string]string{primary: "primary-openai", backup: "backup-openai"},
	}
	handler := newHandler(provider, nil, nil, nil, nil, nil, failoverResolverStub{
		selectors: []core.ModelSelector{{Provider: "backup-openai", Model: "text-embedding-3-small"}},
	}, nil)
	primaryRequests, backupRequests, consumerRequests := int64(1), int64(1), int64(3)
	handler.rateLimiter = newTestRateLimitService(t,
		ratelimit.Rule{Scope: ratelimit.ScopeProvider, Subject: "primary-openai", PeriodSeconds: ratelimit.PeriodMinuteSeconds, MaxRequests: &primaryRequests},
		ratelimit.Rule{Scope: ratelimit.ScopeProvider, Subject: "backup-openai", PeriodSeconds: ratelimit.PeriodMinuteSeconds, MaxRequests: &backupRequests},
		rateLimitRuleWithRequests("/", consumerRequests),
	)
	request := `{"model":"text-embedding-3-small","provider":"primary-openai","input":"hi"}`
	post := func() (*echo.Context, *httptest.ResponseRecorder) {
		c, rec := echotest.Post(t, "/v1/embeddings", request)
		require.NoError(t, handler.Embeddings(c))
		return c, rec
	}

	_, first := post()
	require.Equal(t, http.StatusOK, first.Code, first.Body.String())
	secondContext, second := post()
	require.Equal(t, http.StatusOK, second.Code, second.Body.String(), "backup admission must not count the consumer twice")
	require.True(t, core.GetFailoverUsed(secondContext.Request().Context()))
	_, third := post()
	require.Equal(t, http.StatusTooManyRequests, third.Code, third.Body.String(), "the backup request limit must reject a second backup attempt")
	_, fourth := post()
	require.Equal(t, http.StatusTooManyRequests, fourth.Code, fourth.Body.String(), "each HTTP request must count once against the consumer limit")
	require.Equal(t, []string{primary, backup}, provider.embeddingCalls, "rate-limited backup attempts must not reach the provider")
}

func TestEmbeddings_FailoverAdmissionRecordsRejectedTargetAndHonorsAttemptCap(t *testing.T) {
	primary := "primary-openai/text-embedding-3-small"
	firstBackup := "first-backup-openai/text-embedding-3-small"
	secondBackup := "second-backup-openai/text-embedding-3-small"
	thirdBackup := "third-backup-openai/text-embedding-3-small"
	provider := &failoverProvider{
		embeddingErrors: map[string]error{
			primary:      core.NewProviderError("openai", http.StatusServiceUnavailable, "primary unavailable", nil),
			secondBackup: core.NewProviderError("openai", http.StatusServiceUnavailable, "backup unavailable", nil),
		},
		embeddingResponses: map[string]*core.EmbeddingResponse{
			thirdBackup: {Object: "list", Model: "text-embedding-3-small", Provider: "openai"},
		},
		supportedModels: map[string]string{
			primary: "openai", firstBackup: "openai", secondBackup: "openai", thirdBackup: "openai",
		},
		providerNames: map[string]string{
			primary: "primary-openai", firstBackup: "first-backup-openai",
			secondBackup: "second-backup-openai", thirdBackup: "third-backup-openai",
		},
	}
	handler := newHandler(provider, nil, nil, nil, nil, nil, failoverResolverStub{
		selectors: []core.ModelSelector{
			{Provider: "first-backup-openai", Model: "text-embedding-3-small"},
			{Provider: "second-backup-openai", Model: "text-embedding-3-small"},
			{Provider: "third-backup-openai", Model: "text-embedding-3-small"},
		},
	}, nil)
	handler.failoverPolicy = &gateway.FailoverPolicy{MaxAttempts: 1}
	firstBackupRequests := int64(1)
	limiter := newTestRateLimitService(t, ratelimit.Rule{
		Scope: ratelimit.ScopeProvider, Subject: "first-backup-openai", PeriodSeconds: ratelimit.PeriodMinuteSeconds, MaxRequests: &firstBackupRequests,
	})
	consumed, err := limiter.Acquire(ratelimit.Subjects{Provider: "first-backup-openai"}, time.Now().UTC())
	require.NoError(t, err)
	consumed.Release()
	handler.rateLimiter = limiter

	c, rec := echotest.Post(t, "/v1/embeddings", `{"model":"text-embedding-3-small","provider":"primary-openai","input":"hi"}`)
	entry := &auditlog.LogEntry{Data: &auditlog.LogData{}}
	c.Set(string(auditlog.LogEntryKey), entry)
	require.NoError(t, handler.Embeddings(c))
	require.Equal(t, http.StatusServiceUnavailable, rec.Code, rec.Body.String())
	require.Equal(t, []string{primary, secondBackup}, provider.embeddingCalls, "a rejected route is skipped; the single admitted attempt consumes the cap")
	require.Len(t, entry.Data.Attempts, 3)
	require.Equal(t, auditlog.AttemptKindFailover, entry.Data.Attempts[1].Kind)
	require.Equal(t, firstBackup, entry.Data.Attempts[1].Model)
	require.Equal(t, http.StatusTooManyRequests, entry.Data.Attempts[1].StatusCode)
	require.Equal(t, auditlog.AttemptKindFailover, entry.Data.Attempts[2].Kind)
	require.Equal(t, secondBackup, entry.Data.Attempts[2].Model)
	require.Equal(t, http.StatusServiceUnavailable, entry.Data.Attempts[2].StatusCode)
}

func TestEmbeddings_FailoverReleasesPrimaryConcurrencyBeforeSameProviderAttempt(t *testing.T) {
	primary := "shared-openai/text-embedding-3-small"
	backup := "shared-openai/text-embedding-3-large"
	provider := &failoverProvider{
		embeddingErrors: map[string]error{
			primary: core.NewProviderError("openai", http.StatusServiceUnavailable, "primary unavailable", nil),
		},
		embeddingResponses: map[string]*core.EmbeddingResponse{
			backup: {Object: "list", Model: "text-embedding-3-large", Provider: "openai"},
		},
		supportedModels: map[string]string{primary: "openai", backup: "openai"},
		providerNames:   map[string]string{primary: "shared-openai", backup: "shared-openai"},
	}
	handler := newHandler(provider, nil, nil, nil, nil, nil, failoverResolverStub{
		selectors: []core.ModelSelector{{Provider: "shared-openai", Model: "text-embedding-3-large"}},
	}, nil)
	concurrent := int64(1)
	handler.rateLimiter = newTestRateLimitService(t, ratelimit.Rule{
		Scope: ratelimit.ScopeProvider, Subject: "shared-openai", PeriodSeconds: ratelimit.PeriodConcurrent, MaxRequests: &concurrent,
	})
	c, rec := echotest.Post(t, "/v1/embeddings", `{"model":"text-embedding-3-small","provider":"shared-openai","input":"hi"}`)
	require.NoError(t, handler.Embeddings(c))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, []string{primary, backup}, provider.embeddingCalls)
}

func TestEmbeddings_FailoverAdmissionReservesBackupConcurrencyAndReleasesAfterError(t *testing.T) {
	primary := "primary-openai/text-embedding-3-small"
	backup := "backup-openai/text-embedding-3-small"
	backupStarted := make(chan struct{}, 2)
	releaseFirst := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-releaseFirst:
		default:
			close(releaseFirst)
		}
	})
	var backupMu sync.Mutex
	backupCalls := 0
	provider := &failoverProvider{
		supportedModels: map[string]string{primary: "openai", backup: "openai"},
		providerNames:   map[string]string{primary: "primary-openai", backup: "backup-openai"},
	}
	provider.embeddingCall = func(_ context.Context, req *core.EmbeddingRequest) (*core.EmbeddingResponse, error) {
		if requestSelector(req.Model, req.Provider) == primary {
			return nil, core.NewProviderError("openai", http.StatusServiceUnavailable, "primary unavailable", nil)
		}
		backupMu.Lock()
		backupCalls++
		callNumber := backupCalls
		backupMu.Unlock()
		backupStarted <- struct{}{}
		if callNumber == 1 {
			<-releaseFirst
			return nil, core.NewProviderError("openai", http.StatusServiceUnavailable, "backup unavailable", nil)
		}
		return &core.EmbeddingResponse{Object: "list", Model: "text-embedding-3-small", Provider: "openai"}, nil
	}
	handler := newHandler(provider, nil, nil, nil, nil, nil, failoverResolverStub{
		selectors: []core.ModelSelector{{Provider: "backup-openai", Model: "text-embedding-3-small"}},
	}, nil)
	concurrent := int64(1)
	handler.rateLimiter = newTestRateLimitService(t, ratelimit.Rule{
		Scope: ratelimit.ScopeProvider, Subject: "backup-openai", PeriodSeconds: ratelimit.PeriodConcurrent, MaxRequests: &concurrent,
	})
	request := `{"model":"text-embedding-3-small","provider":"primary-openai","input":"hi"}`
	firstContext, firstRecorder := echotest.Post(t, "/v1/embeddings", request)
	type response struct {
		err  error
		code int
	}
	firstDone := make(chan response, 1)
	go func() { firstDone <- response{err: handler.Embeddings(firstContext), code: firstRecorder.Code} }()
	select {
	case <-backupStarted:
	case <-time.After(2 * time.Second):
		require.FailNow(t, "first backup call did not start")
	}

	secondContext, secondRecorder := echotest.Post(t, "/v1/embeddings", request)
	require.NoError(t, handler.Embeddings(secondContext))
	require.Equal(t, http.StatusTooManyRequests, secondRecorder.Code, secondRecorder.Body.String())
	backupMu.Lock()
	gotBackupCalls := backupCalls
	backupMu.Unlock()
	require.Equal(t, 1, gotBackupCalls, "a held backup concurrency slot must block another upstream attempt")

	close(releaseFirst)
	select {
	case result := <-firstDone:
		require.NoError(t, result.err)
		require.Equal(t, http.StatusServiceUnavailable, result.code)
	case <-time.After(2 * time.Second):
		require.FailNow(t, "first embedding request did not finish")
	}

	thirdContext, thirdRecorder := echotest.Post(t, "/v1/embeddings", request)
	require.NoError(t, handler.Embeddings(thirdContext))
	require.Equal(t, http.StatusOK, thirdRecorder.Code, thirdRecorder.Body.String(), "the failed backup attempt must release its concurrency slot")
	backupMu.Lock()
	gotBackupCalls = backupCalls
	backupMu.Unlock()
	require.Equal(t, 2, gotBackupCalls)
}

func TestEmbeddings_FailoverAdmissionReleasesBackupConcurrencyAfterCancellation(t *testing.T) {
	primary := "primary-openai/text-embedding-3-small"
	backup := "backup-openai/text-embedding-3-small"
	backupStarted := make(chan struct{}, 1)
	var backupMu sync.Mutex
	backupCalls := 0
	provider := &failoverProvider{
		supportedModels: map[string]string{primary: "openai", backup: "openai"},
		providerNames:   map[string]string{primary: "primary-openai", backup: "backup-openai"},
	}
	provider.embeddingCall = func(ctx context.Context, req *core.EmbeddingRequest) (*core.EmbeddingResponse, error) {
		if requestSelector(req.Model, req.Provider) == primary {
			return nil, core.NewProviderError("openai", http.StatusServiceUnavailable, "primary unavailable", nil)
		}
		backupMu.Lock()
		backupCalls++
		callNumber := backupCalls
		backupMu.Unlock()
		if callNumber == 1 {
			close(backupStarted)
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return &core.EmbeddingResponse{Object: "list", Model: "text-embedding-3-small", Provider: "openai"}, nil
	}
	handler := newHandler(provider, nil, nil, nil, nil, nil, failoverResolverStub{
		selectors: []core.ModelSelector{{Provider: "backup-openai", Model: "text-embedding-3-small"}},
	}, nil)
	concurrent := int64(1)
	handler.rateLimiter = newTestRateLimitService(t, ratelimit.Rule{
		Scope: ratelimit.ScopeProvider, Subject: "backup-openai", PeriodSeconds: ratelimit.PeriodConcurrent, MaxRequests: &concurrent,
	})
	request := `{"model":"text-embedding-3-small","provider":"primary-openai","input":"hi"}`
	firstContext, firstRecorder := echotest.Post(t, "/v1/embeddings", request)
	ctx, cancel := context.WithCancel(firstContext.Request().Context())
	t.Cleanup(cancel)
	firstContext.SetRequest(firstContext.Request().WithContext(ctx))
	firstDone := make(chan error, 1)
	go func() { firstDone <- handler.Embeddings(firstContext) }()
	select {
	case <-backupStarted:
	case <-time.After(2 * time.Second):
		require.FailNow(t, "backup call did not start")
	}
	cancel()
	select {
	case err := <-firstDone:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		require.FailNow(t, "canceled embedding request did not finish")
	}
	require.NotZero(t, firstRecorder.Code)

	secondContext, secondRecorder := echotest.Post(t, "/v1/embeddings", request)
	require.NoError(t, handler.Embeddings(secondContext))
	require.Equal(t, http.StatusOK, secondRecorder.Code, secondRecorder.Body.String(), "cancellation must release the backup concurrency slot")
	backupMu.Lock()
	gotBackupCalls := backupCalls
	backupMu.Unlock()
	require.Equal(t, 2, gotBackupCalls)
}

func TestEmbeddings_AdmissionDoesNotFailOverConsumerRejections(t *testing.T) {
	primary := "primary-openai/text-embedding-3-small"
	backup := "backup-openai/text-embedding-3-small"
	newProvider := func() *failoverProvider {
		return &failoverProvider{
			embeddingResponses: map[string]*core.EmbeddingResponse{
				primary: {Object: "list", Model: "text-embedding-3-small", Provider: "openai"},
				backup:  {Object: "list", Model: "text-embedding-3-small", Provider: "openai"},
			},
			supportedModels: map[string]string{primary: "openai", backup: "openai"},
			providerNames:   map[string]string{primary: "primary-openai", backup: "backup-openai"},
		}
	}
	newEmbeddingsHandler := func(provider *failoverProvider) *Handler {
		return newHandler(provider, nil, nil, nil, nil, nil, failoverResolverStub{
			selectors: []core.ModelSelector{{Provider: "backup-openai", Model: "text-embedding-3-small"}},
		}, nil)
	}
	request := `{"model":"text-embedding-3-small","provider":"primary-openai","input":"hi"}`

	t.Run("consumer rate limit", func(t *testing.T) {
		provider := newProvider()
		handler := newEmbeddingsHandler(provider)
		handler.rateLimiter = newTestRateLimitService(t, rateLimitRuleWithRequests("/", 1))

		first, firstRec := echotest.Post(t, "/v1/embeddings", request)
		require.NoError(t, handler.Embeddings(first))
		require.Equal(t, http.StatusOK, firstRec.Code, firstRec.Body.String())

		second, secondRec := echotest.Post(t, "/v1/embeddings", request)
		require.NoError(t, handler.Embeddings(second))
		require.Equal(t, http.StatusTooManyRequests, secondRec.Code, secondRec.Body.String())
		require.Equal(t, []string{primary}, provider.embeddingCalls, "consumer rejection must not try the alternate")
	})

	t.Run("consumer budget", func(t *testing.T) {
		provider := newProvider()
		handler := newEmbeddingsHandler(provider)
		handler.budgetChecker = exceededBudgetChecker{scope: budget.ScopeUserPath}

		c, rec := echotest.Post(t, "/v1/embeddings", request)
		require.NoError(t, handler.Embeddings(c))
		require.Equal(t, http.StatusTooManyRequests, rec.Code, rec.Body.String())
		require.Empty(t, provider.embeddingCalls, "consumer budget rejection must not try any provider")
	})
}

type embeddingProviderBudgetChecker struct{}

func (embeddingProviderBudgetChecker) Check(_ context.Context, subjects budget.Subjects, _ time.Time) error {
	if subjects.Provider != "primary-openai" {
		return nil
	}
	return &budget.ExceededError{Result: budget.CheckResult{
		Budget:    budget.Budget{Scope: budget.ScopeProvider, Subject: subjects.Provider, PeriodSeconds: budget.PeriodDailySeconds, Amount: 1},
		PeriodEnd: time.Now().UTC().Add(time.Hour),
		Spent:     2,
		HasUsage:  true,
	}}
}

func (embeddingProviderBudgetChecker) ProviderWithinBudget(_ context.Context, providerName string) bool {
	return providerName != "primary-openai"
}

func TestEmbeddings_AdmissionDefersPrimaryProviderBudgetToFailover(t *testing.T) {
	primary := "primary-openai/text-embedding-3-small"
	backup := "backup-openai/text-embedding-3-small"
	provider := &failoverProvider{
		embeddingResponses: map[string]*core.EmbeddingResponse{
			backup: {Object: "list", Model: "text-embedding-3-small", Provider: "openai"},
		},
		supportedModels: map[string]string{primary: "openai", backup: "openai"},
		providerNames:   map[string]string{primary: "primary-openai", backup: "backup-openai"},
	}
	handler := newHandler(provider, nil, nil, nil, nil, nil, failoverResolverStub{
		selectors: []core.ModelSelector{{Provider: "backup-openai", Model: "text-embedding-3-small"}},
	}, nil)
	handler.budgetChecker = embeddingProviderBudgetChecker{}

	c, rec := echotest.Post(t, "/v1/embeddings", `{"model":"text-embedding-3-small","provider":"primary-openai","input":"hi"}`)
	require.NoError(t, handler.Embeddings(c))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, []string{backup}, provider.embeddingCalls, "provider budget fallback skips the exhausted primary")
	require.True(t, core.GetFailoverUsed(c.Request().Context()))
}

// TestChatCompletion_SingleTargetAliasKeepsSubtreeFailover pins the
// end-to-end contract through the real virtualmodels.Service: a redirect
// with one declared target whose target is a chained failover redirect keeps
// the subtree's alternative leaves as its failover chain. A future change
// that re-narrows the chain to declared targets only must fail this test.
func TestChatCompletion_SingleTargetAliasKeepsSubtreeFailover(t *testing.T) {
	catalog := &aliasesTestCatalog{
		supported: map[string]bool{
			"vendor-a/model-x": true,
			"vendor-b/model-y": true,
		},
		providerTypes: map[string]string{
			"vendor-a/model-x": "vendor-a",
			"vendor-b/model-y": "vendor-b",
		},
	}
	service, err := virtualmodels.NewService(newAliasesTestStore(
		virtualmodels.VirtualModel{
			Source:  "public/model",
			Targets: []virtualmodels.Target{{Model: "router/main"}},
			Enabled: true,
		},
		virtualmodels.VirtualModel{
			Source:   "router/main",
			Strategy: virtualmodels.StrategyFailover,
			Targets: []virtualmodels.Target{
				{Model: "vendor-a/model-x"},
				{Model: "vendor-b/model-y"},
			},
			Enabled: true,
		},
	), catalog, true)
	require.NoError(t, err)
	require.NoError(t, service.Refresh(context.Background()))

	provider := &failoverProvider{
		chatResponses: map[string]*core.ChatResponse{
			"vendor-b/model-y": {
				ID:       "chatcmpl-subtree-failover",
				Object:   "chat.completion",
				Model:    "model-y",
				Provider: "vendor-b",
				Choices: []core.Choice{{
					Index:        0,
					Message:      core.ResponseMessage{Role: "assistant", Content: "subtree failover ok"},
					FinishReason: "stop",
				}},
			},
		},
		chatErrors: map[string]error{
			"vendor-a/model-x": core.NewProviderError("vendor-a", http.StatusServiceUnavailable, "model temporarily unavailable", nil),
		},
		supportedModels: map[string]string{
			"vendor-a/model-x": "vendor-a",
			"vendor-b/model-y": "vendor-b",
		},
	}

	handler := newHandler(provider, nil, nil, nil, service, nil, service, nil)

	c, rec := echotest.Post(t, "/v1/chat/completions", `{"model":"public/model","messages":[{"role":"user","content":"hi"}]}`)
	entry := &auditlog.LogEntry{Data: &auditlog.LogData{}}
	c.Set(string(auditlog.LogEntryKey), entry)
	err = handler.ChatCompletion(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, []string{"vendor-a/model-x", "vendor-b/model-y"}, provider.chatCalls)
	require.Contains(t, rec.Body.String(), "subtree failover ok")
	require.NotNil(t, entry.Data)
	require.Len(t, entry.Data.Attempts, 2)
	require.Equal(t, auditlog.AttemptKindPrimary, entry.Data.Attempts[0].Kind)
	require.False(t, entry.Data.Attempts[0].Success)
	require.Equal(t, auditlog.AttemptKindFailover, entry.Data.Attempts[1].Kind)
	require.True(t, entry.Data.Attempts[1].Success)
	require.Equal(t, "vendor-b/model-y", entry.Data.Attempts[1].Model)
}

func requestSelector(model, provider string) string {
	selector, err := core.ParseModelSelector(model, provider)
	if err != nil {
		return strings.TrimSpace(model)
	}
	return selector.QualifiedModel()
}
