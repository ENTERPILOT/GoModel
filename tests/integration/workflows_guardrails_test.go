//go:build integration

package integration

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"gomodel/internal/auditlog"
	"gomodel/internal/authkeys"
	"gomodel/internal/core"
	"gomodel/internal/guardrails"
	"gomodel/internal/workflows"
	"gomodel/tests/integration/dbassert"
)

func TestManagedAuthKeyWorkflow_AuditAndUsageValidity_PostgreSQL(t *testing.T) {
	fixture := SetupTestServer(t, TestServerConfig{
		DBType:                "postgresql",
		AuditLogEnabled:       true,
		UsageEnabled:          true,
		LogBodies:             true,
		LogHeaders:            true,
		AdminEndpointsEnabled: true,
		OnlyModelInteractions: false,
		MasterKey:             "integration-master-key",
	})
	defer fixture.Shutdown(t)

	issuedKey := createManagedAuthKey(t, fixture.ServerURL, "integration-master-key", map[string]any{
		"name":        "managed-workflow-key",
		"description": "integration managed auth key",
		"user_path":   "/team/integration/workflows",
	})

	workflow := createWorkflow(t, fixture.ServerURL, "integration-master-key", map[string]any{
		"scope_provider_name": "test",
		"scope_model":         "gpt-4",
		"scope_user_path":     issuedKey.UserPath,
		"name":                "managed-auth-workflow",
		"description":         "disable cache for managed auth key path",
		"workflow_payload": workflows.Payload{
			SchemaVersion: 1,
			Features: workflows.FeatureFlags{
				Cache:      false,
				Audit:      true,
				Usage:      true,
				Guardrails: false,
				Fallback:   boolPtr(false),
			},
			Guardrails: []workflows.GuardrailStep{},
		},
	})
	require.Equal(t, "test", workflow.Scope.Provider)
	require.Equal(t, "gpt-4", workflow.Scope.Model)
	require.Equal(t, issuedKey.UserPath, workflow.Scope.UserPath)

	requestID := uuid.NewString()
	req := newChatRequest("gpt-4", "Hello from managed workflow")
	req.Provider = "test"

	resp := sendChatRequestWithHeaders(t, fixture.ServerURL, req, map[string]string{
		"Authorization": "Bearer " + issuedKey.Value,
		core.RequestIDHeader:  requestID,
	})
	require.Equal(t, http.StatusOK, resp.StatusCode)
	closeBody(resp)

	fixture.FlushAndClose(t)

	auditEntries := dbassert.QueryAuditLogsByRequestID(t, fixture.PgPool, requestID)
	require.Len(t, auditEntries, 1, "expected one audit log entry")
	auditEntry := auditEntries[0]

	dbassert.AssertAuditLogFieldCompleteness(t, auditEntry)
	dbassert.AssertAuditLogMatches(t, dbassert.ExpectedAuditLog{
		Provider:   "test",
		StatusCode: http.StatusOK,
		Method:     http.MethodPost,
		Path:       "/v1/chat/completions",
		RequestID:  requestID,
	}, auditEntry)
	dbassert.AssertAuditLogDurationPositive(t, auditEntry)
	dbassert.AssertNoErrorType(t, auditEntry)
	dbassert.AssertAuditLogHasData(t, auditEntry)
	dbassert.AssertAuditLogHasBody(t, auditEntry, true, true)
	dbassert.AssertAuditLogHasHeaders(t, auditEntry, true, true)

	assert.True(t, strings.HasSuffix(auditEntry.Model, "gpt-4"), "expected requested model to end with gpt-4, got %q", auditEntry.Model)
	assert.Equal(t, workflow.ID, auditEntry.WorkflowVersionID)
	assert.Equal(t, issuedKey.ID, auditEntry.AuthKeyID)
	assert.Equal(t, auditlog.AuthMethodAPIKey, auditEntry.AuthMethod)
	assert.Equal(t, issuedKey.UserPath, auditEntry.UserPath)
	assert.Empty(t, auditEntry.CacheType)

	require.NotNil(t, auditEntry.Data)
	require.NotNil(t, auditEntry.Data.WorkflowFeatures)
	assert.False(t, auditEntry.Data.WorkflowFeatures.Cache)
	assert.True(t, auditEntry.Data.WorkflowFeatures.Audit)
	assert.True(t, auditEntry.Data.WorkflowFeatures.Usage)
	assert.False(t, auditEntry.Data.WorkflowFeatures.Guardrails)
	assert.False(t, auditEntry.Data.WorkflowFeatures.Fallback)

	usageEntries := dbassert.QueryUsageByRequestID(t, fixture.PgPool, requestID)
	require.Len(t, usageEntries, 1, "expected one usage entry")
	usageEntry := usageEntries[0]

	dbassert.AssertUsageFieldCompleteness(t, usageEntry)
	dbassert.AssertUsageMatches(t, dbassert.ExpectedUsage{
		Provider:  "test",
		Endpoint:  "/v1/chat/completions",
		RequestID: requestID,
	}, usageEntry)
	dbassert.AssertUsageHasTokens(t, usageEntry)
	dbassert.AssertUsageTokensConsistent(t, usageEntry)

	assert.True(t, strings.HasSuffix(usageEntry.Model, "gpt-4"), "expected usage model to end with gpt-4, got %q", usageEntry.Model)
	assert.Equal(t, issuedKey.UserPath, usageEntry.UserPath)
	assert.Empty(t, usageEntry.CacheType)
}

