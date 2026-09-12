package server

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/auditlog"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/echotest"
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

	c, rec := echotest.Post(t, "/v1/embeddings", `{"model":"text-embedding-3-small","input":"hi"}`)
	err := handler.Embeddings(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusServiceUnavailable, rec.Code, rec.Body.String())
	require.Equal(t, []string{"text-embedding-3-small"}, provider.embeddingCalls)
}

func requestSelector(model, provider string) string {
	selector, err := core.ParseModelSelector(model, provider)
	if err != nil {
		return strings.TrimSpace(model)
	}
	return selector.QualifiedModel()
}
