package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/require"

	"gomodel/internal/core"
)

type passthroughSemanticEnricherStub struct {
	providerType string
}

func (p passthroughSemanticEnricherStub) ProviderType() string {
	return p.providerType
}

func (p passthroughSemanticEnricherStub) Enrich(_ *core.RequestSnapshot, _ *core.WhiteBoxPrompt, info *core.PassthroughRouteInfo) *core.PassthroughRouteInfo {
	if info == nil {
		return nil
	}
	cloned := *info
	cloned.SemanticOperation = p.providerType + ".responses"
	cloned.AuditPath = "/v1/responses"
	return &cloned
}

func TestPassthroughSemanticEnrichment_EnrichesPromptBeforeWorkflowResolution(t *testing.T) {
	provider := &mockProvider{}
	e := echo.New()

	req := httptest.NewRequest(http.MethodPost, "/p/openai/v1/responses", strings.NewReader(`{"model":"gpt-5-mini"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	var capturedWorkflow *core.Workflow
	handler := PassthroughSemanticEnrichment(provider, []core.PassthroughSemanticEnricher{
		passthroughSemanticEnricherStub{providerType: "openai"},
	}, true)(WorkflowResolution(provider)(func(c *echo.Context) error {
		capturedWorkflow = core.GetWorkflow(c.Request().Context())
		return c.String(http.StatusOK, "ok")
	}))

	ctxReq, _ := ensureRequestID(c.Request())
	c.SetRequest(ctxReq)
	err := RequestSnapshotCapture()(handler)(c)
	require.NoError(t, err)

	if capturedWorkflow == nil || capturedWorkflow.Passthrough == nil {
		t.Fatal("expected passthrough workflow")
	}
	if capturedWorkflow.Passthrough.NormalizedEndpoint != "responses" {
		t.Fatalf("NormalizedEndpoint = %q, want responses", capturedWorkflow.Passthrough.NormalizedEndpoint)
	}
	if capturedWorkflow.Passthrough.SemanticOperation != "openai.responses" {
		t.Fatalf("SemanticOperation = %q, want openai.responses", capturedWorkflow.Passthrough.SemanticOperation)
	}
	if capturedWorkflow.Passthrough.AuditPath != "/v1/responses" {
		t.Fatalf("AuditPath = %q, want /v1/responses", capturedWorkflow.Passthrough.AuditPath)
	}
}
