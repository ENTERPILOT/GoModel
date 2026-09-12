package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/auditlog"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/echotest"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/usage"
)

// mockUsageReader implements usage.UsageReader for testing.
type mockUsageReader struct {
	summary              *usage.UsageSummary
	daily                []usage.DailyUsage
	modelUsage           []usage.ModelUsage
	userPathUsage        []usage.UserPathUsage
	labelUsage           []usage.LabelUsage
	sessionUsage         *usage.SessionUsageResult
	usageLog             *usage.UsageLogResult
	usageByRequestID     map[string][]usage.UsageLogEntry
	cacheOverview        *usage.CacheOverview
	throughput           *usage.TokenThroughput
	lastUsageLog         usage.UsageLogParams
	lastSessionUsage     usage.SessionUsageParams
	lastRequestIDs       []string
	lastCacheOverview    usage.UsageQueryParams
	lastThroughputGran   usage.ThroughputGranularity
	lastThroughputEnd    time.Time
	lastThroughputOffset int64
	summaryErr           error
	dailyErr             error
	modelUsageErr        error
	userPathUsageErr     error
	labelUsageErr        error
	sessionUsageErr      error
	usageLogErr          error
	usageByRequestErr    error
	cacheErr             error
	throughputErr        error
}

type mockAuditReader struct {
	logResult           *auditlog.LogListResult
	logErr              error
	lastQuery           auditlog.LogQueryParams
	logByID             *auditlog.LogEntry
	logByIDErr          error
	conversationResult  *auditlog.ConversationResult
	conversationErr     error
	lastConversationID  string
	lastConversationLim int
	statsResult         *auditlog.RequestStats
	statsErr            error
	lastStatsParams     auditlog.RequestStatsParams
	sessionsResult      *auditlog.SessionListResult
	sessionsErr         error
}

type mockRuntimeRefresher struct {
	report RuntimeRefreshReport
	err    error
	calls  int
}

func (m *mockRuntimeRefresher) RefreshRuntime(_ context.Context) (RuntimeRefreshReport, error) {
	m.calls++
	return m.report, m.err
}

func (m *mockUsageReader) GetSummary(_ context.Context, _ usage.UsageQueryParams) (*usage.UsageSummary, error) {
	if m.summaryErr != nil {
		return nil, m.summaryErr
	}
	return m.summary, nil
}

func (m *mockUsageReader) GetDailyUsage(_ context.Context, _ usage.UsageQueryParams) ([]usage.DailyUsage, error) {
	if m.dailyErr != nil {
		return nil, m.dailyErr
	}
	return m.daily, nil
}

func (m *mockUsageReader) GetUsageByModel(_ context.Context, _ usage.UsageQueryParams) ([]usage.ModelUsage, error) {
	if m.modelUsageErr != nil {
		return nil, m.modelUsageErr
	}
	return m.modelUsage, nil
}

func (m *mockUsageReader) GetUsageByUserPath(_ context.Context, _ usage.UsageQueryParams) ([]usage.UserPathUsage, error) {
	if m.userPathUsageErr != nil {
		return nil, m.userPathUsageErr
	}
	return m.userPathUsage, nil
}

func (m *mockUsageReader) GetUsageByLabel(_ context.Context, _ usage.UsageQueryParams) ([]usage.LabelUsage, error) {
	if m.labelUsageErr != nil {
		return nil, m.labelUsageErr
	}
	return m.labelUsage, nil
}

func (m *mockUsageReader) GetUsageBySession(_ context.Context, params usage.SessionUsageParams) (*usage.SessionUsageResult, error) {
	m.lastSessionUsage = params
	if m.sessionUsageErr != nil {
		return nil, m.sessionUsageErr
	}
	return m.sessionUsage, nil
}

func (m *mockUsageReader) GetUsageLog(_ context.Context, params usage.UsageLogParams) (*usage.UsageLogResult, error) {
	m.lastUsageLog = params
	if m.usageLogErr != nil {
		return nil, m.usageLogErr
	}
	return m.usageLog, nil
}

func (m *mockUsageReader) GetUsageByRequestIDs(_ context.Context, requestIDs []string) (map[string][]usage.UsageLogEntry, error) {
	m.lastRequestIDs = append([]string(nil), requestIDs...)
	if m.usageByRequestErr != nil {
		return nil, m.usageByRequestErr
	}
	return m.usageByRequestID, nil
}

func (m *mockUsageReader) GetCacheOverview(_ context.Context, params usage.UsageQueryParams) (*usage.CacheOverview, error) {
	m.lastCacheOverview = params
	if m.cacheErr != nil {
		return nil, m.cacheErr
	}
	return m.cacheOverview, nil
}

func (m *mockUsageReader) GetTokenThroughput(_ context.Context, gran usage.ThroughputGranularity, end time.Time, offset int64) (*usage.TokenThroughput, error) {
	m.lastThroughputGran = gran
	m.lastThroughputEnd = end
	m.lastThroughputOffset = offset
	if m.throughputErr != nil {
		return nil, m.throughputErr
	}
	return m.throughput, nil
}

func (m *mockAuditReader) GetLogs(_ context.Context, params auditlog.LogQueryParams) (*auditlog.LogListResult, error) {
	m.lastQuery = params
	if m.logErr != nil {
		return nil, m.logErr
	}
	return m.logResult, nil
}

func (m *mockAuditReader) GetSessions(_ context.Context, params auditlog.LogQueryParams) (*auditlog.SessionListResult, error) {
	m.lastQuery = params
	if m.sessionsErr != nil {
		return nil, m.sessionsErr
	}
	return m.sessionsResult, nil
}

func (m *mockAuditReader) GetLogByID(_ context.Context, _ string) (*auditlog.LogEntry, error) {
	if m.logByIDErr != nil {
		return nil, m.logByIDErr
	}
	return m.logByID, nil
}

func (m *mockAuditReader) GetInteractionParent(_ context.Context, _ string) (*auditlog.InteractionParent, error) {
	if m.logByIDErr != nil {
		return nil, m.logByIDErr
	}
	if m.logByID == nil {
		return nil, nil
	}
	return &auditlog.InteractionParent{UserPath: m.logByID.UserPath, SessionID: m.logByID.SessionID}, nil
}

func (m *mockAuditReader) GetRequestStats(_ context.Context, params auditlog.RequestStatsParams) (*auditlog.RequestStats, error) {
	m.lastStatsParams = params
	if m.statsErr != nil {
		return nil, m.statsErr
	}
	return m.statsResult, nil
}

func (m *mockAuditReader) GetConversation(_ context.Context, logID string, limit int) (*auditlog.ConversationResult, error) {
	m.lastConversationID = logID
	m.lastConversationLim = limit
	if m.conversationErr != nil {
		return nil, m.conversationErr
	}
	return m.conversationResult, nil
}

// handlerMockProvider implements core.Provider for ListModels registry testing.
type handlerMockProvider struct {
	models *core.ModelsResponse
	err    error
}

func (m *handlerMockProvider) ChatCompletion(_ context.Context, _ *core.ChatRequest) (*core.ChatResponse, error) {
	return nil, nil
}
func (m *handlerMockProvider) StreamChatCompletion(_ context.Context, _ *core.ChatRequest) (io.ReadCloser, error) {
	return nil, nil
}
func (m *handlerMockProvider) ListModels(_ context.Context) (*core.ModelsResponse, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.models, nil
}
func (m *handlerMockProvider) Responses(_ context.Context, _ *core.ResponsesRequest) (*core.ResponsesResponse, error) {
	return nil, nil
}
func (m *handlerMockProvider) StreamResponses(_ context.Context, _ *core.ResponsesRequest) (io.ReadCloser, error) {
	return nil, nil
}

func (m *handlerMockProvider) Embeddings(_ context.Context, _ *core.EmbeddingRequest) (*core.EmbeddingResponse, error) {
	return nil, core.NewInvalidRequestError("not supported", nil)
}

// decodeErrorObject returns the "error" object of an OpenAI-style error body.
func decodeErrorObject(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	body := echotest.Decode[map[string]any](t, rec)
	errorBody, ok := body["error"].(map[string]any)
	require.True(t, ok, "expected error payload, got %v", body)
	return errorBody
}

// --- UsageSummary handler tests ---

func TestUsageSummary_NilReader(t *testing.T) {
	h := NewHandler(nil, nil)
	c, rec := echotest.Get(t, "/admin/usage/summary")
	require.NoError(t, h.UsageSummary(c))
	assert.Equal(t, http.StatusOK, rec.Code)

	summary := echotest.Decode[usage.UsageSummary](t, rec)
	assert.Equal(t, 0, summary.TotalRequests)
	assert.Equal(t, int64(0), summary.TotalInput)
	assert.Equal(t, int64(0), summary.TotalOutput)
	assert.Equal(t, int64(0), summary.TotalTokens)
}

func TestUsageSummary_Success(t *testing.T) {
	reader := &mockUsageReader{
		summary: &usage.UsageSummary{
			TotalRequests: 42,
			TotalInput:    1000,
			TotalOutput:   500,
			TotalTokens:   1500,
		},
	}
	h := NewHandler(reader, nil)
	c, rec := echotest.Get(t, "/admin/usage/summary?days=30")
	require.NoError(t, h.UsageSummary(c))
	assert.Equal(t, http.StatusOK, rec.Code)

	summary := echotest.Decode[usage.UsageSummary](t, rec)
	assert.Equal(t, 42, summary.TotalRequests)
	assert.Equal(t, int64(1500), summary.TotalTokens)
}

func TestUsageSummary_IncludesCacheSplitFields(t *testing.T) {
	reader := &mockUsageReader{
		summary: &usage.UsageSummary{
			TotalRequests:         10,
			TotalInput:            1000,
			UncachedInputTokens:   600,
			CachedInputTokens:     350,
			CacheWriteInputTokens: 50,
		},
	}
	h := NewHandler(reader, nil)
	c, rec := echotest.Get(t, "/admin/usage/summary?days=30")
	require.NoError(t, h.UsageSummary(c))
	assert.Equal(t, http.StatusOK, rec.Code)

	body := rec.Body.String()
	for _, key := range []string{"uncached_input_tokens", "cached_input_tokens", "cache_write_input_tokens"} {
		assert.Contains(t, body, key)
	}

	summary := echotest.Decode[usage.UsageSummary](t, rec)
	assert.Equal(t, int64(600), summary.UncachedInputTokens)
	assert.Equal(t, int64(350), summary.CachedInputTokens)
	assert.Equal(t, int64(50), summary.CacheWriteInputTokens)
}

func TestUsageSummary_NilReaderZeroesCacheSplit(t *testing.T) {
	h := NewHandler(nil, nil)
	c, rec := echotest.Get(t, "/admin/usage/summary?days=30")
	require.NoError(t, h.UsageSummary(c))

	// Assert the keys are present (not just absent-as-zero) so a dropped field
	// can't masquerade as a zero value after unmarshalling.
	body := rec.Body.String()
	for _, key := range []string{"uncached_input_tokens", "cached_input_tokens", "cache_write_input_tokens"} {
		assert.Contains(t, body, key)
	}

	summary := echotest.Decode[usage.UsageSummary](t, rec)
	assert.Equal(t, int64(0), summary.UncachedInputTokens)
	assert.Equal(t, int64(0), summary.CachedInputTokens)
	assert.Equal(t, int64(0), summary.CacheWriteInputTokens)
}

