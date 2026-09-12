package server

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/auditlog"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/echotest"
)

type canonicalizingProvider struct {
	resolved map[string]core.ModelSelector
	types    map[string]string
	names    map[string]string
}

func (p *canonicalizingProvider) ResolveModel(requested core.RequestedModelSelector) (core.ModelSelector, bool, error) {
	key := requested.RequestedQualifiedModel()
	if selector, ok := p.resolved[key]; ok {
		return selector, selector.QualifiedModel() != key, nil
	}
	selector, err := requested.Normalize()
	return selector, false, err
}

func (p *canonicalizingProvider) Supports(model string) bool {
	_, ok := p.types[model]
	return ok
}

func (p *canonicalizingProvider) GetProviderType(model string) string {
	return p.types[model]
}

func (p *canonicalizingProvider) GetProviderName(model string) string {
	return p.names[model]
}

func (p *canonicalizingProvider) GetProviderTypeForName(providerName string) string {
	providerName = strings.TrimSpace(providerName)
	if providerName == "" {
		return ""
	}
	for qualifiedModel, candidate := range p.names {
		if strings.TrimSpace(candidate) != providerName {
			continue
		}
		return strings.TrimSpace(p.types[qualifiedModel])
	}
	return ""
}

func (p *canonicalizingProvider) ChatCompletion(_ context.Context, _ *core.ChatRequest) (*core.ChatResponse, error) {
	return nil, nil
}

func (p *canonicalizingProvider) StreamChatCompletion(_ context.Context, _ *core.ChatRequest) (io.ReadCloser, error) {
	return nil, nil
}

func (p *canonicalizingProvider) ListModels(_ context.Context) (*core.ModelsResponse, error) {
	return nil, nil
}

func (p *canonicalizingProvider) Responses(_ context.Context, _ *core.ResponsesRequest) (*core.ResponsesResponse, error) {
	return nil, nil
}

func (p *canonicalizingProvider) StreamResponses(_ context.Context, _ *core.ResponsesRequest) (io.ReadCloser, error) {
	return nil, nil
}

func (p *canonicalizingProvider) Embeddings(_ context.Context, _ *core.EmbeddingRequest) (*core.EmbeddingResponse, error) {
	return nil, nil
}

func TestResolveRequestModel_UsesResolvedProviderNameInsteadOfSelectorPrefix(t *testing.T) {
	provider := &mockProvider{
		supportedModels: []string{"gpt-5-nano"},
		providerTypes: map[string]string{
			"openai/gpt-5-nano": "openai",
		},
		providerNames: map[string]string{
			"openai/gpt-5-nano": "openai_test",
		},
	}

	resolution, err := resolveRequestModelWithAuthorizer(context.Background(), provider, nil, nil, core.NewRequestedModelSelector("openai/gpt-5-nano", ""))
	require.NoError(t, err)
	require.Equal(t, "openai", resolution.ResolvedSelector.Provider)
	require.Equal(t, "openai_test", resolution.ProviderName)
}

func TestResolveRequestModel_CanonicalizesProviderTypeSelectorToConcreteProviderName(t *testing.T) {
	provider := &canonicalizingProvider{
		resolved: map[string]core.ModelSelector{
			"openai/gpt-5-nano": {Provider: "openai_test", Model: "gpt-5-nano"},
		},
		types: map[string]string{
			"openai_test/gpt-5-nano": "openai",
		},
		names: map[string]string{
			"openai_test/gpt-5-nano": "openai_test",
		},
	}

	resolution, err := resolveRequestModelWithAuthorizer(context.Background(), provider, nil, nil, core.NewRequestedModelSelector("openai/gpt-5-nano", ""))
	require.NoError(t, err)
	require.Equal(t, "openai_test/gpt-5-nano", resolution.ResolvedQualifiedModel())
	require.Equal(t, "openai", resolution.ProviderType)
	require.Equal(t, "openai_test", resolution.ProviderName)
}

type aliasResolverStub struct{}

