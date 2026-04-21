//go:build integration

package integration

import (
	"io"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"gomodel/internal/core"
	"gomodel/tests/integration/dbassert"
)

func TestUsage_CapturesAllFields_PostgreSQL(t *testing.T) {
	fixture := SetupTestServer(t, TestServerConfig{
		DBType:                "postgresql",
		AuditLogEnabled:       false,
		UsageEnabled:          true,
		OnlyModelInteractions: false,
	})

	// Generate unique request ID
	requestID := uuid.New().String()

	// Make HTTP request
	payload := newChatRequest("gpt-4", "Hello, world!")
	resp := sendChatRequestWithHeaders(t, fixture.ServerURL, payload, map[string]string{
		core.RequestIDHeader: requestID,
	})
	require.Equal(t, 200, resp.StatusCode)
	closeBody(resp)

	// CRITICAL: Flush before querying DB
	fixture.FlushAndClose(t)

	// Query and assert DB state
	entries := dbassert.QueryUsageByRequestID(t, fixture.PgPool, requestID)
	require.Len(t, entries, 1, "expected exactly one usage entry")

	entry := entries[0]

	// Assert field completeness
	dbassert.AssertUsageFieldCompleteness(t, entry)

	// Assert specific values
	dbassert.AssertUsageMatches(t, dbassert.ExpectedUsage{
		Model:     "gpt-4",
		Provider:  "test",
		Endpoint:  "/v1/chat/completions",
		RequestID: requestID,
	}, entry)

	// Assert token counts are populated
	dbassert.AssertUsageHasTokens(t, entry)

	// Verify token values from mock response
	assert.Equal(t, 10, entry.InputTokens, "input tokens mismatch")
	assert.Equal(t, 8, entry.OutputTokens, "output tokens mismatch")
	assert.Equal(t, 18, entry.TotalTokens, "total tokens mismatch")

	// Assert token consistency
	dbassert.AssertUsageTokensConsistent(t, entry)
}

func TestUsage_CapturesAllFields_MongoDB(t *testing.T) {
	fixture := SetupTestServer(t, TestServerConfig{
		DBType:                "mongodb",
		AuditLogEnabled:       false,
		UsageEnabled:          true,
		OnlyModelInteractions: false,
	})

	requestID := uuid.New().String()

	payload := newChatRequest("gpt-4", "Hello, world!")
	resp := sendChatRequestWithHeaders(t, fixture.ServerURL, payload, map[string]string{
		core.RequestIDHeader: requestID,
	})
	require.Equal(t, 200, resp.StatusCode)
	closeBody(resp)

	fixture.FlushAndClose(t)

	entries := dbassert.QueryUsageByRequestIDMongo(t, fixture.MongoDb, requestID)
	require.Len(t, entries, 1, "expected exactly one usage entry")

	entry := entries[0]

	dbassert.AssertUsageFieldCompleteness(t, entry)
	dbassert.AssertUsageMatches(t, dbassert.ExpectedUsage{
		Model:     "gpt-4",
		Provider:  "test",
		Endpoint:  "/v1/chat/completions",
		RequestID: requestID,
	}, entry)
}

func TestUsage_ResponsesEndpoint_PostgreSQL(t *testing.T) {
	fixture := SetupTestServer(t, TestServerConfig{
		DBType:                "postgresql",
		AuditLogEnabled:       false,
		UsageEnabled:          true,
		OnlyModelInteractions: false,
	})

	requestID := uuid.New().String()

	payload := newResponsesRequest("gpt-4", "Hello!")
	resp := sendResponsesRequestWithHeaders(t, fixture.ServerURL, payload, map[string]string{
		core.RequestIDHeader: requestID,
	})
	require.Equal(t, 200, resp.StatusCode)
	closeBody(resp)

	fixture.FlushAndClose(t)

	entries := dbassert.QueryUsageByRequestID(t, fixture.PgPool, requestID)
	require.Len(t, entries, 1)

	dbassert.AssertUsageMatches(t, dbassert.ExpectedUsage{
		Model:     "gpt-4",
		Provider:  "test",
		Endpoint:  "/v1/responses",
		RequestID: requestID,
	}, entries[0])
}

func TestUsage_MultipleRequests_PostgreSQL(t *testing.T) {
	fixture := SetupTestServer(t, TestServerConfig{
		DBType:                "postgresql",
		AuditLogEnabled:       false,
		UsageEnabled:          true,
		OnlyModelInteractions: false,
	})

	// Clear existing entries
	dbassert.ClearUsage(t, fixture.PgPool)

	requestIDs := make([]string, 5)
	for i := 0; i < 5; i++ {
		requestIDs[i] = uuid.New().String()
		payload := newChatRequest("gpt-4", "Hello!")
		resp := sendChatRequestWithHeaders(t, fixture.ServerURL, payload, map[string]string{
			core.RequestIDHeader: requestIDs[i],
		})
		require.Equal(t, 200, resp.StatusCode)
		closeBody(resp)
	}

	fixture.FlushAndClose(t)

	// Verify each request has its own usage entry
	for _, reqID := range requestIDs {
		entries := dbassert.QueryUsageByRequestID(t, fixture.PgPool, reqID)
		require.Len(t, entries, 1, "expected one usage entry for request ID %s", reqID)
	}

	// Verify total count
	totalCount := dbassert.CountUsage(t, fixture.PgPool)
	assert.GreaterOrEqual(t, totalCount, 5, "expected at least 5 usage entries")
}

