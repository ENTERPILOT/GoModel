package server

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"

	"gomodel/internal/auditlog"
	"gomodel/internal/core"
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
	if err != nil {
		t.Fatalf("resolveRequestModelWithAuthorizer() error = %v", err)
	}

	if got := resolution.ResolvedSelector.Provider; got != "openai" {
		t.Fatalf("ResolvedSelector.Provider = %q, want %q", got, "openai")
	}
	if got := resolution.ProviderName; got != "openai_test" {
		t.Fatalf("ProviderName = %q, want %q", got, "openai_test")
	}
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
	if err != nil {
		t.Fatalf("resolveRequestModelWithAuthorizer() error = %v", err)
	}

	if got := resolution.ResolvedQualifiedModel(); got != "openai_test/gpt-5-nano" {
		t.Fatalf("ResolvedQualifiedModel = %q, want %q", got, "openai_test/gpt-5-nano")
	}
	if got := resolution.ProviderType; got != "openai" {
		t.Fatalf("ProviderType = %q, want %q", got, "openai")
	}
	if got := resolution.ProviderName; got != "openai_test" {
		t.Fatalf("ProviderName = %q, want %q", got, "openai_test")
	}
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
	if err != nil {
		t.Fatalf("resolveRequestModelWithAuthorizer() error = %v", err)
	}

	if !resolution.AliasApplied {
		t.Fatal("AliasApplied = false, want true")
	}
	if got := resolution.ResolvedQualifiedModel(); got != "openai_test/gpt-5-nano" {
		t.Fatalf("ResolvedQualifiedModel = %q, want %q", got, "openai_test/gpt-5-nano")
	}
	if got := resolution.ProviderType; got != "openai" {
		t.Fatalf("ProviderType = %q, want %q", got, "openai")
	}
	if got := resolution.ProviderName; got != "openai_test" {
		t.Fatalf("ProviderName = %q, want %q", got, "openai_test")
	}
}

func TestEnrichAuditEntryWithRequestedModelDoesNotPublishBodyBeforePolicy(t *testing.T) {
	logger := &requestModelLiveLogger{
		cfg: auditlog.Config{
			Enabled:   true,
			LogBodies: true,
		},
	}

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req = req.WithContext(core.WithRequestSnapshot(req.Context(), core.NewRequestSnapshot(
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
	)))
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	handler := auditlog.Middleware(logger)(func(c *echo.Context) error {
		enrichAuditEntryWithRequestedModel(c, core.NewRequestedModelSelector("gpt-test", ""))
		if len(logger.events) != 2 {
			t.Fatalf("live events before policy resolution = %d, want 2", len(logger.events))
		}
		updated := logger.events[1]
		if updated.eventType != auditlog.LiveEventAuditUpdated {
			t.Fatalf("second event type = %q, want %q", updated.eventType, auditlog.LiveEventAuditUpdated)
		}
		if updated.requestedModel != "gpt-test" {
			t.Fatalf("requested model = %q, want gpt-test", updated.requestedModel)
		}
		if updated.requestBody != nil {
			t.Fatalf("request body before policy resolution = %#v, want nil", updated.requestBody)
		}

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

	if err := handler(c); err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if len(logger.events) != 3 {
		t.Fatalf("live events after audit removal = %d, want 3", len(logger.events))
	}
	removed := logger.events[2]
	if removed.eventType != auditlog.LiveEventAuditRemoved {
		t.Fatalf("third event type = %q, want %q", removed.eventType, auditlog.LiveEventAuditRemoved)
	}
	if removed.requestBody != nil {
		t.Fatalf("removed event request body = %#v, want nil", removed.requestBody)
	}
	if logger.writes != 0 {
		t.Fatalf("audit writes = %d, want 0", logger.writes)
	}
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
