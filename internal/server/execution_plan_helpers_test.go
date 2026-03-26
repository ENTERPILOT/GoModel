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

func TestEnsureTranslatedRequestPlan_CompletesPartialPlanFromDecodedSelector(t *testing.T) {
	provider := &mockProvider{supportedModels: []string{"gpt-4o-mini"}}

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	rec := httptest.NewRecorder()

	desc := core.DescribeEndpoint(req.Method, req.URL.Path)
	ctx := core.WithExecutionPlan(req.Context(), &core.ExecutionPlan{
		RequestID:    "req-partial-plan",
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

	plan, err := ensureTranslatedRequestPlan(c, provider, nil, nil, &model, &providerHint)
	require.NoError(t, err)
	require.NotNil(t, plan)

	assert.Equal(t, "gpt-4o-mini", model)
	assert.Equal(t, "", providerHint)
	assert.Equal(t, core.ExecutionModeTranslated, plan.Mode)
	assert.Equal(t, "mock", plan.ProviderType)
	if assert.NotNil(t, plan.Resolution) {
		assert.Equal(t, "gpt-4o-mini", plan.Resolution.Requested.Model)
		assert.Equal(t, "gpt-4o-mini", plan.Resolution.ResolvedSelector.Model)
	}

	storedPlan := core.GetExecutionPlan(c.Request().Context())
	if assert.NotNil(t, storedPlan) {
		assert.Equal(t, "mock", storedPlan.ProviderType)
		assert.Equal(t, "gpt-4o-mini", storedPlan.ResolvedQualifiedModel())
		if assert.NotNil(t, storedPlan.Resolution) {
			assert.Equal(t, "mock", storedPlan.Resolution.ProviderType)
			assert.Equal(t, "gpt-4o-mini", storedPlan.Resolution.ResolvedSelector.Model)
		}
	}
	assert.Equal(t, "gpt-4o-mini", entry.Model)
	assert.Equal(t, "gpt-4o-mini", entry.ResolvedModel)
	assert.Equal(t, "mock", entry.Provider)
}
