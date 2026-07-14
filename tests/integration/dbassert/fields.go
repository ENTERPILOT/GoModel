//go:build integration

package dbassert

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/auditlog"
)

// RequiredAuditLogFields are fields that must always be populated in an audit log entry.
var RequiredAuditLogFields = []string{
	"ID",
	"Timestamp",
	"StatusCode",
	"Method",
	"Path",
}

// RequiredUsageFields are fields that must always be populated in a usage entry.
var RequiredUsageFields = []string{
	"ID",
	"RequestID",
	"Timestamp",
	"Model",
	"Provider",
	"Endpoint",
}

// ExpectedAuditLog contains expected values for audit log assertions.
// Zero values are not checked, allowing partial matching.
type ExpectedAuditLog struct {
	Model      string
	Provider   string
	StatusCode int
	Method     string
	Path       string
	Stream     bool
	ErrorType  string
	RequestID  string
}

// ExpectedUsage contains expected values for usage assertions.
// Zero values are not checked, allowing partial matching.
type ExpectedUsage struct {
	Model        string
	Provider     string
	Endpoint     string
	InputTokens  int
	OutputTokens int
	TotalTokens  int
	RequestID    string
}

// AssertAuditLogFieldCompleteness verifies that all required fields are populated.
func AssertAuditLogFieldCompleteness(t *testing.T, entry AuditLogEntry) {
	t.Helper()

	assert.NotEmpty(t, entry.ID, "audit log ID should not be empty")
	assert.False(t, entry.Timestamp.IsZero(), "audit log timestamp should not be zero")
	assert.NotZero(t, entry.StatusCode, "audit log status code should not be zero")
	assert.NotEmpty(t, entry.Method, "audit log method should not be empty")
	assert.NotEmpty(t, entry.Path, "audit log path should not be empty")
}

// AssertUsageFieldCompleteness verifies that all required fields are populated.
func AssertUsageFieldCompleteness(t *testing.T, entry UsageEntry) {
	t.Helper()

	assert.NotEmpty(t, entry.ID, "usage ID should not be empty")
	assert.NotEmpty(t, entry.RequestID, "usage request ID should not be empty")
	assert.False(t, entry.Timestamp.IsZero(), "usage timestamp should not be zero")
	assert.NotEmpty(t, entry.Model, "usage model should not be empty")
	assert.NotEmpty(t, entry.Provider, "usage provider should not be empty")
	assert.NotEmpty(t, entry.Endpoint, "usage endpoint should not be empty")
}

// AssertAuditLogMatches verifies that the actual entry matches expected values.
// Only non-zero expected values are checked.
func AssertAuditLogMatches(t *testing.T, expected ExpectedAuditLog, actual AuditLogEntry) {
	t.Helper()

	if expected.Model != "" {
		assert.Equal(t, expected.Model, actual.Model, "model mismatch")
	}
	if expected.Provider != "" {
		assert.Equal(t, expected.Provider, actual.Provider, "provider mismatch")
	}
	if expected.StatusCode != 0 {
		assert.Equal(t, expected.StatusCode, actual.StatusCode, "status code mismatch")
	}
	if expected.Method != "" {
		assert.Equal(t, expected.Method, actual.Method, "method mismatch")
	}
	if expected.Path != "" {
		assert.Equal(t, expected.Path, actual.Path, "path mismatch")
	}
	if expected.Stream {
		assert.True(t, actual.Stream, "expected stream to be true")
	}
	if expected.ErrorType != "" {
		assert.Equal(t, expected.ErrorType, actual.ErrorType, "error type mismatch")
	}
	if expected.RequestID != "" {
		assert.Equal(t, expected.RequestID, actual.RequestID, "request ID mismatch")
	}
}

