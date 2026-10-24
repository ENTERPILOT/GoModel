package server

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/auditlog"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/echotest"
)

func TestEnsureTranslatedRequestWorkflow_CompletesPartialWorkflowFromDecodedSelector(t *testing.T) {
	provider := &mockProvider{supportedModels: []string{"gpt-4o-mini"}}

	entry := &auditlog.LogEntry{Data: &auditlog.LogData{}}
	c, _ := echotest.Post(t, "/v1/chat/completions", nil, echotest.WithValue(string(auditlog.LogEntryKey), entry))

	desc := core.DescribeEndpoint(http.MethodPost, "/v1/chat/completions")
	c.SetRequest(c.Request().WithContext(core.WithWorkflow(c.Request().Context(), &core.Workflow{
		RequestID:    "req-partial-workflow",
		Endpoint:     desc,
		Mode:         core.ExecutionModeTranslated,
		Capabilities: core.CapabilitiesForEndpoint(desc),
	})))
	model := "gpt-4o-mini"
	providerHint := ""

	workflow, err := ensureTranslatedRequestWorkflowWithAuthorizer(c, provider, nil, nil, nil, &model, &providerHint)
	require.NoError(t, err)
	require.NotNil(t, workflow)
	assert.Equal(t, "gpt-4o-mini", model)
	assert.Empty(t, providerHint)
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
