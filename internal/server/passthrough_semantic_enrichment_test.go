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

func TestPassthroughSemanticEnrichment_EnrichesPromptBeforePlanning(t *testing.T) {
	provider := &mockProvider{}
	e := echo.New()

	req := httptest.NewRequest(http.MethodPost, "/p/openai/v1/responses", strings.NewReader(`{"model":"gpt-5-mini"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	var capturedPlan *core.ExecutionPlan
	handler := PassthroughSemanticEnrichment(provider, []core.PassthroughSemanticEnricher{
		passthroughSemanticEnricherStub{providerType: "openai"},
	}, true)(ExecutionPlanning(provider)(func(c *echo.Context) error {
		capturedPlan = core.GetExecutionPlan(c.Request().Context())
		return c.String(http.StatusOK, "ok")
	}))

	ctxReq, _ := ensureRequestID(c.Request())
	c.SetRequest(ctxReq)
	err := RequestSnapshotCapture()(handler)(c)
	require.NoError(t, err)

	if capturedPlan == nil || capturedPlan.Passthrough == nil {
		t.Fatal("expected passthrough execution plan")
	}
	if capturedPlan.Passthrough.NormalizedEndpoint != "responses" {
		t.Fatalf("NormalizedEndpoint = %q, want responses", capturedPlan.Passthrough.NormalizedEndpoint)
	}
	if capturedPlan.Passthrough.SemanticOperation != "openai.responses" {
		t.Fatalf("SemanticOperation = %q, want openai.responses", capturedPlan.Passthrough.SemanticOperation)
	}
	if capturedPlan.Passthrough.AuditPath != "/v1/responses" {
		t.Fatalf("AuditPath = %q, want /v1/responses", capturedPlan.Passthrough.AuditPath)
	}
}