// AssertUsageMatches verifies that the actual entry matches expected values.
// Only non-zero expected values are checked.
func AssertUsageMatches(t *testing.T, expected ExpectedUsage, actual UsageEntry) {
	t.Helper()

	if expected.Model != "" {
		assert.Equal(t, expected.Model, actual.Model, "model mismatch")
	}
	if expected.Provider != "" {
		assert.Equal(t, expected.Provider, actual.Provider, "provider mismatch")
	}
	if expected.Endpoint != "" {
		assert.Equal(t, expected.Endpoint, actual.Endpoint, "endpoint mismatch")
	}
	if expected.InputTokens != 0 {
		assert.Equal(t, expected.InputTokens, actual.InputTokens, "input tokens mismatch")
	}
	if expected.OutputTokens != 0 {
		assert.Equal(t, expected.OutputTokens, actual.OutputTokens, "output tokens mismatch")
	}
	if expected.TotalTokens != 0 {
		assert.Equal(t, expected.TotalTokens, actual.TotalTokens, "total tokens mismatch")
	}
	if expected.RequestID != "" {
		assert.Equal(t, expected.RequestID, actual.RequestID, "request ID mismatch")
	}
}

// AssertAuditLogHasData verifies that the audit log entry has associated data.
func AssertAuditLogHasData(t *testing.T, entry AuditLogEntry) {
	t.Helper()
	assert.NotNil(t, entry.Data, "audit log should have data field populated")
}

// AssertAuditLogHasBody verifies that request and/or response bodies are logged.
func AssertAuditLogHasBody(t *testing.T, entry AuditLogEntry, expectRequest, expectResponse bool) {
	t.Helper()
	require.NotNil(t, entry.Data, "audit log data should not be nil")

	if expectRequest {
		assert.NotNil(t, entry.Data.RequestBody, "expected request body to be logged")
	}
	if expectResponse {
		assert.NotNil(t, entry.Data.ResponseBody, "expected response body to be logged")
	}
}

// AssertAuditLogHasHeaders verifies that headers are logged.
func AssertAuditLogHasHeaders(t *testing.T, entry AuditLogEntry, expectRequest, expectResponse bool) {
	t.Helper()
	require.NotNil(t, entry.Data, "audit log data should not be nil")

	if expectRequest {
		assert.NotNil(t, entry.Data.RequestHeaders, "expected request headers to be logged")
	}
	if expectResponse {
		assert.NotNil(t, entry.Data.ResponseHeaders, "expected response headers to be logged")
	}
}

// AssertUsageHasTokens verifies that token counts are populated (non-zero).
func AssertUsageHasTokens(t *testing.T, entry UsageEntry) {
	t.Helper()

	// At minimum, total tokens should be non-zero for a valid usage entry
	assert.Greater(t, entry.TotalTokens, 0, "total tokens should be greater than zero")
}

// AssertUsageTokensConsistent verifies that input + output = total tokens.
func AssertUsageTokensConsistent(t *testing.T, entry UsageEntry) {
	t.Helper()

	expectedTotal := entry.InputTokens + entry.OutputTokens
	assert.Equal(t, expectedTotal, entry.TotalTokens,
		"total tokens (%d) should equal input (%d) + output (%d)",
		entry.TotalTokens, entry.InputTokens, entry.OutputTokens)
}

// AssertNoErrorType verifies that the entry has no error type set.
func AssertNoErrorType(t *testing.T, entry AuditLogEntry) {
	t.Helper()
	assert.Empty(t, entry.ErrorType, "expected no error type, got: %s", entry.ErrorType)
}

// AssertAuditLogDurationPositive verifies that the duration is positive.
func AssertAuditLogDurationPositive(t *testing.T, entry AuditLogEntry) {
	t.Helper()
	assert.Greater(t, entry.DurationNs, int64(0), "duration should be positive")
}

// unmarshalLogData unmarshals JSON bytes to auditlog.LogData.
func unmarshalLogData(t *testing.T, data []byte) *auditlog.LogData {
	t.Helper()
	var logData auditlog.LogData
	err := json.Unmarshal(data, &logData)
	require.NoError(t, err, "failed to unmarshal log data")
	return &logData
}

// unmarshalRawData unmarshals JSON bytes to map[string]any.
func unmarshalRawData(t *testing.T, data []byte) map[string]any {
	t.Helper()
	var rawData map[string]any
	err := json.Unmarshal(data, &rawData)
	require.NoError(t, err, "failed to unmarshal raw data")
	return rawData
}