func TestGuardrailWorkflow_RewritesUpstreamRequestAndPreservesAuditUsage_PostgreSQL(t *testing.T) {
	fixture := SetupTestServer(t, TestServerConfig{
		DBType:                "postgresql",
		AuditLogEnabled:       true,
		UsageEnabled:          true,
		LogBodies:             true,
		LogHeaders:            true,
		AdminEndpointsEnabled: true,
		GuardrailsEnabled:     true,
		OnlyModelInteractions: false,
		MasterKey:             "integration-master-key",
	})
	defer fixture.Shutdown(t)

	guardrail := upsertGuardrail(t, fixture.ServerURL, "integration-master-key", "policy-system", map[string]any{
		"type":        "system_prompt",
		"description": "integration override policy",
		"config": map[string]any{
			"mode":    "override",
			"content": "Guardrail says be precise.",
		},
	})
	require.Equal(t, "policy-system", guardrail.Name)

	workflow := createWorkflow(t, fixture.ServerURL, "integration-master-key", map[string]any{
		"scope_provider_name": "test",
		"scope_model":         "gpt-4",
		"name":                "guardrailed-workflow",
		"description":         "apply policy-system to test provider",
		"workflow_payload": workflows.Payload{
			SchemaVersion: 1,
			Features: workflows.FeatureFlags{
				Cache:      false,
				Audit:      true,
				Usage:      true,
				Guardrails: true,
				Fallback:   boolPtr(false),
			},
			Guardrails: []workflows.GuardrailStep{
				{Ref: "policy-system", Step: 10},
			},
		},
	})
	require.Equal(t, "test", workflow.Scope.Provider)
	require.Equal(t, "gpt-4", workflow.Scope.Model)

	fixture.MockLLM.ResetRequests()

	requestID := uuid.NewString()
	req := core.ChatRequest{
		Model:    "gpt-4",
		Provider: "test",
		Messages: []core.Message{
			{Role: "system", Content: "Original system prompt"},
			{Role: "user", Content: "Guardrail hello"},
		},
	}

	resp := sendChatRequestWithHeaders(t, fixture.ServerURL, req, map[string]string{
		"Authorization": "Bearer integration-master-key",
		core.RequestIDHeader:  requestID,
	})
	require.Equal(t, http.StatusOK, resp.StatusCode)
	closeBody(resp)

	upstreamRequests := fixture.MockLLM.Requests()
	require.NotEmpty(t, upstreamRequests, "expected the mock provider to observe an upstream request")

	lastUpstream := upstreamRequests[len(upstreamRequests)-1]
	assert.Equal(t, http.MethodPost, lastUpstream.Method)
	assert.Equal(t, "/v1/chat/completions", lastUpstream.Path)

	var upstreamPayload core.ChatRequest
	require.NoError(t, json.Unmarshal(lastUpstream.Body, &upstreamPayload))
	require.Len(t, upstreamPayload.Messages, 2)
	assert.Equal(t, "system", upstreamPayload.Messages[0].Role)
	assert.Equal(t, "Guardrail says be precise.", core.ExtractTextContent(upstreamPayload.Messages[0].Content))
	assert.Equal(t, "user", upstreamPayload.Messages[1].Role)
	assert.Equal(t, "Guardrail hello", core.ExtractTextContent(upstreamPayload.Messages[1].Content))

	fixture.FlushAndClose(t)

	auditEntries := dbassert.QueryAuditLogsByRequestID(t, fixture.PgPool, requestID)
	require.Len(t, auditEntries, 1, "expected one audit log entry")
	auditEntry := auditEntries[0]

	dbassert.AssertAuditLogFieldCompleteness(t, auditEntry)
	dbassert.AssertAuditLogMatches(t, dbassert.ExpectedAuditLog{
		Provider:   "test",
		StatusCode: http.StatusOK,
		Method:     http.MethodPost,
		Path:       "/v1/chat/completions",
		RequestID:  requestID,
	}, auditEntry)
	dbassert.AssertAuditLogDurationPositive(t, auditEntry)
	dbassert.AssertNoErrorType(t, auditEntry)
	dbassert.AssertAuditLogHasData(t, auditEntry)

	assert.True(t, strings.HasSuffix(auditEntry.Model, "gpt-4"), "expected requested model to end with gpt-4, got %q", auditEntry.Model)
	assert.Equal(t, workflow.ID, auditEntry.WorkflowVersionID)
	assert.Equal(t, auditlog.AuthMethodMasterKey, auditEntry.AuthMethod)

	require.NotNil(t, auditEntry.Data)
	require.NotNil(t, auditEntry.Data.WorkflowFeatures)
	assert.False(t, auditEntry.Data.WorkflowFeatures.Cache)
	assert.True(t, auditEntry.Data.WorkflowFeatures.Audit)
	assert.True(t, auditEntry.Data.WorkflowFeatures.Usage)
	assert.True(t, auditEntry.Data.WorkflowFeatures.Guardrails)
	assert.False(t, auditEntry.Data.WorkflowFeatures.Fallback)

	usageEntries := dbassert.QueryUsageByRequestID(t, fixture.PgPool, requestID)
	require.Len(t, usageEntries, 1, "expected one usage entry")
	usageEntry := usageEntries[0]

	dbassert.AssertUsageFieldCompleteness(t, usageEntry)
	dbassert.AssertUsageMatches(t, dbassert.ExpectedUsage{
		Provider:  "test",
		Endpoint:  "/v1/chat/completions",
		RequestID: requestID,
	}, usageEntry)
	dbassert.AssertUsageHasTokens(t, usageEntry)
	dbassert.AssertUsageTokensConsistent(t, usageEntry)

	assert.True(t, strings.HasSuffix(usageEntry.Model, "gpt-4"), "expected usage model to end with gpt-4, got %q", usageEntry.Model)
}

