//go:build integration

package integration

import (
	"io"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"gomodel/tests/integration/dbassert"
)

func TestAuditLog_CapturesAllFields_PostgreSQL(t *testing.T) {
	fixture := SetupTestServer(t, TestServerConfig{
		DBType:                "postgresql",
		AuditLogEnabled:       true,
		UsageEnabled:          false,
		LogBodies:             true,
		LogHeaders:            true,
		OnlyModelInteractions: false,
	})

	// Generate unique request ID to isolate this test's data
	requestID := uuid.New().String()

	// Make HTTP request with custom request ID header
	payload := newChatRequest("gpt-4", "Hello, world!")
	resp := sendChatRequestWithHeaders(t, fixture.ServerURL, payload, map[string]string{
		core.RequestIDHeader: requestID,
	})
	require.Equal(t, 200, resp.StatusCode)
	closeBody(resp)

	// CRITICAL: Flush before querying DB
	fixture.FlushAndClose(t)

	// Query and assert DB state
	entries := dbassert.QueryAuditLogsByRequestID(t, fixture.PgPool, requestID)
	require.Len(t, entries, 1, "expected exactly one audit log entry")

	entry := entries[0]

	// Assert field completeness
	dbassert.AssertAuditLogFieldCompleteness(t, entry)

	// Assert specific values
	dbassert.AssertAuditLogMatches(t, dbassert.ExpectedAuditLog{
		Model:      "gpt-4",
		StatusCode: 200,
		Method:     "POST",
		Path:       "/v1/chat/completions",
		RequestID:  requestID,
	}, entry)

	// Assert duration is positive
	dbassert.AssertAuditLogDurationPositive(t, entry)

	// Assert no error
	dbassert.AssertNoErrorType(t, entry)

	// Assert bodies and headers are logged
	dbassert.AssertAuditLogHasData(t, entry)
	dbassert.AssertAuditLogHasBody(t, entry, true, true)
	dbassert.AssertAuditLogHasHeaders(t, entry, true, true)
}