func (aliasResolverStub) ResolveModel(requested core.RequestedModelSelector) (core.ModelSelector, bool, error) {
	if requested.RequestedQualifiedModel() == "anthropic/claude-opus-4-6" {
		return core.ModelSelector{Provider: "openai", Model: "gpt-5-nano"}, true, nil
	}
	selector, err := requested.Normalize()
	return selector, false, err
}

func TestResolveRequestModel_CanonicalizesAliasOutputThroughProviderResolver(t *testing.T) {
	provider := &canonicalizingProvider{
		resolved: map[string]core.ModelSelector{
			"openai/gpt-5-nano": {Provider: "openai_test", Model: "gpt-5-nano"},
		},
		types: map[string]string{
			"openai_test/gpt-5-nano": "openai",
		},
		names: map[string]string{
			"openai_test/gpt-5-nano": "openai_test",
		},
	}

	resolution, err := resolveRequestModelWithAuthorizer(context.Background(), provider, aliasResolverStub{}, nil, core.NewRequestedModelSelector("anthropic/claude-opus-4-6", ""))
	require.NoError(t, err)
	require.True(t, resolution.AliasApplied)
	require.Equal(t, "openai_test/gpt-5-nano", resolution.ResolvedQualifiedModel())
	require.Equal(t, "openai", resolution.ProviderType)
	require.Equal(t, "openai_test", resolution.ProviderName)
}

func TestEnrichAuditEntryWithRequestedModelDoesNotPublishBodyBeforePolicy(t *testing.T) {
	logger := &requestModelLiveLogger{
		cfg: auditlog.Config{
			Enabled:   true,
			LogBodies: true,
		},
	}

	c, _ := echotest.Post(t, "/v1/chat/completions", nil)
	c.SetRequest(c.Request().WithContext(core.WithRequestSnapshot(c.Request().Context(), core.NewRequestSnapshot(
		http.MethodPost,
		"/v1/chat/completions",
		nil,
		nil,
		nil,
		"application/json",
		[]byte(`{"model":"hidden"}`),
		false,
		"req-hidden",
		nil,
	))))

	handler := auditlog.Middleware(logger)(func(c *echo.Context) error {
		enrichAuditEntryWithRequestedModel(c, core.NewRequestedModelSelector("gpt-test", ""))
		require.Len(t, logger.events, 2)

		updated := logger.events[1]
		require.Equal(t, auditlog.LiveEventAuditUpdated, updated.eventType)
		require.Equal(t, "gpt-test", updated.requestedModel)
		require.Nil(t, updated.requestBody)

		workflow := &core.Workflow{
			Policy: &core.ResolvedWorkflowPolicy{
				VersionID: "audit-disabled",
				Features: core.WorkflowFeatures{
					Audit: false,
				},
			},
		}
		c.SetRequest(c.Request().WithContext(core.WithWorkflow(c.Request().Context(), workflow)))
		return nil
	})
	err := handler(c)
	require.NoError(t, err)
	require.Len(t, logger.events, 3)

	removed := logger.events[2]
	require.Equal(t, auditlog.LiveEventAuditRemoved, removed.eventType)
	require.Nil(t, removed.requestBody)
	require.Equal(t, 0, logger.writes)
}

type requestModelLiveEvent struct {
	eventType      string
	requestedModel string
	requestBody    any
}

type requestModelLiveLogger struct {
	cfg    auditlog.Config
	events []requestModelLiveEvent
	writes int
}

func (l *requestModelLiveLogger) Write(_ *auditlog.LogEntry) {
	l.writes++
}

func (l *requestModelLiveLogger) Config() auditlog.Config {
	return l.cfg
}

func (l *requestModelLiveLogger) Close() error {
	return nil
}

func (l *requestModelLiveLogger) PublishLiveEvent(eventType string, entry *auditlog.LogEntry) {
	event := requestModelLiveEvent{eventType: eventType}
	if entry != nil {
		event.requestedModel = entry.RequestedModel
		if entry.Data != nil {
			event.requestBody = entry.Data.RequestBody
		}
	}
	l.events = append(l.events, event)
}