func TestUsageSummary_GatewayError(t *testing.T) {
	reader := &mockUsageReader{
		summaryErr: core.NewProviderError("test", http.StatusBadGateway, "upstream failed", nil),
	}
	h := NewHandler(reader, nil)
	c, rec := echotest.Get(t, "/admin/usage/summary")
	require.NoError(t, h.UsageSummary(c))
	assert.Equal(t, http.StatusBadGateway, rec.Code)

	body := rec.Body.String()
	assert.Contains(t, body, "provider_error")
}

func TestUsageSummary_GenericError(t *testing.T) {
	reader := &mockUsageReader{
		summaryErr: errors.New("database connection lost"),
	}
	h := NewHandler(reader, nil)
	c, rec := echotest.Get(t, "/admin/usage/summary")
	require.NoError(t, h.UsageSummary(c))
	assert.Equal(t, http.StatusInternalServerError, rec.Code)

	body := rec.Body.String()
	assert.Contains(t, body, "internal_error")
	assert.NotContains(t, body, "database connection lost")
	assert.Contains(t, body, "an unexpected error occurred")
}

func TestUsageSummary_WithPersistedCosts(t *testing.T) {
	inputCost := 3.0
	outputCost := 7.5
	totalCost := 10.5

	reader := &mockUsageReader{
		summary: &usage.UsageSummary{
			TotalRequests:   10,
			TotalInput:      1_000_000,
			TotalOutput:     500_000,
			TotalTokens:     1_500_000,
			TotalInputCost:  &inputCost,
			TotalOutputCost: &outputCost,
			TotalCost:       &totalCost,
		},
	}

	h := NewHandler(reader, nil)
	c, rec := echotest.Get(t, "/admin/usage/summary?days=30")
	require.NoError(t, h.UsageSummary(c))
	assert.Equal(t, http.StatusOK, rec.Code)

	result := echotest.Decode[map[string]any](t, rec)
	cost, ok := result["total_input_cost"].(float64)
	assert.True(t, ok)
	assert.Equal(t, 3.0, cost)
	cost, ok = result["total_output_cost"].(float64)
	assert.True(t, ok)
	assert.Equal(t, 7.5, cost)
	cost, ok = result["total_cost"].(float64)
	assert.True(t, ok)
	assert.Equal(t, 10.5, cost)
}

func TestUsageSummary_NilCosts(t *testing.T) {
	reader := &mockUsageReader{
		summary: &usage.UsageSummary{
			TotalRequests: 5,
			TotalInput:    100,
			TotalOutput:   50,
			TotalTokens:   150,
		},
	}

	h := NewHandler(reader, nil)
	c, rec := echotest.Get(t, "/admin/usage/summary?days=30")
	require.NoError(t, h.UsageSummary(c))

	result := echotest.Decode[map[string]any](t, rec)

	// Cost fields should be null when reader returns nil costs
	assert.Nil(t, result["total_cost"])
	assert.Nil(t, result["total_input_cost"])
	assert.Nil(t, result["total_output_cost"])
}

// --- DailyUsage handler tests ---

func TestDailyUsage_NilReader(t *testing.T) {
	h := NewHandler(nil, nil)
	c, rec := echotest.Get(t, "/admin/usage/daily")
	require.NoError(t, h.DailyUsage(c))
	assert.Equal(t, http.StatusOK, rec.Code)

	// Should be [] not null
	assert.Equal(t, "[]\n", rec.Body.String())
}

func TestDailyUsage_Success(t *testing.T) {
	reader := &mockUsageReader{
		daily: []usage.DailyUsage{
			{Date: "2026-02-01", Requests: 10, InputTokens: 100, OutputTokens: 50, TotalTokens: 150},
			{Date: "2026-02-02", Requests: 20, InputTokens: 200, OutputTokens: 100, TotalTokens: 300},
		},
	}
	h := NewHandler(reader, nil)
	c, rec := echotest.Get(t, "/admin/usage/daily?days=7")
	require.NoError(t, h.DailyUsage(c))
	assert.Equal(t, http.StatusOK, rec.Code)

	daily := echotest.Decode[[]usage.DailyUsage](t, rec)
	assert.Len(t, daily, 2)
}

func TestDailyUsage_NilResult(t *testing.T) {
	reader := &mockUsageReader{
		daily: nil, // reader returns nil slice
	}
	h := NewHandler(reader, nil)
	c, rec := echotest.Get(t, "/admin/usage/daily")
	require.NoError(t, h.DailyUsage(c))
	assert.Equal(t, http.StatusOK, rec.Code)

	// Should be [] not null
	assert.Equal(t, "[]\n", rec.Body.String())
}

func TestDailyUsage_Error(t *testing.T) {
	reader := &mockUsageReader{
		dailyErr: core.NewRateLimitError("test", "too many requests"),
	}
	h := NewHandler(reader, nil)
	c, rec := echotest.Get(t, "/admin/usage/daily")
	require.NoError(t, h.DailyUsage(c))
	assert.Equal(t, http.StatusTooManyRequests, rec.Code)

	body := rec.Body.String()
	assert.Contains(t, body, "rate_limit_error")
}

// --- UsageByModel handler tests ---

func TestUsageByModel_NilReader(t *testing.T) {
	h := NewHandler(nil, nil)
	c, rec := echotest.Get(t, "/admin/usage/models")
	require.NoError(t, h.UsageByModel(c))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "[]\n", rec.Body.String())
}

func TestUsageByModel_Success(t *testing.T) {
	cost := 1.5
	reader := &mockUsageReader{
		modelUsage: []usage.ModelUsage{
			{Model: "gpt-4", Provider: "openai", InputTokens: 1000, OutputTokens: 500, TotalCost: &cost},
		},
	}
	h := NewHandler(reader, nil)
	c, rec := echotest.Get(t, "/admin/usage/models?days=30")
	require.NoError(t, h.UsageByModel(c))
	assert.Equal(t, http.StatusOK, rec.Code)

	models := echotest.Decode[[]usage.ModelUsage](t, rec)
	require.Len(t, models, 1)
	assert.Equal(t, "gpt-4", models[0].Model)
	require.NotNil(t, models[0].TotalCost)
	assert.Equal(t, 1.5, *models[0].TotalCost)
}

func TestUsageByModel_PreservesProviderName(t *testing.T) {
	reader := &mockUsageReader{
		modelUsage: []usage.ModelUsage{
			{Model: "gpt-4o", Provider: "openai", ProviderName: "primary-openai", InputTokens: 100, OutputTokens: 25},
		},
	}
	h := NewHandler(reader, nil)
	c, rec := echotest.Get(t, "/admin/usage/models?days=30")
	require.NoError(t, h.UsageByModel(c))
	require.Equal(t, http.StatusOK, rec.Code)

	models := echotest.Decode[[]usage.ModelUsage](t, rec)
	require.Len(t, models, 1)
	require.Equal(t, "primary-openai", models[0].ProviderName)
	require.Equal(t, "openai", models[0].Provider)
}

func TestUsageByModel_Error(t *testing.T) {
	reader := &mockUsageReader{
		modelUsageErr: errors.New("db failure"),
	}
	h := NewHandler(reader, nil)
	c, rec := echotest.Get(t, "/admin/usage/models")
	require.NoError(t, h.UsageByModel(c))
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

// --- UsageByUserPath handler tests ---

func TestUsageByUserPath_NilReader(t *testing.T) {
	h := NewHandler(nil, nil)
	c, rec := echotest.Get(t, "/admin/usage/user-paths")
	require.NoError(t, h.UsageByUserPath(c))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "[]\n", rec.Body.String())
}

func TestUsageByUserPath_Success(t *testing.T) {
	cost := 0.75
	reader := &mockUsageReader{
		userPathUsage: []usage.UserPathUsage{
			{UserPath: "/team/alpha", InputTokens: 100, OutputTokens: 50, TotalTokens: 150, TotalCost: &cost},
		},
	}
	h := NewHandler(reader, nil)
	c, rec := echotest.Get(t, "/admin/usage/user-paths?days=30")
	require.NoError(t, h.UsageByUserPath(c))
	assert.Equal(t, http.StatusOK, rec.Code)

	userPaths := echotest.Decode[[]usage.UserPathUsage](t, rec)
	require.Len(t, userPaths, 1)
	assert.Equal(t, "/team/alpha", userPaths[0].UserPath)
	assert.Equal(t, int64(150), userPaths[0].TotalTokens)
	require.NotNil(t, userPaths[0].TotalCost)
	assert.Equal(t, 0.75, *userPaths[0].TotalCost)
}

func TestUsageByUserPath_Error(t *testing.T) {
	reader := &mockUsageReader{
		userPathUsageErr: errors.New("db failure"),
	}
	h := NewHandler(reader, nil)
	c, rec := echotest.Get(t, "/admin/usage/user-paths")
	require.NoError(t, h.UsageByUserPath(c))
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

// --- UsageByLabel handler tests ---

func TestUsageByLabel_NilReader(t *testing.T) {
	h := NewHandler(nil, nil)
	c, rec := echotest.Get(t, "/admin/usage/labels")
	require.NoError(t, h.UsageByLabel(c))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "[]\n", rec.Body.String())
}

func TestUsageByLabel_Success(t *testing.T) {
	cost := 1.25
	reader := &mockUsageReader{
		labelUsage: []usage.LabelUsage{
			{Label: "team-alpha", Requests: 4, InputTokens: 100, OutputTokens: 50, TotalTokens: 150, TotalCost: &cost},
		},
	}
	h := NewHandler(reader, nil)
	c, rec := echotest.Get(t, "/admin/usage/labels?days=30")
	require.NoError(t, h.UsageByLabel(c))
	assert.Equal(t, http.StatusOK, rec.Code)

	labels := echotest.Decode[[]usage.LabelUsage](t, rec)
	require.Len(t, labels, 1)
	assert.Equal(t, "team-alpha", labels[0].Label)
	assert.Equal(t, 4, labels[0].Requests)
	require.NotNil(t, labels[0].TotalCost)
	assert.Equal(t, 1.25, *labels[0].TotalCost)
}

