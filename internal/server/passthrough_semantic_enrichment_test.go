package server

import (
	"net/http"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/echotest"
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
	c, _ := echotest.Post(t, "/p/openai/v1/responses", `{"model":"gpt-5-mini","stream":true}`)

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
	require.NotNil(t, capturedWorkflow)
	require.NotNil(t, capturedWorkflow.Passthrough)
	require.Equal(t, "responses", capturedWorkflow.Passthrough.NormalizedEndpoint)
	require.Equal(t, "openai.responses", capturedWorkflow.Passthrough.SemanticOperation)
	require.Equal(t, "/v1/responses", capturedWorkflow.Passthrough.AuditPath)
	require.True(t, capturedWorkflow.Passthrough.Stream)
}