func TestUsage_TokenSummary_PostgreSQL(t *testing.T) {
	fixture := SetupTestServer(t, TestServerConfig{
		DBType:                "postgresql",
		AuditLogEnabled:       false,
		UsageEnabled:          true,
		OnlyModelInteractions: false,
	})

	// Clear existing entries
	dbassert.ClearUsage(t, fixture.PgPool)

	// Make several requests
	for i := 0; i < 3; i++ {
		payload := newChatRequest("gpt-4", "Hello!")
		resp := sendChatRequest(t, fixture.ServerURL, payload)
		require.Equal(t, 200, resp.StatusCode)
		closeBody(resp)
	}

	fixture.FlushAndClose(t)

	// Get token summary
	summary := dbassert.SumTokensByModel(t, fixture.PgPool)

	// Verify gpt-4 tokens (3 requests * 18 total tokens each = 54 total)
	gpt4Summary, ok := summary["gpt-4"]
	require.True(t, ok, "expected summary for gpt-4 model")

	assert.Equal(t, int64(3), gpt4Summary.RequestCount, "expected 3 requests")
	assert.Equal(t, int64(30), gpt4Summary.InputTokens, "expected 30 input tokens (3 * 10)")
	assert.Equal(t, int64(24), gpt4Summary.OutputTokens, "expected 24 output tokens (3 * 8)")
	assert.Equal(t, int64(54), gpt4Summary.TotalTokens, "expected 54 total tokens (3 * 18)")
}

func TestUsage_ProviderID_PostgreSQL(t *testing.T) {
	fixture := SetupTestServer(t, TestServerConfig{
		DBType:                "postgresql",
		AuditLogEnabled:       false,
		UsageEnabled:          true,
		OnlyModelInteractions: false,
	})

	requestID := uuid.New().String()

	payload := newChatRequest("gpt-4", "Hello!")
	resp := sendChatRequestWithHeaders(t, fixture.ServerURL, payload, map[string]string{
		core.RequestIDHeader: requestID,
	})
	require.Equal(t, 200, resp.StatusCode)
	closeBody(resp)

	fixture.FlushAndClose(t)

	entries := dbassert.QueryUsageByRequestID(t, fixture.PgPool, requestID)
	require.Len(t, entries, 1)

	// The mock LLM returns "chatcmpl-test123" as the response ID
	assert.Equal(t, "chatcmpl-test123", entries[0].ProviderID, "expected provider ID from mock response")
}

func TestUsage_BothAuditAndUsage_PostgreSQL(t *testing.T) {
	fixture := SetupTestServer(t, TestServerConfig{
		DBType:                "postgresql",
		AuditLogEnabled:       true,
		UsageEnabled:          true,
		LogBodies:             true,
		OnlyModelInteractions: false,
	})

	requestID := uuid.New().String()

	payload := newChatRequest("gpt-4", "Hello!")
	resp := sendChatRequestWithHeaders(t, fixture.ServerURL, payload, map[string]string{
		core.RequestIDHeader: requestID,
	})
	require.Equal(t, 200, resp.StatusCode)
	closeBody(resp)

	fixture.FlushAndClose(t)

	// Both tables should have entries
	auditEntries := dbassert.QueryAuditLogsByRequestID(t, fixture.PgPool, requestID)
	require.Len(t, auditEntries, 1, "expected audit log entry")

	usageEntries := dbassert.QueryUsageByRequestID(t, fixture.PgPool, requestID)
	require.Len(t, usageEntries, 1, "expected usage entry")

	// They should share the same request ID
	assert.Equal(t, requestID, auditEntries[0].RequestID)
	assert.Equal(t, requestID, usageEntries[0].RequestID)
}