func TestUsageByLabel_Error(t *testing.T) {
	reader := &mockUsageReader{
		labelUsageErr: errors.New("db failure"),
	}
	h := NewHandler(reader, nil)
	c, rec := echotest.Get(t, "/admin/usage/labels")
	require.NoError(t, h.UsageByLabel(c))
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

// --- UsageLog handler tests ---

func TestUsageLog_NilReader(t *testing.T) {
	h := NewHandler(nil, nil)
	// Omit limit, as a paging client's first request may. The disabled-reader
	// path must report the default page size (not 0) so the client never resends
	// limit=0 (which 400s).
	c, rec := echotest.Get(t, "/admin/usage/log")
	require.NoError(t, h.UsageLog(c))
	assert.Equal(t, http.StatusOK, rec.Code)

	result := echotest.Decode[usage.UsageLogResult](t, rec)
	assert.Empty(t, result.Entries)
	assert.Equal(t, 0, result.Offset)
	assert.Equal(t, 50, result.Limit)
}

func TestUsageLog_Success(t *testing.T) {
	now := time.Now().UTC()
	reader := &mockUsageReader{
		usageLog: &usage.UsageLogResult{
			Entries: []usage.UsageLogEntry{
				{ID: "1", RequestID: "req-1", Model: "gpt-4", Provider: "openai", Timestamp: now, InputTokens: 100, OutputTokens: 50, TotalTokens: 150, RawData: map[string]any{"cached_tokens": float64(50)}},
				{ID: "2", RequestID: "req-2", Model: "claude-3", Provider: "anthropic", Timestamp: now, InputTokens: 200, OutputTokens: 100, TotalTokens: 300},
			},
			Total:  2,
			Limit:  50,
			Offset: 0,
		},
	}
	h := NewHandler(reader, nil)
	c, rec := echotest.Get(t, "/admin/usage/log?days=30&limit=50&offset=0")
	require.NoError(t, h.UsageLog(c))
	assert.Equal(t, http.StatusOK, rec.Code)

	result := echotest.Decode[usage.UsageLogResult](t, rec)
	require.Len(t, result.Entries, 2)
	assert.Equal(t, 2, result.Total)
	assert.Equal(t, "gpt-4", result.Entries[0].Model)
	require.NotNil(t, result.Entries[0].RawData)
	ct, ok := result.Entries[0].RawData["cached_tokens"].(float64)
	assert.True(t, ok)
	assert.Equal(t, float64(50), ct)
	assert.Nil(t, result.Entries[1].RawData)
}

func TestUsageLog_PreservesProviderName(t *testing.T) {
	now := time.Now().UTC()
	reader := &mockUsageReader{
		usageLog: &usage.UsageLogResult{
			Entries: []usage.UsageLogEntry{
				{
					ID:           "1",
					RequestID:    "req-1",
					Model:        "gpt-4o",
					Provider:     "openai",
					ProviderName: "primary-openai",
					Timestamp:    now,
					InputTokens:  100,
					TotalTokens:  100,
				},
			},
			Total: 1,
			Limit: 50,
		},
	}
	h := NewHandler(reader, nil)
	c, rec := echotest.Get(t, "/admin/usage/log?days=30")
	require.NoError(t, h.UsageLog(c))
	require.Equal(t, http.StatusOK, rec.Code)

	result := echotest.Decode[usage.UsageLogResult](t, rec)
	require.Len(t, result.Entries, 1)
	require.Equal(t, "primary-openai", result.Entries[0].ProviderName)
	require.Equal(t, "openai", result.Entries[0].Provider)
}

func TestUsageLog_Error(t *testing.T) {
	reader := &mockUsageReader{
		usageLogErr: core.NewProviderError("test", http.StatusBadGateway, "upstream failed", nil),
	}
	h := NewHandler(reader, nil)
	c, rec := echotest.Get(t, "/admin/usage/log")
	require.NoError(t, h.UsageLog(c))
	assert.Equal(t, http.StatusBadGateway, rec.Code)
}

func TestUsageLog_WithFilters(t *testing.T) {
	reader := &mockUsageReader{
		usageLog: &usage.UsageLogResult{
			Entries: []usage.UsageLogEntry{},
			Total:   0,
			Limit:   10,
			Offset:  0,
		},
	}
	h := NewHandler(reader, nil)
	c, rec := echotest.Get(t, "/admin/usage/log?model=gpt-4&provider=openai&user_path=/team&label=team-alpha&session_id=scoped-session&search=test&limit=10&offset=5")
	require.NoError(t, h.UsageLog(c))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "/team", reader.lastUsageLog.UserPath)
	assert.Equal(t, "team-alpha", reader.lastUsageLog.Label)
	assert.Equal(t, "scoped-session", reader.lastUsageLog.SessionID)
}

func TestUsageBySession_Success(t *testing.T) {
	reader := &mockUsageReader{sessionUsage: &usage.SessionUsageResult{
		Entries: []usage.SessionUsage{{
			SessionID: "scoped-session", UserPath: "/team", Requests: 2, TotalTokens: 42,
		}},
		Total: 1, Limit: 25, Offset: 10,
	}}
	h := NewHandler(reader, nil)
	c, rec := echotest.Get(t, "/admin/usage/sessions?limit=25&offset=10&session_id=scoped-session")
	require.NoError(t, h.UsageBySession(c))
	require.Equal(t, http.StatusOK, rec.Code)

	result := echotest.Decode[usage.SessionUsageResult](t, rec)
	require.Equal(t, 1, result.Total)
	require.Equal(t, 25, result.Limit)
	require.Equal(t, 10, result.Offset)
	require.Len(t, result.Entries, 1)
	require.Equal(t, "scoped-session", result.Entries[0].SessionID)
	require.Equal(t, 2, result.Entries[0].Requests)
	require.Equal(t, 25, reader.lastSessionUsage.Limit)
	require.Equal(t, 10, reader.lastSessionUsage.Offset)
	require.Equal(t, "scoped-session", reader.lastSessionUsage.SessionID)
}

func TestUsageBySessionRejectsInvalidPagination(t *testing.T) {
	for _, path := range []string{
		"/admin/usage/sessions?limit=0",
		"/admin/usage/sessions?limit=201",
		"/admin/usage/sessions?offset=-1",
	} {
		h := NewHandler(&mockUsageReader{}, nil)
		c, rec := echotest.Get(t, path)
		require.NoError(t, h.UsageBySession(c))
		require.Equal(t, http.StatusBadRequest, rec.Code, path)
	}
}

func TestUsageBySessionUsesDefaultLimit(t *testing.T) {
	reader := &mockUsageReader{sessionUsage: &usage.SessionUsageResult{Entries: []usage.SessionUsage{}}}
	h := NewHandler(reader, nil)
	c, rec := echotest.Get(t, "/admin/usage/sessions")
	require.NoError(t, h.UsageBySession(c))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, defaultSessionUsageLimit, reader.lastSessionUsage.Limit)

	result := echotest.Decode[usage.SessionUsageResult](t, rec)
	require.Equal(t, defaultSessionUsageLimit, result.Limit)
}

func TestUsageBySessionPropagatesReaderError(t *testing.T) {
	reader := &mockUsageReader{
		sessionUsageErr: core.NewProviderError("usage", http.StatusBadGateway, "reader unavailable", nil),
	}
	h := NewHandler(reader, nil)
	c, rec := echotest.Get(t, "/admin/usage/sessions")
	require.NoError(t, h.UsageBySession(c))
	require.Equal(t, http.StatusBadGateway, rec.Code)
}

// --- AuditLog handler tests ---

func TestAuditLog_NilReader(t *testing.T) {
	h := NewHandler(nil, nil)
	// Omit limit, as a paging client's first request may. The disabled-reader
	// path must report the default page size (not 0) so the client never resends
	// limit=0 (which 400s).
	c, rec := echotest.Get(t, "/admin/audit/log")
	require.NoError(t, h.AuditLog(c))
	assert.Equal(t, http.StatusOK, rec.Code)

	result := echotest.Decode[auditlog.LogListResult](t, rec)
	assert.Empty(t, result.Entries)
	assert.Equal(t, 0, result.Offset)
	assert.Equal(t, 25, result.Limit)
}

func TestAuditLog_Success(t *testing.T) {
	now := time.Now().UTC()
	reader := &mockAuditReader{
		logResult: &auditlog.LogListResult{
			Entries: []auditlog.LogEntry{
				{
					ID:             "log-1",
					Timestamp:      now,
					DurationNs:     12_000_000,
					RequestedModel: "gpt-4o",
					Provider:       "openai",
					StatusCode:     200,
					RequestID:      "req-1",
					Method:         http.MethodPost,
					Path:           "/v1/chat/completions",
					Data: &auditlog.LogData{
						RequestBody: map[string]any{
							"model": "gpt-4o",
						},
						ResponseBody: map[string]any{
							"id": "chatcmpl-1",
						},
					},
				},
			},
			Total:  1,
			Limit:  25,
			Offset: 0,
		},
	}

	h := NewHandler(nil, nil, WithAuditReader(reader))
	c, rec := echotest.Get(t, "/admin/audit/log?days=7")
	require.NoError(t, h.AuditLog(c))
	assert.Equal(t, http.StatusOK, rec.Code)

	result := echotest.Decode[auditlog.LogListResult](t, rec)
	require.Len(t, result.Entries, 1)
	assert.Equal(t, 1, result.Total)
	assert.Equal(t, "log-1", result.Entries[0].ID)

	// List entries are slim: bodies live behind /admin/audit/detail.
	require.NotNil(t, result.Entries[0].Data)
	assert.Nil(t, result.Entries[0].Data.RequestBody)
	assert.Nil(t, result.Entries[0].Data.ResponseBody)
}