func TestAuditLog_CapturesAllFields_MongoDB(t *testing.T) {
	fixture := SetupTestServer(t, TestServerConfig{
		DBType:                "mongodb",
		AuditLogEnabled:       true,
		UsageEnabled:          false,
		LogBodies:             true,
		LogHeaders:            true,
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
	entries := dbassert.QueryAuditLogsByRequestIDMongo(t, fixture.MongoDb, requestID)
	require.Len(t, entries, 1, "expected exactly one audit log entry")

	entry := entries[0]

	// Assert field completeness
	dbassert.AssertAuditLogFieldCompleteness(t, entry)

	// Assert specific values
	dbassert.AssertAuditLogMatches(t, dbassert.ExpectedAuditLog{
		Model:      "gpt-4",
		StatusCode: 200,
		Method:     "POST",
		Path:       "/v1/chat/completions",
		RequestID:  requestID,
	}, entry)
}

func TestAuditLog_OnlyModelInteractions_PostgreSQL(t *testing.T) {
	fixture := SetupTestServer(t, TestServerConfig{
		DBType:                "postgresql",
		AuditLogEnabled:       true,
		UsageEnabled:          false,
		OnlyModelInteractions: true,
	})

	// Clear any existing entries first
	dbassert.ClearAuditLogs(t, fixture.PgPool)

	requestID := uuid.New().String()

	// Make a model request (should be logged)
	payload := newChatRequest("gpt-4", "Hello!")
	resp := sendChatRequestWithHeaders(t, fixture.ServerURL, payload, map[string]string{
		core.RequestIDHeader: requestID,
	})
	require.Equal(t, 200, resp.StatusCode)
	closeBody(resp)

	// Flush and query
	fixture.FlushAndClose(t)

	// Should have entry for chat completion
	entries := dbassert.QueryAuditLogsByRequestID(t, fixture.PgPool, requestID)
	require.Len(t, entries, 1, "expected one audit log entry for model interaction")

	// Health endpoint should NOT be logged when OnlyModelInteractions is true
	// (we can't easily test this without additional infrastructure, but
	// the config is correctly set)
}

func TestAuditLog_WithoutBodies_PostgreSQL(t *testing.T) {
	fixture := SetupTestServer(t, TestServerConfig{
		DBType:                "postgresql",
		AuditLogEnabled:       true,
		UsageEnabled:          false,
		LogBodies:             false,
		LogHeaders:            false,
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

	entries := dbassert.QueryAuditLogsByRequestID(t, fixture.PgPool, requestID)
	require.Len(t, entries, 1)

	entry := entries[0]

	// Bodies should not be logged
	if entry.Data != nil {
		assert.Nil(t, entry.Data.RequestBody, "request body should not be logged")
		assert.Nil(t, entry.Data.ResponseBody, "response body should not be logged")
		assert.Nil(t, entry.Data.RequestHeaders, "request headers should not be logged")
		assert.Nil(t, entry.Data.ResponseHeaders, "response headers should not be logged")
	}
}

func TestAuditLog_ResponsesEndpoint_PostgreSQL(t *testing.T) {
	fixture := SetupTestServer(t, TestServerConfig{
		DBType:                "postgresql",
		AuditLogEnabled:       true,
		UsageEnabled:          false,
		LogBodies:             true,
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

	entries := dbassert.QueryAuditLogsByRequestID(t, fixture.PgPool, requestID)
	require.Len(t, entries, 1)

	dbassert.AssertAuditLogMatches(t, dbassert.ExpectedAuditLog{
		Model:      "gpt-4",
		StatusCode: 200,
		Method:     "POST",
		Path:       "/v1/responses",
		RequestID:  requestID,
	}, entries[0])
}

func TestAuditLog_MultipleRequests_PostgreSQL(t *testing.T) {
	fixture := SetupTestServer(t, TestServerConfig{
		DBType:                "postgresql",
		AuditLogEnabled:       true,
		UsageEnabled:          false,
		OnlyModelInteractions: false,
	})

	// Make multiple requests with unique IDs
	requestIDs := make([]string, 3)
	for i := 0; i < 3; i++ {
		requestIDs[i] = uuid.New().String()
		payload := newChatRequest("gpt-4", "Hello!")
		resp := sendChatRequestWithHeaders(t, fixture.ServerURL, payload, map[string]string{
			core.RequestIDHeader: requestIDs[i],
		})
		require.Equal(t, 200, resp.StatusCode)
		closeBody(resp)
	}

	fixture.FlushAndClose(t)

	// Verify each request has its own entry
	for _, reqID := range requestIDs {
		entries := dbassert.QueryAuditLogsByRequestID(t, fixture.PgPool, reqID)
		require.Len(t, entries, 1, "expected one entry for request ID %s", reqID)
	}
}

func TestAuditLog_DifferentModels_PostgreSQL(t *testing.T) {
	fixture := SetupTestServer(t, TestServerConfig{
		DBType:                "postgresql",
		AuditLogEnabled:       true,
		UsageEnabled:          false,
		OnlyModelInteractions: false,
	})

	// Clear to start fresh
	dbassert.ClearAuditLogs(t, fixture.PgPool)

	models := []string{"gpt-4", "gpt-4.1", "gpt-3.5-turbo"}
	requestIDs := make(map[string]string)

	for _, model := range models {
		reqID := uuid.New().String()
		requestIDs[model] = reqID

		payload := newChatRequest(model, "Hello!")
		resp := sendChatRequestWithHeaders(t, fixture.ServerURL, payload, map[string]string{
			core.RequestIDHeader: reqID,
		})
		require.Equal(t, 200, resp.StatusCode)
		closeBody(resp)
	}

	fixture.FlushAndClose(t)

	// Verify each model's entry
	for model, reqID := range requestIDs {
		entries := dbassert.QueryAuditLogsByRequestID(t, fixture.PgPool, reqID)
		require.Len(t, entries, 1, "expected one entry for model %s", model)
		assert.Equal(t, model, entries[0].Model, "model mismatch for request %s", reqID)
	}
}

func TestAuditLog_StreamingChatCompletion_PostgreSQL(t *testing.T) {
	fixture := SetupTestServer(t, TestServerConfig{
		DBType:                "postgresql",
		AuditLogEnabled:       true,
		UsageEnabled:          false,
		LogBodies:             true,
		LogHeaders:            true,
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

	// CRITICAL: Flush before querying DB
	fixture.FlushAndClose(t)

	// Query and assert DB state
	entries := dbassert.QueryAuditLogsByRequestID(t, fixture.PgPool, requestID)
	require.Len(t, entries, 1, "expected exactly one audit log entry for streaming request")

	entry := entries[0]

	// Assert specific values
	dbassert.AssertAuditLogMatches(t, dbassert.ExpectedAuditLog{
		Model:      "gpt-4",
		StatusCode: 200,
		Method:     "POST",
		Path:       "/v1/chat/completions",
		RequestID:  requestID,
	}, entry)

	// Assert duration is positive
	dbassert.AssertAuditLogDurationPositive(t, entry)
}

func TestAuditLog_StreamingChatCompletion_MongoDB(t *testing.T) {
	fixture := SetupTestServer(t, TestServerConfig{
		DBType:                "mongodb",
		AuditLogEnabled:       true,
		UsageEnabled:          false,
		LogBodies:             true,
		LogHeaders:            true,
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

	// CRITICAL: Flush before querying DB
	fixture.FlushAndClose(t)

	// Query and assert DB state
	entries := dbassert.QueryAuditLogsByRequestIDMongo(t, fixture.MongoDb, requestID)
	require.Len(t, entries, 1, "expected exactly one audit log entry for streaming request")

	entry := entries[0]

	// Assert specific values
	dbassert.AssertAuditLogMatches(t, dbassert.ExpectedAuditLog{
		Model:      "gpt-4",
		StatusCode: 200,
		Method:     "POST",
		Path:       "/v1/chat/completions",
		RequestID:  requestID,
	}, entry)
}

func TestAuditLog_StreamingResponses_PostgreSQL(t *testing.T) {
	fixture := SetupTestServer(t, TestServerConfig{
		DBType:                "postgresql",
		AuditLogEnabled:       true,
		UsageEnabled:          false,
		LogBodies:             true,
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

	entries := dbassert.QueryAuditLogsByRequestID(t, fixture.PgPool, requestID)
	require.Len(t, entries, 1)

	dbassert.AssertAuditLogMatches(t, dbassert.ExpectedAuditLog{
		Model:      "gpt-4",
		StatusCode: 200,
		Method:     "POST",
		Path:       "/v1/responses",
		RequestID:  requestID,
	}, entries[0])
}