func TestUsage_StreamingChatCompletion_PostgreSQL(t *testing.T) {
	fixture := SetupTestServer(t, TestServerConfig{
		DBType:                "postgresql",
		AuditLogEnabled:       false,
		UsageEnabled:          true,
		OnlyModelInteractions: false,
	})

	requestID := uuid.New().String()

	// Make streaming request
	payload := newStreamingChatRequest("gpt-4", "Hello, world!")
	resp := sendChatRequestWithHeaders(t, fixture.ServerURL, payload, map[string]string{
		core.RequestIDHeader: requestID,
	})
	require.Equal(t, 200, resp.StatusCode)
	assert.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))

	// Read and close the stream to ensure it completes
	_, _ = io.ReadAll(resp.Body)
	closeBody(resp)

	// CRITICAL: Flush before querying DB
	fixture.FlushAndClose(t)

	// Query and assert DB state
	entries := dbassert.QueryUsageByRequestID(t, fixture.PgPool, requestID)
	require.Len(t, entries, 1, "expected exactly one usage entry for streaming request")

	entry := entries[0]

	// Assert field completeness
	dbassert.AssertUsageFieldCompleteness(t, entry)

	// Assert specific values
	dbassert.AssertUsageMatches(t, dbassert.ExpectedUsage{
		Model:     "gpt-4",
		Provider:  "test",
		Endpoint:  "/v1/chat/completions",
		RequestID: requestID,
	}, entry)

	// Assert token counts are populated from streaming response
	dbassert.AssertUsageHasTokens(t, entry)

	// Verify token values from mock streaming response
	assert.Equal(t, 10, entry.InputTokens, "input tokens mismatch")
	assert.Equal(t, 8, entry.OutputTokens, "output tokens mismatch")
	assert.Equal(t, 18, entry.TotalTokens, "total tokens mismatch")
}

func TestUsage_StreamingChatCompletion_MongoDB(t *testing.T) {
	fixture := SetupTestServer(t, TestServerConfig{
		DBType:                "mongodb",
		AuditLogEnabled:       false,
		UsageEnabled:          true,
		OnlyModelInteractions: false,
	})

	requestID := uuid.New().String()

	// Make streaming request
	payload := newStreamingChatRequest("gpt-4", "Hello, world!")
	resp := sendChatRequestWithHeaders(t, fixture.ServerURL, payload, map[string]string{
		core.RequestIDHeader: requestID,
	})
	require.Equal(t, 200, resp.StatusCode)
	assert.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))

	// Read and close the stream
	_, _ = io.ReadAll(resp.Body)
	closeBody(resp)

	fixture.FlushAndClose(t)

	entries := dbassert.QueryUsageByRequestIDMongo(t, fixture.MongoDb, requestID)
	require.Len(t, entries, 1, "expected exactly one usage entry for streaming request")

	entry := entries[0]

	dbassert.AssertUsageFieldCompleteness(t, entry)
	dbassert.AssertUsageMatches(t, dbassert.ExpectedUsage{
		Model:     "gpt-4",
		Provider:  "test",
		Endpoint:  "/v1/chat/completions",
		RequestID: requestID,
	}, entry)
}

func TestUsage_StreamingResponses_PostgreSQL(t *testing.T) {
	fixture := SetupTestServer(t, TestServerConfig{
		DBType:                "postgresql",
		AuditLogEnabled:       false,
		UsageEnabled:          true,
		OnlyModelInteractions: false,
	})

	requestID := uuid.New().String()

	// Make streaming responses request
	payload := newStreamingResponsesRequest("gpt-4", "Hello!")
	resp := sendResponsesRequestWithHeaders(t, fixture.ServerURL, payload, map[string]string{
		core.RequestIDHeader: requestID,
	})
	require.Equal(t, 200, resp.StatusCode)
	assert.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))

	// Read and close the stream
	_, _ = io.ReadAll(resp.Body)
	closeBody(resp)

	fixture.FlushAndClose(t)

	entries := dbassert.QueryUsageByRequestID(t, fixture.PgPool, requestID)
	require.Len(t, entries, 1)

	dbassert.AssertUsageMatches(t, dbassert.ExpectedUsage{
		Model:     "gpt-4",
		Provider:  "test",
		Endpoint:  "/v1/responses",
		RequestID: requestID,
	}, entries[0])
}

func TestUsage_StreamingBothAuditAndUsage_PostgreSQL(t *testing.T) {
	fixture := SetupTestServer(t, TestServerConfig{
		DBType:                "postgresql",
		AuditLogEnabled:       true,
		UsageEnabled:          true,
		LogBodies:             true,
		OnlyModelInteractions: false,
	})

	requestID := uuid.New().String()

	// Make streaming request
	payload := newStreamingChatRequest("gpt-4", "Hello!")
	resp := sendChatRequestWithHeaders(t, fixture.ServerURL, payload, map[string]string{
		core.RequestIDHeader: requestID,
	})
	require.Equal(t, 200, resp.StatusCode)

	// Read and close the stream
	_, _ = io.ReadAll(resp.Body)
	closeBody(resp)

	fixture.FlushAndClose(t)

	// Both tables should have entries for streaming request
	auditEntries := dbassert.QueryAuditLogsByRequestID(t, fixture.PgPool, requestID)
	require.Len(t, auditEntries, 1, "expected audit log entry for streaming")

	usageEntries := dbassert.QueryUsageByRequestID(t, fixture.PgPool, requestID)
	require.Len(t, usageEntries, 1, "expected usage entry for streaming")

	// They should share the same request ID
	assert.Equal(t, requestID, auditEntries[0].RequestID)
	assert.Equal(t, requestID, usageEntries[0].RequestID)
}