func TestAuditLog_EmitsRequestedModel(t *testing.T) {
	now := time.Now().UTC()
	reader := &mockAuditReader{
		logResult: &auditlog.LogListResult{
			Entries: []auditlog.LogEntry{
				{
					ID:             "log-1",
					Timestamp:      now,
					RequestedModel: "does-not-exist-model",
					StatusCode:     http.StatusBadRequest,
					RequestID:      "req-1",
					Method:         http.MethodPost,
					Path:           "/v1/chat/completions",
					ErrorType:      string(core.ErrorTypeInvalidRequest),
				},
			},
			Total:  1,
			Limit:  25,
			Offset: 0,
		},
	}

	h := NewHandler(nil, nil, WithAuditReader(reader))
	c, rec := echotest.Get(t, "/admin/audit/log?search=req-1")
	require.NoError(t, h.AuditLog(c))
	require.Equal(t, http.StatusOK, rec.Code)

	var result struct {
		Entries []struct {
			RequestedModel string `json:"requested_model"`
		} `json:"entries"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &result))
	require.Len(t, result.Entries, 1)
	require.Equal(t, "does-not-exist-model", result.Entries[0].RequestedModel)
}

func TestAuditLog_EnrichesEntriesWithUsageSummary(t *testing.T) {
	now := time.Now().UTC()
	usageReader := &mockUsageReader{
		usageByRequestID: map[string][]usage.UsageLogEntry{
			"req-1": {
				{
					RequestID:    "req-1",
					Provider:     "openai",
					InputTokens:  200,
					OutputTokens: 40,
					RawData: map[string]any{
						"prompt_cached_tokens": 150,
					},
				},
			},
		},
	}
	auditReader := &mockAuditReader{
		logResult: &auditlog.LogListResult{
			Entries: []auditlog.LogEntry{
				{
					ID:             "log-1",
					Timestamp:      now,
					RequestedModel: "gpt-4o",
					Provider:       "openai",
					StatusCode:     200,
					RequestID:      "req-1",
					Method:         http.MethodPost,
					Path:           "/v1/chat/completions",
				},
			},
			Total:  1,
			Limit:  25,
			Offset: 0,
		},
	}

	h := NewHandler(usageReader, nil, WithAuditReader(auditReader))
	c, rec := echotest.Get(t, "/admin/audit/log?days=7")
	require.NoError(t, h.AuditLog(c))
	require.Equal(t, http.StatusOK, rec.Code)

	result := echotest.Decode[auditLogListResponse](t, rec)
	require.Len(t, result.Entries, 1)
	require.Len(t, usageReader.lastRequestIDs, 1)
	require.Equal(t, "req-1", usageReader.lastRequestIDs[0])
	require.NotNil(t, result.Entries[0].Usage)
	require.Equal(t, int64(150), result.Entries[0].Usage.CachedInputTokens)
	require.Equal(t, int64(200), result.Entries[0].Usage.InputTokens)
	require.Equal(t, int64(240), result.Entries[0].Usage.TotalTokens)
}

func TestAuditLogDetail_MissingLogID(t *testing.T) {
	h := NewHandler(nil, nil, WithAuditReader(&mockAuditReader{}))
	c, rec := echotest.Get(t, "/admin/audit/detail")
	require.NoError(t, h.AuditLogDetail(c))
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "log_id is required")
}

func TestAuditLogDetail_NilReader(t *testing.T) {
	h := NewHandler(nil, nil)
	c, rec := echotest.Get(t, "/admin/audit/detail?log_id=log-1")
	require.NoError(t, h.AuditLogDetail(c))
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	require.Contains(t, rec.Body.String(), "audit log detail is unavailable")
}

func TestAuditLogDetail_NotFound(t *testing.T) {
	h := NewHandler(nil, nil, WithAuditReader(&mockAuditReader{}))
	c, rec := echotest.Get(t, "/admin/audit/detail?log_id=missing")
	require.NoError(t, h.AuditLogDetail(c))
	require.Equal(t, http.StatusNotFound, rec.Code)
	require.Contains(t, rec.Body.String(), "audit log not found: missing")
}

func TestAuditLogDetail_PropagatesReaderError(t *testing.T) {
	h := NewHandler(nil, nil, WithAuditReader(&mockAuditReader{
		logByIDErr: core.NewProviderError("audit", http.StatusServiceUnavailable, "audit reader unavailable", nil),
	}))
	c, rec := echotest.Get(t, "/admin/audit/detail?log_id=log-1")
	require.NoError(t, h.AuditLogDetail(c))
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	require.Contains(t, rec.Body.String(), "audit reader unavailable")
}

func TestAuditLogDetail_SuccessEnrichesUsage(t *testing.T) {
	now := time.Now().UTC()
	usageReader := &mockUsageReader{
		usageByRequestID: map[string][]usage.UsageLogEntry{
			"req-1": {
				{
					RequestID:    "req-1",
					Provider:     "openai",
					InputTokens:  200,
					OutputTokens: 40,
					RawData: map[string]any{
						"prompt_cached_tokens": 150,
					},
				},
			},
		},
	}
	auditReader := &mockAuditReader{
		logByID: &auditlog.LogEntry{
			ID:             "log-1",
			Timestamp:      now,
			RequestedModel: "gpt-4o",
			Provider:       "openai",
			StatusCode:     http.StatusOK,
			RequestID:      "req-1",
			Method:         http.MethodPost,
			Path:           "/v1/chat/completions",
		},
	}
	h := NewHandler(usageReader, nil, WithAuditReader(auditReader))
	c, rec := echotest.Get(t, "/admin/audit/detail?log_id=log-1")
	require.NoError(t, h.AuditLogDetail(c))
	require.Equal(t, http.StatusOK, rec.Code)

	result := echotest.Decode[auditLogEntryResponse](t, rec)
	require.Equal(t, "log-1", result.ID)
	require.Len(t, usageReader.lastRequestIDs, 1)
	require.Equal(t, "req-1", usageReader.lastRequestIDs[0])
	require.NotNil(t, result.Usage)
	require.Equal(t, int64(150), result.Usage.CachedInputTokens)
	require.Equal(t, int64(240), result.Usage.TotalTokens)
}

func TestAuditLog_PreservesProviderName(t *testing.T) {
	now := time.Now().UTC()
	reader := &mockAuditReader{
		logResult: &auditlog.LogListResult{
			Entries: []auditlog.LogEntry{
				{
					ID:             "log-1",
					Timestamp:      now,
					RequestedModel: "smart",
					ResolvedModel:  "primary-openai/gpt-4o",
					Provider:       "openai",
					ProviderName:   "primary-openai",
					StatusCode:     200,
				},
			},
			Total: 1,
			Limit: 25,
		},
	}
	h := NewHandler(nil, nil, WithAuditReader(reader))
	c, rec := echotest.Get(t, "/admin/audit/log?days=7")
	require.NoError(t, h.AuditLog(c))
	require.Equal(t, http.StatusOK, rec.Code)

	result := echotest.Decode[auditlog.LogListResult](t, rec)
	require.Len(t, result.Entries, 1)
	require.Equal(t, "primary-openai", result.Entries[0].ProviderName)
	require.Equal(t, "openai", result.Entries[0].Provider)
}

func TestAuditLog_WithFilters(t *testing.T) {
	reader := &mockAuditReader{
		logResult: &auditlog.LogListResult{
			Entries: []auditlog.LogEntry{},
			Total:   0,
			Limit:   10,
			Offset:  0,
		},
	}

	h := NewHandler(nil, nil, WithAuditReader(reader))
	c, rec := echotest.Get(t, "/admin/audit/log?model=gpt-4&provider=openai&method=post&path=/v1/chat/completions&user_path=/team&error_type=provider_error&status_code=502&stream=true&search=timeout&limit=10&offset=5")
	require.NoError(t, h.AuditLog(c))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "gpt-4", reader.lastQuery.RequestedModel)
	assert.Equal(t, "openai", reader.lastQuery.Provider)
	assert.Equal(t, http.MethodPost, reader.lastQuery.Method)
	assert.Equal(t, "/v1/chat/completions", reader.lastQuery.Path)
	assert.Equal(t, "/team", reader.lastQuery.UserPath)
	assert.Equal(t, "provider_error", reader.lastQuery.ErrorType)
	require.NotNil(t, reader.lastQuery.StatusCode)
	assert.Equal(t, 502, *reader.lastQuery.StatusCode)
	require.NotNil(t, reader.lastQuery.Stream)
	assert.True(t, *reader.lastQuery.Stream)
	assert.Equal(t, "timeout", reader.lastQuery.Search)
	assert.Equal(t, 10, reader.lastQuery.Limit)
	assert.Equal(t, 5, reader.lastQuery.Offset)
}

func TestAuditLog_InvalidStatusCode(t *testing.T) {
	reader := &mockAuditReader{
		logResult: &auditlog.LogListResult{Entries: []auditlog.LogEntry{}},
	}
	h := NewHandler(nil, nil, WithAuditReader(reader))
	c, rec := echotest.Get(t, "/admin/audit/log?status_code=foo")
	require.NoError(t, h.AuditLog(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "invalid_request_error")
}

func TestAuditLog_InvalidStream(t *testing.T) {
	reader := &mockAuditReader{
		logResult: &auditlog.LogListResult{Entries: []auditlog.LogEntry{}},
	}
	h := NewHandler(nil, nil, WithAuditReader(reader))
	c, rec := echotest.Get(t, "/admin/audit/log?stream=maybe")
	require.NoError(t, h.AuditLog(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "invalid_request_error")
}

func TestAuditLog_InvalidLimit(t *testing.T) {
	cases := []string{"abc", "0", "-1"}
	for _, q := range cases {
		t.Run(q, func(t *testing.T) {
			reader := &mockAuditReader{
				logResult: &auditlog.LogListResult{Entries: []auditlog.LogEntry{}},
			}
			h := NewHandler(nil, nil, WithAuditReader(reader))
			c, rec := echotest.Get(t, "/admin/audit/log?limit="+q)
			require.NoError(t, h.AuditLog(c))
			assert.Equal(t, http.StatusBadRequest, rec.Code, q)
			assert.Contains(t, rec.Body.String(), "invalid_request_error")
		})
	}
}

func TestAuditLog_InvalidOffset(t *testing.T) {
	cases := []string{"abc", "-1"}
	for _, q := range cases {
		t.Run(q, func(t *testing.T) {
			reader := &mockAuditReader{
				logResult: &auditlog.LogListResult{Entries: []auditlog.LogEntry{}},
			}
			h := NewHandler(nil, nil, WithAuditReader(reader))
			c, rec := echotest.Get(t, "/admin/audit/log?offset="+q)
			require.NoError(t, h.AuditLog(c))
			assert.Equal(t, http.StatusBadRequest, rec.Code, q)
			assert.Contains(t, rec.Body.String(), "invalid_request_error")
		})
	}
}

func TestAuditLog_Error(t *testing.T) {
	reader := &mockAuditReader{
		logErr: core.NewProviderError("test", http.StatusBadGateway, "upstream failed", nil),
	}
	h := NewHandler(nil, nil, WithAuditReader(reader))
	c, rec := echotest.Get(t, "/admin/audit/log")
	require.NoError(t, h.AuditLog(c))
	assert.Equal(t, http.StatusBadGateway, rec.Code)
}

func TestAuditConversation_NilReader(t *testing.T) {
	h := NewHandler(nil, nil)
	c, rec := echotest.Get(t, "/admin/audit/conversation?log_id=log-1")
	require.NoError(t, h.AuditConversation(c))
	assert.Equal(t, http.StatusOK, rec.Code)

	result := echotest.Decode[auditlog.ConversationResult](t, rec)
	assert.Equal(t, "log-1", result.AnchorID)
	assert.Empty(t, result.Entries)
}

func TestAuditConversation_Success(t *testing.T) {
	now := time.Now().UTC()
	reader := &mockAuditReader{
		conversationResult: &auditlog.ConversationResult{
			AnchorID: "log-2",
			Entries: []auditlog.LogEntry{
				{ID: "log-1", Timestamp: now.Add(-time.Minute), Path: "/v1/responses"},
				{ID: "log-2", Timestamp: now, Path: "/v1/responses"},
			},
		},
	}
	h := NewHandler(nil, nil, WithAuditReader(reader))
	c, rec := echotest.Get(t, "/admin/audit/conversation?log_id=log-2&limit=80")
	require.NoError(t, h.AuditConversation(c))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "log-2", reader.lastConversationID)
	assert.Equal(t, 80, reader.lastConversationLim)
}

func TestAuditConversation_EnrichesEntriesWithPromptCacheUsage(t *testing.T) {
	now := time.Now().UTC()
	usageReader := &mockUsageReader{
		usageByRequestID: map[string][]usage.UsageLogEntry{
			"req-cached": {
				{
					RequestID:   "req-cached",
					InputTokens: 100,
					RawData: map[string]any{
						"prompt_cached_tokens": 25,
					},
				},
			},
		},
	}
	reader := &mockAuditReader{
		conversationResult: &auditlog.ConversationResult{
			AnchorID: "log-cached",
			Entries: []auditlog.LogEntry{
				{ID: "log-cached", RequestID: "req-cached", Timestamp: now, Path: "/v1/chat/completions"},
			},
		},
	}
	h := NewHandler(usageReader, nil, WithAuditReader(reader))
	c, rec := echotest.Get(t, "/admin/audit/conversation?log_id=log-cached")
	require.NoError(t, h.AuditConversation(c))
	require.Equal(t, http.StatusOK, rec.Code)

	result := echotest.Decode[auditConversationResponse](t, rec)
	require.Len(t, result.Entries, 1)
	require.NotNil(t, result.Entries[0].Usage)
	require.Equal(t, int64(25), result.Entries[0].Usage.CachedInputTokens)
	require.Equal(t, int64(100), result.Entries[0].Usage.EstimatedCachedCharacters)
}

func TestAuditConversation_PreservesProviderName(t *testing.T) {
	now := time.Now().UTC()
	reader := &mockAuditReader{
		conversationResult: &auditlog.ConversationResult{
			AnchorID: "log-2",
			Entries: []auditlog.LogEntry{
				{ID: "log-1", Timestamp: now.Add(-time.Minute), ResolvedModel: "primary-openai/gpt-4o", Provider: "openai", ProviderName: "primary-openai", Path: "/v1/responses"},
				{ID: "log-2", Timestamp: now, ResolvedModel: "primary-openai/gpt-4o", Provider: "openai", ProviderName: "primary-openai", Path: "/v1/responses"},
			},
		},
	}
	h := NewHandler(nil, nil, WithAuditReader(reader))
	c, rec := echotest.Get(t, "/admin/audit/conversation?log_id=log-2")
	require.NoError(t, h.AuditConversation(c))
	require.Equal(t, http.StatusOK, rec.Code)

	result := echotest.Decode[auditlog.ConversationResult](t, rec)
	require.Len(t, result.Entries, 2)
	require.Equal(t, "primary-openai", result.Entries[0].ProviderName)
	require.Equal(t, "openai", result.Entries[0].Provider)
}

func TestAuditConversation_MissingLogID(t *testing.T) {
	reader := &mockAuditReader{}
	h := NewHandler(nil, nil, WithAuditReader(reader))
	c, rec := echotest.Get(t, "/admin/audit/conversation")
	require.NoError(t, h.AuditConversation(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestAuditConversation_InvalidLimit(t *testing.T) {
	reader := &mockAuditReader{}
	h := NewHandler(nil, nil, WithAuditReader(reader))
	c, rec := echotest.Get(t, "/admin/audit/conversation?log_id=log-1&limit=bad")
	require.NoError(t, h.AuditConversation(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestAuditConversation_Error(t *testing.T) {
	reader := &mockAuditReader{
		conversationErr: core.NewProviderError("test", http.StatusBadGateway, "upstream failed", nil),
	}
	h := NewHandler(nil, nil, WithAuditReader(reader))
	c, rec := echotest.Get(t, "/admin/audit/conversation?log_id=log-1")
	require.NoError(t, h.AuditConversation(c))
	assert.Equal(t, http.StatusBadGateway, rec.Code)
}

// --- Validation-before-fast-path tests ---
//
// AuditLog, AuditConversation, and UsageLog all short-circuit to an empty
// success payload when their reader is nil. These tests assert that request-
// shape validation runs *before* that fast path, so callers get a 400 for
// missing/malformed required params regardless of whether the underlying
// reader is wired up.

func TestAuditLog_NilReaderStillValidatesParams(t *testing.T) {
	h := NewHandler(nil, nil) // no audit reader configured
	c, rec := echotest.Get(t, "/admin/audit/log?status_code=not-an-int")
	require.NoError(t, h.AuditLog(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "invalid_request_error")
}

func TestAuditConversation_NilReaderStillValidatesParams(t *testing.T) {
	h := NewHandler(nil, nil) // no audit reader configured
	c, rec := echotest.Get(t, "/admin/audit/conversation")
	require.NoError(t, h.AuditConversation(c)) // missing required log_id
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "log_id is required")
}

func TestUsageLog_NilReaderStillValidatesParams(t *testing.T) {
	h := NewHandler(nil, nil) // no usage reader configured
	c, rec := echotest.Get(t, "/admin/usage/log?start_date=not-a-date")
	require.NoError(t, h.UsageLog(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "invalid_request_error")
}

// --- ListModels handler tests ---

func TestListModels_NilRegistry(t *testing.T) {
	h := NewHandler(nil, nil)
	c, rec := echotest.Get(t, "/admin/models")
	require.NoError(t, h.ListModels(c))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "[]\n", rec.Body.String())
}

func TestListModels_WithModels(t *testing.T) {
	registry := providers.NewModelRegistry()
	mock := &handlerMockProvider{
		models: &core.ModelsResponse{
			Object: "list",
			Data: []core.Model{
				{ID: "gpt-4", Object: "model", OwnedBy: "openai"},
				{ID: "claude-3", Object: "model", OwnedBy: "anthropic"},
			},
		},
	}
	registry.RegisterProviderWithType(mock, "test")
	err := registry.Initialize(context.Background())
	require.NoError(t, err)

	h := NewHandler(nil, registry)
	c, rec := echotest.Get(t, "/admin/models")
	require.NoError(t, h.ListModels(c))
	assert.Equal(t, http.StatusOK, rec.Code)

	models := echotest.Decode[[]providers.ModelWithProvider](t, rec)
	require.Len(t, models, 2)

	// Should be sorted by model ID
	assert.Equal(t, "claude-3", models[0].Model.ID)
	assert.Equal(t, "gpt-4", models[1].Model.ID)
	assert.Equal(t, "test", models[0].ProviderType)
}

func TestListModels_EmptyRegistry(t *testing.T) {
	// A registry with no providers initialized — ListModelsWithProvider returns nil
	registry := providers.NewModelRegistry()

	h := NewHandler(nil, registry)
	c, rec := echotest.Get(t, "/admin/models")
	require.NoError(t, h.ListModels(c))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "[]\n", rec.Body.String())
}

// --- ListModels with category filter tests ---

func TestListModels_WithCategoryFilter(t *testing.T) {
	registry := providers.NewModelRegistry()
	mock := &handlerMockProvider{
		models: &core.ModelsResponse{
			Object: "list",
			Data: []core.Model{
				{
					ID: "gpt-4o", Object: "model", OwnedBy: "openai",
					Metadata: &core.ModelMetadata{
						Modes:      []string{"chat"},
						Categories: []core.ModelCategory{core.CategoryTextGeneration},
					},
				},
				{
					ID: "text-embedding-3-small", Object: "model", OwnedBy: "openai",
					Metadata: &core.ModelMetadata{
						Modes:      []string{"embedding"},
						Categories: []core.ModelCategory{core.CategoryEmbedding},
					},
				},
				{
					ID: "dall-e-3", Object: "model", OwnedBy: "openai",
					Metadata: &core.ModelMetadata{
						Modes:      []string{"image_generation"},
						Categories: []core.ModelCategory{core.CategoryImage},
					},
				},
			},
		},
	}
	registry.RegisterProviderWithType(mock, "openai")
	err := registry.Initialize(context.Background())
	require.NoError(t, err)

	h := NewHandler(nil, registry)

	t.Run("FilterTextGeneration", func(t *testing.T) {
		c, rec := echotest.Get(t, "/admin/models?category=text_generation")
		require.NoError(t, h.ListModels(c))
		assert.Equal(t, http.StatusOK, rec.Code)

		models := echotest.Decode[[]providers.ModelWithProvider](t, rec)
		require.Len(t, models, 1)
		assert.Equal(t, "gpt-4o", models[0].Model.ID)
	})

	t.Run("FilterAll", func(t *testing.T) {
		c, rec := echotest.Get(t, "/admin/models?category=all")
		require.NoError(t, h.ListModels(c))

		models := echotest.Decode[[]providers.ModelWithProvider](t, rec)
		assert.Len(t, models, 3)
	})

	t.Run("NoFilter", func(t *testing.T) {
		c, rec := echotest.Get(t, "/admin/models")
		require.NoError(t, h.ListModels(c))

		models := echotest.Decode[[]providers.ModelWithProvider](t, rec)
		assert.Len(t, models, 3)
	})
}

func TestListModels_InvalidCategory(t *testing.T) {
	registry := providers.NewModelRegistry()
	mock := &handlerMockProvider{
		models: &core.ModelsResponse{
			Object: "list",
			Data: []core.Model{
				{ID: "gpt-4o", Object: "model", OwnedBy: "openai"},
			},
		},
	}
	registry.RegisterProviderWithType(mock, "openai")
	err := registry.Initialize(context.Background())
	require.NoError(t, err)

	h := NewHandler(nil, registry)
	c, rec := echotest.Get(t, "/admin/models?category=bogus_category")
	require.NoError(t, h.ListModels(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	body := rec.Body.String()
	assert.Contains(t, body, "invalid_request_error")
	assert.Contains(t, body, "invalid category")
}

func TestListModels_IncludesSelectorAndProviderName(t *testing.T) {
	registry := providers.NewModelRegistry()
	openAI := &handlerMockProvider{
		models: &core.ModelsResponse{
			Object: "list",
			Data: []core.Model{
				{ID: "gpt-3.5-turbo", Object: "model", OwnedBy: "openai"},
			},
		},
	}
	openRouter := &handlerMockProvider{
		models: &core.ModelsResponse{
			Object: "list",
			Data: []core.Model{
				{ID: "openai/gpt-3.5-turbo", Object: "model", OwnedBy: "openai"},
			},
		},
	}
	registry.RegisterProviderWithNameAndType(openAI, "openai", "openai")
	registry.RegisterProviderWithNameAndType(openRouter, "openrouter", "openrouter")
	err := registry.Initialize(context.Background())
	require.NoError(t, err)

	h := NewHandler(nil, registry)
	c, rec := echotest.Get(t, "/admin/models")
	require.NoError(t, h.ListModels(c))
	require.Equal(t, http.StatusOK, rec.Code)

	models := echotest.Decode[[]providers.ModelWithProvider](t, rec)
	require.Len(t, models, 2)
	require.Equal(t, "openai/gpt-3.5-turbo", models[0].Selector)
	require.Equal(t, "openai", models[0].ProviderName)
	require.Equal(t, "openrouter/openai/gpt-3.5-turbo", models[1].Selector)
	require.Equal(t, "openrouter", models[1].ProviderName)
}

// --- ListCategories handler tests ---

func TestListCategories_NilRegistry(t *testing.T) {
	h := NewHandler(nil, nil)
	c, rec := echotest.Get(t, "/admin/models/categories")
	require.NoError(t, h.ListCategories(c))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "[]\n", rec.Body.String())
}

func TestListCategories_WithModels(t *testing.T) {
	registry := providers.NewModelRegistry()
	mock := &handlerMockProvider{
		models: &core.ModelsResponse{
			Object: "list",
			Data: []core.Model{
				{
					ID: "gpt-4o", Object: "model",
					Metadata: &core.ModelMetadata{Categories: []core.ModelCategory{core.CategoryTextGeneration}},
				},
				{
					ID: "dall-e-3", Object: "model",
					Metadata: &core.ModelMetadata{Categories: []core.ModelCategory{core.CategoryImage}},
				},
			},
		},
	}
	registry.RegisterProviderWithType(mock, "openai")
	err := registry.Initialize(context.Background())
	require.NoError(t, err)

	h := NewHandler(nil, registry)
	c, rec := echotest.Get(t, "/admin/models/categories")
	require.NoError(t, h.ListCategories(c))
	assert.Equal(t, http.StatusOK, rec.Code)

	cats := echotest.Decode[[]providers.CategoryCount](t, rec)
	require.Len(t, cats, 7)

	// Find "all" count
	for _, cat := range cats {
		if cat.Category == core.CategoryAll {
			assert.Equal(t, 2, cat.Count)
		}
	}
}

func TestProviderStatus_DistinguishesProvidersWithSameTypeByName(t *testing.T) {
	registry := providers.NewModelRegistry()
	registry.RegisterProviderWithNameAndType(&handlerMockProvider{
		models: &core.ModelsResponse{
			Object: "list",
			Data: []core.Model{
				{ID: "gpt-4o", Object: "model"},
			},
		},
	}, "openai_primary", "openai")
	registry.RegisterProviderWithNameAndType(&handlerMockProvider{
		err: errors.New("upstream unavailable"),
	}, "openai_backup", "openai")
	err := registry.Initialize(context.Background())
	require.NoError(t, err)

	registry.RecordAvailabilityCheck("openai_primary", nil)
	registry.RecordAvailabilityCheck("openai_backup", errors.New("dial tcp timeout"))

	h := NewHandler(nil, registry, WithConfiguredProviders([]providers.SanitizedProviderConfig{
		{
			Name:    "openai_backup",
			Type:    "openai",
			BaseURL: "https://backup.example.com/v1",
			Resilience: providers.SanitizedResilienceConfig{
				Retry: providers.SanitizedRetryConfig{
					MaxRetries:     2,
					InitialBackoff: "1s",
					MaxBackoff:     "10s",
					BackoffFactor:  2,
					JitterFactor:   0.1,
				},
				CircuitBreaker: providers.SanitizedCircuitBreakerConfig{
					FailureThreshold: 5,
					SuccessThreshold: 2,
					Timeout:          "30s",
				},
			},
		},
		{
			Name:    "openai_primary",
			Type:    "openai",
			BaseURL: "https://primary.example.com/v1",
			Resilience: providers.SanitizedResilienceConfig{
				Retry: providers.SanitizedRetryConfig{
					MaxRetries:     3,
					InitialBackoff: "1s",
					MaxBackoff:     "30s",
					BackoffFactor:  2,
					JitterFactor:   0.1,
				},
				CircuitBreaker: providers.SanitizedCircuitBreakerConfig{
					FailureThreshold: 5,
					SuccessThreshold: 2,
					Timeout:          "30s",
				},
			},
		},
	}))
	c, rec := echotest.Get(t, "/admin/providers/status")
	require.NoError(t, h.ProviderStatus(c))
	require.Equal(t, http.StatusOK, rec.Code)

	body := echotest.Decode[providerStatusResponse](t, rec)
	require.Equal(t, 2, body.Summary.Total)
	require.Equal(t, 1, body.Summary.Healthy)
	require.Equal(t, 1, body.Summary.Unhealthy)
	require.Equal(t, "degraded", body.Summary.OverallStatus)

	byName := make(map[string]providerStatusItemResponse, len(body.Providers))
	for _, provider := range body.Providers {
		byName[provider.Name] = provider
	}

	require.Contains(t, byName, "openai_primary")
	primary := byName["openai_primary"]
	require.Equal(t, "openai", primary.Type)
	require.Equal(t, "healthy", primary.Status)
	require.Equal(t, 1, primary.Runtime.DiscoveredModelCount)
	require.Equal(t, "https://primary.example.com/v1", primary.Config.BaseURL)
	require.Contains(t, byName, "openai_backup")
	backup := byName["openai_backup"]
	require.Equal(t, "openai", backup.Type)
	require.Equal(t, "unhealthy", backup.Status)
	require.Equal(t, 0, backup.Runtime.DiscoveredModelCount)
	require.Equal(t, "https://backup.example.com/v1", backup.Config.BaseURL)
	require.Contains(t, backup.LastError, "upstream unavailable")
}

func TestClassifyProviderStatus_RegisteredZeroModelProviderIsConfigured(t *testing.T) {
	status, label, reason, _ := classifyProviderStatus(
		providers.SanitizedProviderConfig{Name: "openai"},
		providers.ProviderRuntimeSnapshot{
			Name:       "openai",
			Registered: true,
		},
	)

	require.Equal(t, "degraded", status)
	require.Equal(t, "Configured", label)
	require.Equal(t, "provider is configured but has not exposed models yet", reason)
}

func TestClassifyProviderStatus_DerivesCachedModelInventory(t *testing.T) {
	status, label, reason, _ := classifyProviderStatus(
		providers.SanitizedProviderConfig{Name: "openai"},
		providers.ProviderRuntimeSnapshot{
			Name:                 "openai",
			Registered:           true,
			DiscoveredModelCount: 1,
		},
	)

	require.Equal(t, "degraded", status)
	require.Equal(t, "Starting", label)
	require.Equal(t, "serving cached model inventory while live refresh finishes", reason)
}

// TestBuildProviderStatusItem_ClassifyAndDisplayFallbacks covers the contract
// of buildProviderStatusItem — that classifyProviderStatus runs against the
// original inputs (so runtime-only providers reach the "Unknown" branch) and
// that the response row's Config/Runtime then gets Name/Type synthesised from
// whichever side has the value.
func TestBuildProviderStatusItem_ClassifyAndDisplayFallbacks(t *testing.T) {
	cases := []struct {
		name        string
		key         string
		cfg         providers.SanitizedProviderConfig
		runtime     providers.ProviderRuntimeSnapshot
		wantStatus  string
		wantLabel   string
		wantCfgName string
		wantCfgType string
		wantRunName string
		wantRunTyp  string
	}{
		{
			// Runtime-only: registry knows the provider but no operator config.
			// Must hit the "Unknown" branch — synthesising cfg.Name first
			// would mislabel this as "Configured".
			name:        "runtime_only_unknown",
			key:         "shadow",
			cfg:         providers.SanitizedProviderConfig{},
			runtime:     providers.ProviderRuntimeSnapshot{Name: "shadow", Type: "openai", Registered: true},
			wantStatus:  "degraded",
			wantLabel:   "Unknown",
			wantCfgName: "shadow",
			wantCfgType: "openai",
			wantRunName: "shadow",
			wantRunTyp:  "openai",
		},
		{
			// Config-only: operator wired the provider but the registry has
			// not produced a runtime snapshot yet.
			name:        "config_only_starting",
			key:         "openai",
			cfg:         providers.SanitizedProviderConfig{Name: "openai", Type: "openai"},
			runtime:     providers.ProviderRuntimeSnapshot{},
			wantStatus:  "degraded",
			wantLabel:   "Starting",
			wantCfgName: "openai",
			wantCfgType: "openai",
			wantRunName: "openai",
			wantRunTyp:  "openai",
		},
		{
			// Config + registered + zero models: existing "Configured" path,
			// guarded by TestClassifyProviderStatus_RegisteredZeroModelProviderIsConfigured
			// at the classifier level — re-asserted here through the wrapper.
			name:        "configured_zero_models",
			key:         "openai",
			cfg:         providers.SanitizedProviderConfig{Name: "openai", Type: "openai"},
			runtime:     providers.ProviderRuntimeSnapshot{Name: "openai", Type: "openai", Registered: true},
			wantStatus:  "degraded",
			wantLabel:   "Configured",
			wantCfgName: "openai",
			wantCfgType: "openai",
			wantRunName: "openai",
			wantRunTyp:  "openai",
		},
		{
			// Healthy: discovery succeeded.
			name: "healthy",
			key:  "openai",
			cfg:  providers.SanitizedProviderConfig{Name: "openai", Type: "openai"},
			runtime: providers.ProviderRuntimeSnapshot{
				Name:                    "openai",
				Type:                    "openai",
				Registered:              true,
				DiscoveredModelCount:    7,
				LastModelFetchSuccessAt: new(time.Now()),
			},
			wantStatus:  "healthy",
			wantLabel:   "Healthy",
			wantCfgName: "openai",
			wantCfgType: "openai",
			wantRunName: "openai",
			wantRunTyp:  "openai",
		},
		{
			// Type only known to the runtime side: cfg.Type should be filled
			// from runtime.Type so the response row carries a usable label.
			name:        "type_filled_from_runtime",
			key:         "custom",
			cfg:         providers.SanitizedProviderConfig{Name: "custom"},
			runtime:     providers.ProviderRuntimeSnapshot{Name: "custom", Type: "ollama", Registered: true},
			wantStatus:  "degraded",
			wantLabel:   "Configured",
			wantCfgName: "custom",
			wantCfgType: "ollama",
			wantRunName: "custom",
			wantRunTyp:  "ollama",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			item := buildProviderStatusItem(tc.key, tc.cfg, tc.runtime, nil)

			assert.Equal(t, tc.key, item.Name)
			assert.Equal(t, tc.wantStatus, item.Status)
			assert.Equal(t, tc.wantLabel, item.StatusLabel)
			assert.Equal(t, tc.wantCfgName, item.Config.Name)
			assert.Equal(t, tc.wantCfgType, item.Config.Type)
			assert.Equal(t, tc.wantRunName, item.Runtime.Name)
			assert.Equal(t, tc.wantRunTyp, item.Runtime.Type)
			assert.Equal(t, tc.wantCfgType, item.Type)
		})
	}
}

func TestDashboardConfig_ReturnsAllowlistedRuntimeFlags(t *testing.T) {
	cfg := DashboardConfigResponse{
		DemoMode:               "on",
		FailoverEnabled:        "on",
		LoggingEnabled:         "on",
		LoggingRetentionDays:   "14",
		UsageEnabled:           "off",
		BudgetsEnabled:         "on",
		RateLimitsEnabled:      "off",
		QuotaTemplatesEnabled:  "on",
		GuardrailsEnabled:      "on",
		PluginsEnabled:         "on",
		CacheEnabled:           "on",
		RedisURL:               "on",
		SemanticCacheEnabled:   "off",
		PricingRecalculation:   "on",
		LiveLogsEnabled:        "on",
		MCPEnabled:             "off",
		VirtualModelStrategies: "round_robin,cost,adaptive",
		UserPathHeader:         " X-Tenant-Path ",
	}
	h := NewHandler(nil, nil, WithDashboardRuntimeConfig(cfg))
	c, rec := echotest.Get(t, "/admin/runtime/config")
	require.NoError(t, h.DashboardConfig(c))
	require.Equal(t, http.StatusOK, rec.Code)

	want := cfg
	want.UserPathHeader = "X-Tenant-Path"
	assert.Equal(t, want, echotest.Decode[DashboardConfigResponse](t, rec))
}

func TestRefreshRuntime_ReturnsReport(t *testing.T) {
	started := time.Date(2026, 4, 11, 12, 0, 0, 0, time.UTC)
	refresher := &mockRuntimeRefresher{
		report: RuntimeRefreshReport{
			Status:        RuntimeRefreshStatusPartial,
			StartedAt:     started,
			FinishedAt:    started.Add(2 * time.Second),
			DurationMS:    2000,
			ModelCount:    7,
			ProviderCount: 2,
			Steps: []RuntimeRefreshStep{
				{Name: "providers", Status: RuntimeRefreshStatusPartial, Error: "one provider failed"},
			},
		},
	}
	h := NewHandler(nil, nil, WithRuntimeRefresher(refresher))
	c, rec := echotest.Post(t, "/admin/runtime/refresh", nil)
	require.NoError(t, h.RefreshRuntime(c))
	require.Equal(t, 1, refresher.calls)
	require.Equal(t, http.StatusOK, rec.Code)

	body := echotest.Decode[RuntimeRefreshReport](t, rec)
	require.Equal(t, RuntimeRefreshStatusPartial, body.Status)
	require.Equal(t, 7, body.ModelCount)
	require.Equal(t, 2, body.ProviderCount)
	require.Len(t, body.Steps, 1)
	require.Equal(t, "one provider failed", body.Steps[0].Error)
}

func TestRefreshRuntime_FeatureUnavailableWhenNotConfigured(t *testing.T) {
	h := NewHandler(nil, nil)
	c, rec := echotest.Post(t, "/admin/runtime/refresh", nil)
	require.NoError(t, h.RefreshRuntime(c))
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.Equal(t, map[string]any{
		"type":    string(core.ErrorTypeInvalidRequest),
		"message": "runtime refresh is unavailable",
		"param":   nil,
		"code":    "feature_unavailable",
	}, decodeErrorObject(t, rec))
}

func TestRefreshRuntime_PreservesGatewayError(t *testing.T) {
	refresher := &mockRuntimeRefresher{
		err: core.NewInvalidRequestErrorWithStatus(http.StatusRequestTimeout, "runtime refresh canceled before start", context.Canceled).
			WithCode("request_canceled"),
	}
	h := NewHandler(nil, nil, WithRuntimeRefresher(refresher))
	c, rec := echotest.Post(t, "/admin/runtime/refresh", nil)
	require.NoError(t, h.RefreshRuntime(c))
	require.Equal(t, http.StatusRequestTimeout, rec.Code)
	assert.Equal(t, map[string]any{
		"type":    string(core.ErrorTypeInvalidRequest),
		"message": "runtime refresh canceled before start",
		"param":   nil,
		"code":    "request_canceled",
	}, decodeErrorObject(t, rec))
}

func TestCacheOverview_FeatureUnavailableWhenCacheDisabled(t *testing.T) {
	h := NewHandler(&mockUsageReader{}, nil, WithDashboardRuntimeConfig(DashboardConfigResponse{
		CacheEnabled: "off",
	}))
	c, rec := echotest.Get(t, "/admin/cache/overview")
	require.NoError(t, h.CacheOverview(c))
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

func TestCacheOverview_ReturnsPayloadWhenEnabled(t *testing.T) {
	reader := &mockUsageReader{
		cacheOverview: &usage.CacheOverview{
			Summary: usage.CacheOverviewSummary{
				TotalHits:    4,
				ExactHits:    3,
				SemanticHits: 1,
				TotalInput:   120,
				TotalOutput:  60,
				TotalTokens:  180,
			},
			Daily: []usage.CacheOverviewDaily{
				{Date: "2026-03-31", Hits: 4, ExactHits: 3, SemanticHits: 1, InputTokens: 120, OutputTokens: 60, TotalTokens: 180},
			},
		},
	}
	h := NewHandler(reader, nil, WithDashboardRuntimeConfig(DashboardConfigResponse{
		CacheEnabled: "on",
	}))
	c, rec := echotest.Get(t, "/admin/cache/overview?days=30&user_path=/team")
	require.NoError(t, h.CacheOverview(c))
	require.Equal(t, http.StatusOK, rec.Code)

	body := echotest.Decode[usage.CacheOverview](t, rec)
	require.Equal(t, 4, body.Summary.TotalHits)
	require.Len(t, body.Daily, 1)
	require.Equal(t, 3, body.Daily[0].ExactHits)
	require.Equal(t, "/team", reader.lastCacheOverview.UserPath)
}

func TestCacheOverview_ReturnsErrorWhenReaderFails(t *testing.T) {
	reader := &mockUsageReader{cacheErr: errors.New("boom")}
	h := NewHandler(reader, nil, WithDashboardRuntimeConfig(DashboardConfigResponse{
		CacheEnabled: "on",
	}))
	c, rec := echotest.Get(t, "/admin/cache/overview?days=30")
	require.NoError(t, h.CacheOverview(c))
	require.Equal(t, http.StatusInternalServerError, rec.Code)

	errorBody := decodeErrorObject(t, rec)
	assert.Equal(t, "internal_error", errorBody["type"])
	assert.Equal(t, "an unexpected error occurred", errorBody["message"], "reader error must not leak")
	assert.Contains(t, errorBody, "param")
	assert.Contains(t, errorBody, "code")
}

func TestCacheOverview_ReturnsClientClosedWhenRequestIsCanceled(t *testing.T) {
	reader := &mockUsageReader{cacheErr: errors.Join(errors.New("failed to query cache overview summary"), context.Canceled)}
	h := NewHandler(reader, nil, WithDashboardRuntimeConfig(DashboardConfigResponse{
		CacheEnabled: "on",
	}))
	c, rec := echotest.Get(t, "/admin/cache/overview?days=30")
	require.NoError(t, h.CacheOverview(c))
	require.Equal(t, statusClientClosedRequest, rec.Code)

	errorBody := decodeErrorObject(t, rec)
	assert.Equal(t, string(core.ErrorTypeInvalidRequest), errorBody["type"])
	assert.Equal(t, "request canceled", errorBody["message"])
	assert.Equal(t, "request_canceled", errorBody["code"])
}

func TestCacheOverview_ReturnsGatewayTimeoutWhenRequestDeadlineExceeded(t *testing.T) {
	reader := &mockUsageReader{cacheErr: errors.Join(errors.New("failed to query cache overview summary"), context.DeadlineExceeded)}
	h := NewHandler(reader, nil, WithDashboardRuntimeConfig(DashboardConfigResponse{
		CacheEnabled: "on",
	}))
	c, rec := echotest.Get(t, "/admin/cache/overview?days=30")
	require.NoError(t, h.CacheOverview(c))
	require.Equal(t, http.StatusGatewayTimeout, rec.Code)

	errorBody := decodeErrorObject(t, rec)
	assert.Equal(t, string(core.ErrorTypeInvalidRequest), errorBody["type"])
	assert.Equal(t, "request timed out", errorBody["message"])
	assert.Equal(t, "request_timeout", errorBody["code"])
}

// --- handleError tests ---

func TestHandleError_GatewayErrors(t *testing.T) {
	tests := []struct {
		name           string
		err            error
		expectedStatus int
		expectedType   string
	}{
		{
			name:           "provider_error → 502",
			err:            core.NewProviderError("test", http.StatusBadGateway, "upstream error", nil),
			expectedStatus: http.StatusBadGateway,
			expectedType:   "provider_error",
		},
		{
			name:           "rate_limit_error → 429",
			err:            core.NewRateLimitError("test", "rate limited"),
			expectedStatus: http.StatusTooManyRequests,
			expectedType:   "rate_limit_error",
		},
		{
			name:           "invalid_request_error → 400",
			err:            core.NewInvalidRequestError("bad input", nil),
			expectedStatus: http.StatusBadRequest,
			expectedType:   "invalid_request_error",
		},
		{
			name:           "authentication_error → 401",
			err:            core.NewAuthenticationError("test", "invalid key"),
			expectedStatus: http.StatusUnauthorized,
			expectedType:   "authentication_error",
		},
		{
			name:           "not_found_error → 404",
			err:            core.NewNotFoundError("model not found"),
			expectedStatus: http.StatusNotFound,
			expectedType:   "not_found_error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, rec := echotest.Get(t, "/test")
			err := handleError(c, tt.err)
			require.NoError(t, err)
			assert.Equal(t, tt.expectedStatus, rec.Code)

			body := rec.Body.String()
			assert.Contains(t, body, tt.expectedType)
		})
	}
}

func TestHandleError_UnexpectedError(t *testing.T) {
	c, rec := echotest.Get(t, "/test")
	err := handleError(c, errors.New("something broke"))
	require.NoError(t, err)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)

	body := rec.Body.String()
	assert.Contains(t, body, "an unexpected error occurred")
	assert.NotContains(t, body, "something broke")
}

func TestHandleError_DoesNotLogCanceledRequestsAtDefaultLevel(t *testing.T) {
	var buf bytes.Buffer
	original := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() {
		slog.SetDefault(original)
	})

	c, rec := echotest.Request(t, http.MethodGet, "/admin/cache/overview", nil)
	c.SetRequest(c.Request().WithContext(core.WithRequestID(c.Request().Context(), "admin-canceled-req-789")))
	err := handleError(c, errors.Join(errors.New("failed to query cache overview summary"), context.Canceled))
	require.NoError(t, err)
	require.Equal(t, statusClientClosedRequest, rec.Code)
	logOutput := buf.String()
	require.Empty(t, logOutput)
}

func TestHandleError_LogsClientErrorsAtWarnLevel(t *testing.T) {
	var buf bytes.Buffer
	original := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() {
		slog.SetDefault(original)
	})

	c, rec := echotest.Request(t, http.MethodPost, "/admin/workflows", nil)
	c.SetRequest(c.Request().WithContext(core.WithRequestID(c.Request().Context(), "admin-warn-req-123")))
	err := handleError(c, core.NewInvalidRequestError("unknown provider name: missing", nil))
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, rec.Code)

	logOutput := buf.String()
	require.Contains(t, logOutput, `"level":"WARN"`)
	require.Contains(t, logOutput, `"msg":"admin request failed"`)
	require.Contains(t, logOutput, `"path":"/admin/workflows"`)
	require.Contains(t, logOutput, `"request_id":"admin-warn-req-123"`)
}

func TestHandleError_LogsServerErrorsAtErrorLevel(t *testing.T) {
	var buf bytes.Buffer
	original := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() {
		slog.SetDefault(original)
	})

	c, rec := echotest.Request(t, http.MethodPut, "/admin/guardrails", nil)
	c.SetRequest(c.Request().WithContext(core.WithRequestID(c.Request().Context(), "admin-error-req-456")))

	upstreamErr := errors.New("storage unavailable")
	err := handleError(c, core.NewProviderError("guardrails", http.StatusInternalServerError, "failed to refresh guardrails", upstreamErr))
	require.NoError(t, err)
	require.Equal(t, http.StatusInternalServerError, rec.Code)

	logOutput := buf.String()
	require.Contains(t, logOutput, `"level":"ERROR"`)
	require.Contains(t, logOutput, `"msg":"admin request failed"`)
	require.Contains(t, logOutput, `"provider":"guardrails"`)
	require.Contains(t, logOutput, `"request_id":"admin-error-req-456"`)
	require.Contains(t, logOutput, `"message":"failed to refresh guardrails"`)
}

func TestParseUsageParams_DaysDefault(t *testing.T) {
	c, _ := echotest.Get(t, "/test")
	params, err := parseUsageParams(c)
	require.NoError(t, err)
	assert.Equal(t, "daily", params.Interval)

	today := time.Now().UTC()
	expectedEnd := time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, time.UTC)
	expectedStart := expectedEnd.AddDate(0, 0, -29)

	assert.WithinDuration(t, expectedEnd, params.EndDate, 0)
	assert.WithinDuration(t, expectedStart, params.StartDate, 0)
}

func TestParseUsageParams_UsesTimezoneHeaderForDefaultRange(t *testing.T) {
	originalTimeNow := timeNow
	timeNow = func() time.Time {
		return time.Date(2026, 1, 15, 23, 30, 0, 0, time.UTC)
	}
	defer func() {
		timeNow = originalTimeNow
	}()

	c, _ := echotest.Get(t, "/test", echotest.WithHeader(dashboardTimeZoneHeader, "Europe/Warsaw"))

	params, err := parseUsageParams(c)
	require.NoError(t, err)

	location, err := time.LoadLocation("Europe/Warsaw")
	require.NoError(t, err)

	expectedEnd := time.Date(2026, 1, 16, 0, 0, 0, 0, location)
	expectedStart := expectedEnd.AddDate(0, 0, -29)

	assert.Equal(t, "Europe/Warsaw", params.TimeZone)
	assert.WithinDuration(t, expectedEnd, params.EndDate, 0)
	assert.WithinDuration(t, expectedStart, params.StartDate, 0)
}

func TestParseUsageParams_DaysExplicit(t *testing.T) {
	c, _ := echotest.Get(t, "/test?"+"days=7")
	params, err := parseUsageParams(c)
	require.NoError(t, err)

	today := time.Now().UTC()
	expectedEnd := time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, time.UTC)
	expectedStart := expectedEnd.AddDate(0, 0, -6)

	assert.WithinDuration(t, expectedStart, params.StartDate, 0)
	assert.WithinDuration(t, expectedEnd, params.EndDate, 0)
}

func TestParseUsageParams_DaysClamped(t *testing.T) {
	originalTimeNow := timeNow
	timeNow = func() time.Time {
		return time.Date(2026, 4, 28, 12, 0, 0, 0, time.UTC)
	}
	defer func() {
		timeNow = originalTimeNow
	}()

	c, _ := echotest.Get(t, "/test?"+"days=9999")
	params, err := parseUsageParams(c)
	require.NoError(t, err)

	expectedEnd := time.Date(2026, 4, 28, 0, 0, 0, 0, time.UTC)
	expectedStart := expectedEnd.AddDate(0, 0, -(maxDateRangeDays - 1))

	assert.WithinDuration(t, expectedStart, params.StartDate, 0)
	assert.WithinDuration(t, expectedEnd, params.EndDate, 0)
}

func TestParseUsageParams_StartAndEndDate(t *testing.T) {
	c, _ := echotest.Get(t, "/test?"+"start_date=2026-01-01&end_date=2026-01-31")
	params, err := parseUsageParams(c)
	require.NoError(t, err)

	expectedStart := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	expectedEnd := time.Date(2026, 1, 31, 0, 0, 0, 0, time.UTC)

	assert.WithinDuration(t, expectedStart, params.StartDate, 0)
	assert.WithinDuration(t, expectedEnd, params.EndDate, 0)
}

func TestParseUsageParams_OnlyStartDate(t *testing.T) {
	c, _ := echotest.Get(t, "/test?"+"start_date=2026-01-15")
	params, err := parseUsageParams(c)
	require.NoError(t, err)

	expectedStart := time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC)
	today := time.Now().UTC()
	expectedEnd := time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, time.UTC)

	assert.WithinDuration(t, expectedStart, params.StartDate, 0)
	assert.WithinDuration(t, expectedEnd, params.EndDate, 0)
}

func TestParseUsageParams_OnlyEndDate(t *testing.T) {
	c, _ := echotest.Get(t, "/test?"+"end_date=2026-02-10")
	params, err := parseUsageParams(c)
	require.NoError(t, err)

	expectedEnd := time.Date(2026, 2, 10, 0, 0, 0, 0, time.UTC)
	expectedStart := expectedEnd.AddDate(0, 0, -29)

	assert.WithinDuration(t, expectedStart, params.StartDate, 0)
	assert.WithinDuration(t, expectedEnd, params.EndDate, 0)
}

func TestParseUsageParams_InvalidStartDate(t *testing.T) {
	c, _ := echotest.Get(t, "/test?"+"start_date=invalid")
	_, err := parseUsageParams(c)
	require.Error(t, err)

	var gatewayErr *core.GatewayError
	require.ErrorAs(t, err, &gatewayErr)
	assert.Equal(t, http.StatusBadRequest, gatewayErr.HTTPStatusCode())
}

func TestParseUsageParams_InvalidEndDate(t *testing.T) {
	c, _ := echotest.Get(t, "/test?"+"start_date=2026-01-01&end_date=also-invalid")
	_, err := parseUsageParams(c)
	require.Error(t, err)

	var gatewayErr *core.GatewayError
	require.ErrorAs(t, err, &gatewayErr)
	assert.Equal(t, http.StatusBadRequest, gatewayErr.HTTPStatusCode())
}

func TestParseUsageParams_InvalidUserPath(t *testing.T) {
	c, _ := echotest.Get(t, "/test?"+"user_path=/team/../alpha")
	_, err := parseUsageParams(c)
	require.Error(t, err)

	var gatewayErr *core.GatewayError
	require.ErrorAs(t, err, &gatewayErr)
	require.Equal(t, `invalid user_path: user path cannot contain '.' or '..' segments`, gatewayErr.Message)
	assert.Equal(t, http.StatusBadRequest, gatewayErr.HTTPStatusCode())
}

func TestParseUsageParams_IntervalWeekly(t *testing.T) {
	c, _ := echotest.Get(t, "/test?"+"interval=weekly")
	params, err := parseUsageParams(c)
	require.NoError(t, err)
	assert.Equal(t, "weekly", params.Interval)
}

func TestParseUsageParams_IntervalMonthly(t *testing.T) {
	c, _ := echotest.Get(t, "/test?"+"interval=monthly")
	params, err := parseUsageParams(c)
	require.NoError(t, err)
	assert.Equal(t, "monthly", params.Interval)
}

func TestParseUsageParams_IntervalInvalid(t *testing.T) {
	c, _ := echotest.Get(t, "/test?"+"interval=hourly")
	params, err := parseUsageParams(c)
	require.NoError(t, err)
	assert.Equal(t, "daily", params.Interval)
}

// --- TokenThroughput handler tests ---

func TestTokenThroughput_InvalidGranularity(t *testing.T) {
	h := NewHandler(&mockUsageReader{}, nil)
	c, rec := echotest.Get(t, "/admin/usage/throughput?granularity=weekly")
	require.NoError(t, h.TokenThroughput(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "granularity")
}

func TestTokenThroughput_MissingGranularity(t *testing.T) {
	h := NewHandler(&mockUsageReader{}, nil)
	c, rec := echotest.Get(t, "/admin/usage/throughput")
	require.NoError(t, h.TokenThroughput(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestTokenThroughput_NilReaderReturnsEmptyWindow(t *testing.T) {
	h := NewHandler(nil, nil)
	c, rec := echotest.Get(t, "/admin/usage/throughput?granularity=minute")
	require.NoError(t, h.TokenThroughput(c))
	assert.Equal(t, http.StatusOK, rec.Code)

	tp := echotest.Decode[usage.TokenThroughput](t, rec)
	assert.Equal(t, "minute", tp.Granularity)
	assert.Equal(t, 60, tp.BucketSeconds)
	assert.Len(t, tp.Buckets, 60)

	for i, b := range tp.Buckets {
		assert.Zero(t, b.InputTokens+b.OutputTokens+b.PromptCachedTokens+b.LocallyCachedTokens, "bucket %d", i)
	}
}

func TestTokenThroughput_SuccessForwardsArgs(t *testing.T) {
	reader := &mockUsageReader{
		throughput: &usage.TokenThroughput{
			Granularity:   "minute",
			BucketSeconds: 60,
			Buckets: []usage.ThroughputBucket{
				{InputTokens: 70, OutputTokens: 40, PromptCachedTokens: 30, LocallyCachedTokens: 5},
			},
		},
	}
	h := NewHandler(reader, nil)
	c, rec := echotest.Get(t, "/admin/usage/throughput?granularity=minute")
	before := time.Now().UTC()
	require.NoError(t, h.TokenThroughput(c))
	assert.Equal(t, http.StatusOK, rec.Code)

	tp := echotest.Decode[usage.TokenThroughput](t, rec)
	require.Len(t, tp.Buckets, 1)
	assert.Equal(t, int64(30), tp.Buckets[0].PromptCachedTokens)

	// The handler forwards the parsed granularity, ~now, and a UTC offset by default.
	assert.Equal(t, "minute", reader.lastThroughputGran.Name)
	assert.Equal(t, int64(0), reader.lastThroughputOffset)
	assert.False(t, reader.lastThroughputEnd.Before(before))
	assert.False(t, reader.lastThroughputEnd.After(time.Now().UTC().Add(time.Second)), "forwarded end = %v, want ~now", reader.lastThroughputEnd)
}

func TestTokenThroughput_ForwardsTimezoneOffset(t *testing.T) {
	reader := &mockUsageReader{throughput: &usage.TokenThroughput{Granularity: "day", BucketSeconds: 86400}}
	h := NewHandler(reader, nil)

	c, rec := echotest.Get(t, "/admin/usage/throughput?granularity=day", echotest.WithHeader("X-GoModel-Timezone", "Asia/Kolkata")) // UTC+5:30, no DST
	require.NoError(t, h.TokenThroughput(c))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, int64(5*3600+30*60), reader.lastThroughputOffset)
}

func TestTokenThroughput_GatewayError(t *testing.T) {
	reader := &mockUsageReader{
		throughputErr: core.NewProviderError("test", http.StatusBadGateway, "upstream failed", nil),
	}
	h := NewHandler(reader, nil)
	c, rec := echotest.Get(t, "/admin/usage/throughput?granularity=hour")
	require.NoError(t, h.TokenThroughput(c))
	assert.Equal(t, http.StatusBadGateway, rec.Code)
}

func TestTokenThroughput_GenericError(t *testing.T) {
	reader := &mockUsageReader{
		throughputErr: errors.New("database connection lost"),
	}
	h := NewHandler(reader, nil)
	c, rec := echotest.Get(t, "/admin/usage/throughput?granularity=day")
	require.NoError(t, h.TokenThroughput(c))
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.NotContains(t, rec.Body.String(), "database connection lost")
}
