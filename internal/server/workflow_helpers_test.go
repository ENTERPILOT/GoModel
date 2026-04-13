package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"gomodel/internal/auditlog"
	"gomodel/internal/core"
)

func TestEnsureTranslatedRequestWorkflow_CompletesPartialWorkflowFromDecodedSelector(t *testing.T) {
	provider := &mockProvider{supportedModels: []string{"gpt-4o-mini"}}

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	rec := httptest.NewRecorder()

	desc := core.DescribeEndpoint(req.Method, req.URL.Path)
	ctx := core.WithWorkflow(req.Context(), &core.Workflow{
		RequestID:    "req-partial-workflow",
		Endpoint:     desc,
		Mode:         core.ExecutionModeTranslated,
		Capabilities: core.CapabilitiesForEndpoint(desc),
	})
	req = req.WithContext(ctx)

	c := e.NewContext(req, rec)
	entry := &auditlog.LogEntry{Data: &auditlog.LogData{}}
	c.Set(string(auditlog.LogEntryKey), entry)
	model := "gpt-4o-mini"
	providerHint := ""

	workflow, err := ensureTranslatedRequestWorkflow(c, provider, nil, nil, &model, &providerHint)
	require.NoError(t, err)
	require.NotNil(t, workflow)

	assert.Equal(t, "gpt-4o-mini", model)
	assert.Equal(t, "", providerHint)
	assert.Equal(t, core.ExecutionModeTranslated, workflow.Mode)
	assert.Equal(t, "mock", workflow.ProviderType)
	if assert.NotNil(t, workflow.Resolution) {
		assert.Equal(t, "gpt-4o-mini", workflow.Resolution.Requested.Model)
		assert.Equal(t, "gpt-4o-mini", workflow.Resolution.ResolvedSelector.Model)
	}

	storedWorkflow := core.GetWorkflow(c.Request().Context())
	if assert.NotNil(t, storedWorkflow) {
		assert.Equal(t, "mock", storedWorkflow.ProviderType)
		assert.Equal(t, "gpt-4o-mini", storedWorkflow.ResolvedQualifiedModel())
		if assert.NotNil(t, storedWorkflow.Resolution) {
			assert.Equal(t, "mock", storedWorkflow.Resolution.ProviderType)
			assert.Equal(t, "gpt-4o-mini", storedWorkflow.Resolution.ResolvedSelector.Model)
		}
	}
	assert.Equal(t, "gpt-4o-mini", entry.RequestedModel)
	assert.Equal(t, "gpt-4o-mini", entry.ResolvedModel)
	assert.Equal(t, "mock", entry.Provider)
}