func createManagedAuthKey(t *testing.T, serverURL, masterKey string, payload map[string]any) authkeys.IssuedKey {
	t.Helper()

	resp := adminJSONRequest(t, http.MethodPost, serverURL+"/admin/api/v1/auth-keys", masterKey, payload)
	defer closeBody(resp)
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	var issued authkeys.IssuedKey
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&issued))
	require.NotEmpty(t, issued.ID)
	require.NotEmpty(t, issued.Value)
	require.NotEmpty(t, issued.UserPath)

	return issued
}

func createWorkflow(t *testing.T, serverURL, masterKey string, payload map[string]any) workflows.Version {
	t.Helper()

	resp := adminJSONRequest(t, http.MethodPost, serverURL+"/admin/api/v1/workflows", masterKey, payload)
	defer closeBody(resp)
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	var version workflows.Version
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&version))
	require.NotEmpty(t, version.ID)

	return version
}

func upsertGuardrail(t *testing.T, serverURL, masterKey, name string, payload map[string]any) guardrails.View {
	t.Helper()

	resp := adminJSONRequest(t, http.MethodPut, serverURL+"/admin/api/v1/guardrails/"+name, masterKey, payload)
	defer closeBody(resp)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var view guardrails.View
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&view))
	require.Equal(t, name, view.Name)

	return view
}

func adminJSONRequest(t *testing.T, method, url, masterKey string, payload map[string]any) *http.Response {
	t.Helper()

	body, err := json.Marshal(payload)
	require.NoError(t, err)

	req, err := http.NewRequest(method, url, bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+masterKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	return resp
}

func boolPtr(value bool) *bool {
	return &value
}
