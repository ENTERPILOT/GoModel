package admin

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v5"

	"gomodel/internal/auditlog"
	"gomodel/internal/core"
	"gomodel/internal/providers"
	"gomodel/internal/usage"
)

// mockUsageReader implements usage.UsageReader for testing.
type mockUsageReader struct {
	summary           *usage.UsageSummary
	daily             []usage.DailyUsage
	modelUsage        []usage.ModelUsage
	usageLog          *usage.UsageLogResult
	cacheOverview     *usage.CacheOverview
	lastUsageLog      usage.UsageLogParams
	lastCacheOverview usage.UsageQueryParams
	summaryErr        error
	dailyErr          error
	modelUsageErr     error
	usageLogErr       error
	cacheErr          error
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

func (m *mockUsageReader) GetUsageLog(_ context.Context, params usage.UsageLogParams) (*usage.UsageLogResult, error) {
	m.lastUsageLog = params
	if m.usageLogErr != nil {
		return nil, m.usageLogErr
	}
	return m.usageLog, nil
}

func (m *mockUsageReader) GetCacheOverview(_ context.Context, params usage.UsageQueryParams) (*usage.CacheOverview, error) {
	m.lastCacheOverview = params
	if m.cacheErr != nil {
		return nil, m.cacheErr
	}
	return m.cacheOverview, nil
}

func (m *mockAuditReader) GetLogs(_ context.Context, params auditlog.LogQueryParams) (*auditlog.LogListResult, error) {
	m.lastQuery = params
	if m.logErr != nil {
		return nil, m.logErr
	}
	return m.logResult, nil
}

func (m *mockAuditReader) GetLogByID(_ context.Context, _ string) (*auditlog.LogEntry, error) {
	if m.logByIDErr != nil {
		return nil, m.logByIDErr
	}
	return m.logByID, nil
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

func newHandlerContext(path string) (*echo.Context, *httptest.ResponseRecorder) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	return e.NewContext(req, rec), rec
}

// --- UsageSummary handler tests ---

func TestUsageSummary_NilReader(t *testing.T) {
	h := NewHandler(nil, nil)
	c, rec := newHandlerContext("/admin/api/v1/usage/summary")

	if err := h.UsageSummary(c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
	var summary usage.UsageSummary
	if err := json.Unmarshal(rec.Body.Bytes(), &summary); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}
	if summary.TotalRequests != 0 || summary.TotalInput != 0 || summary.TotalOutput != 0 || summary.TotalTokens != 0 {
		t.Errorf("expected zeroed summary, got %+v", summary)
	}
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
	c, rec := newHandlerContext("/admin/api/v1/usage/summary?days=30")

	if err := h.UsageSummary(c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
	var summary usage.UsageSummary
	if err := json.Unmarshal(rec.Body.Bytes(), &summary); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}
	if summary.TotalRequests != 42 {
		t.Errorf("expected 42 requests, got %d", summary.TotalRequests)
	}
	if summary.TotalTokens != 1500 {
		t.Errorf("expected 1500 total tokens, got %d", summary.TotalTokens)
	}
}

func TestUsageSummary_GatewayError(t *testing.T) {
	reader := &mockUsageReader{
		summaryErr: core.NewProviderError("test", http.StatusBadGateway, "upstream failed", nil),
	}
	h := NewHandler(reader, nil)
	c, rec := newHandlerContext("/admin/api/v1/usage/summary")

	if err := h.UsageSummary(c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.Code != http.StatusBadGateway {
		t.Errorf("expected 502, got %d", rec.Code)
	}
	body := rec.Body.String()
	if !containsString(body, "provider_error") {
		t.Errorf("expected provider_error in body, got: %s", body)
	}
}

func TestUsageSummary_GenericError(t *testing.T) {
	reader := &mockUsageReader{
		summaryErr: errors.New("database connection lost"),
	}
	h := NewHandler(reader, nil)
	c, rec := newHandlerContext("/admin/api/v1/usage/summary")

	if err := h.UsageSummary(c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", rec.Code)
	}
	body := rec.Body.String()
	if !containsString(body, "internal_error") {
		t.Errorf("expected internal_error in body, got: %s", body)
	}
	if containsString(body, "database connection lost") {
		t.Errorf("original error message should be hidden, got: %s", body)
	}
	if !containsString(body, "an unexpected error occurred") {
		t.Errorf("expected generic message, got: %s", body)
	}
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
	c, rec := newHandlerContext("/admin/api/v1/usage/summary?days=30")

	if err := h.UsageSummary(c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}

	var result map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if cost, ok := result["total_input_cost"].(float64); !ok || cost != 3.0 {
		t.Errorf("expected total_input_cost 3.0, got %v", result["total_input_cost"])
	}
	if cost, ok := result["total_output_cost"].(float64); !ok || cost != 7.5 {
		t.Errorf("expected total_output_cost 7.5, got %v", result["total_output_cost"])
	}
	if cost, ok := result["total_cost"].(float64); !ok || cost != 10.5 {
		t.Errorf("expected total_cost 10.5, got %v", result["total_cost"])
	}
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
	c, rec := newHandlerContext("/admin/api/v1/usage/summary?days=30")

	if err := h.UsageSummary(c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var result map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	// Cost fields should be null when reader returns nil costs
	if result["total_cost"] != nil {
		t.Errorf("expected total_cost to be null, got %v", result["total_cost"])
	}
	if result["total_input_cost"] != nil {
		t.Errorf("expected total_input_cost to be null, got %v", result["total_input_cost"])
	}
	if result["total_output_cost"] != nil {
		t.Errorf("expected total_output_cost to be null, got %v", result["total_output_cost"])
	}
}

// --- DailyUsage handler tests ---

func TestDailyUsage_NilReader(t *testing.T) {
	h := NewHandler(nil, nil)
	c, rec := newHandlerContext("/admin/api/v1/usage/daily")

	if err := h.DailyUsage(c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
	// Should be [] not null
	if rec.Body.String() != "[]\n" {
		t.Errorf("expected empty JSON array, got: %q", rec.Body.String())
	}
}

func TestDailyUsage_Success(t *testing.T) {
	reader := &mockUsageReader{
		daily: []usage.DailyUsage{
			{Date: "2026-02-01", Requests: 10, InputTokens: 100, OutputTokens: 50, TotalTokens: 150},
			{Date: "2026-02-02", Requests: 20, InputTokens: 200, OutputTokens: 100, TotalTokens: 300},
		},
	}
	h := NewHandler(reader, nil)
	c, rec := newHandlerContext("/admin/api/v1/usage/daily?days=7")

	if err := h.DailyUsage(c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
	var daily []usage.DailyUsage
	if err := json.Unmarshal(rec.Body.Bytes(), &daily); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}
	if len(daily) != 2 {
		t.Errorf("expected 2 entries, got %d", len(daily))
	}
}

func TestDailyUsage_NilResult(t *testing.T) {
	reader := &mockUsageReader{
		daily: nil, // reader returns nil slice
	}
	h := NewHandler(reader, nil)
	c, rec := newHandlerContext("/admin/api/v1/usage/daily")

	if err := h.DailyUsage(c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
	// Should be [] not null
	if rec.Body.String() != "[]\n" {
		t.Errorf("expected empty JSON array, got: %q", rec.Body.String())
	}
}

func TestDailyUsage_Error(t *testing.T) {
	reader := &mockUsageReader{
		dailyErr: core.NewRateLimitError("test", "too many requests"),
	}
	h := NewHandler(reader, nil)
	c, rec := newHandlerContext("/admin/api/v1/usage/daily")

	if err := h.DailyUsage(c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("expected 429, got %d", rec.Code)
	}
	body := rec.Body.String()
	if !containsString(body, "rate_limit_error") {
		t.Errorf("expected rate_limit_error in body, got: %s", body)
	}
}

// --- UsageByModel handler tests ---

func TestUsageByModel_NilReader(t *testing.T) {
	h := NewHandler(nil, nil)
	c, rec := newHandlerContext("/admin/api/v1/usage/models")

	if err := h.UsageByModel(c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
	if rec.Body.String() != "[]\n" {
		t.Errorf("expected empty JSON array, got: %q", rec.Body.String())
	}
}

func TestUsageByModel_Success(t *testing.T) {
	cost := 1.5
	reader := &mockUsageReader{
		modelUsage: []usage.ModelUsage{
			{Model: "gpt-4", Provider: "openai", InputTokens: 1000, OutputTokens: 500, TotalCost: &cost},
		},
	}
	h := NewHandler(reader, nil)
	c, rec := newHandlerContext("/admin/api/v1/usage/models?days=30")

	if err := h.UsageByModel(c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
	var models []usage.ModelUsage
	if err := json.Unmarshal(rec.Body.Bytes(), &models); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}
	if len(models) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(models))
	}
	if models[0].Model != "gpt-4" {
		t.Errorf("expected model gpt-4, got %s", models[0].Model)
	}
	if models[0].TotalCost == nil || *models[0].TotalCost != 1.5 {
		t.Errorf("expected total_cost 1.5, got %v", models[0].TotalCost)
	}
}

func TestUsageByModel_PreservesProviderName(t *testing.T) {
	reader := &mockUsageReader{
		modelUsage: []usage.ModelUsage{
			{Model: "gpt-4o", Provider: "openai", ProviderName: "primary-openai", InputTokens: 100, OutputTokens: 25},
		},
	}
	h := NewHandler(reader, nil)
	c, rec := newHandlerContext("/admin/api/v1/usage/models?days=30")

	if err := h.UsageByModel(c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var models []usage.ModelUsage
	if err := json.Unmarshal(rec.Body.Bytes(), &models); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}
	if len(models) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(models))
	}
	if models[0].ProviderName != "primary-openai" {
		t.Fatalf("ProviderName = %q, want %q", models[0].ProviderName, "primary-openai")
	}
	if models[0].Provider != "openai" {
		t.Fatalf("Provider = %q, want %q", models[0].Provider, "openai")
	}
}

func TestUsageByModel_Error(t *testing.T) {
	reader := &mockUsageReader{
		modelUsageErr: errors.New("db failure"),
	}
	h := NewHandler(reader, nil)
	c, rec := newHandlerContext("/admin/api/v1/usage/models")

	if err := h.UsageByModel(c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", rec.Code)
	}
}

// --- UsageLog handler tests ---

func TestUsageLog_NilReader(t *testing.T) {
	h := NewHandler(nil, nil)
	c, rec := newHandlerContext("/admin/api/v1/usage/log")

	if err := h.UsageLog(c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}

	var result usage.UsageLogResult
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}
	if len(result.Entries) != 0 {
		t.Errorf("expected 0 entries, got %d", len(result.Entries))
	}
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
	c, rec := newHandlerContext("/admin/api/v1/usage/log?days=30&limit=50&offset=0")

	if err := h.UsageLog(c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}

	var result usage.UsageLogResult
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}
	if len(result.Entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(result.Entries))
	}
	if result.Total != 2 {
		t.Errorf("expected total 2, got %d", result.Total)
	}
	if result.Entries[0].Model != "gpt-4" {
		t.Errorf("expected first entry model gpt-4, got %s", result.Entries[0].Model)
	}
	if result.Entries[0].RawData == nil {
		t.Fatal("expected raw_data on first entry, got nil")
	}
	if ct, ok := result.Entries[0].RawData["cached_tokens"].(float64); !ok || ct != 50 {
		t.Errorf("expected cached_tokens 50, got %v", result.Entries[0].RawData["cached_tokens"])
	}
	if result.Entries[1].RawData != nil {
		t.Errorf("expected nil raw_data on second entry, got %v", result.Entries[1].RawData)
	}
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
	c, rec := newHandlerContext("/admin/api/v1/usage/log?days=30")

	if err := h.UsageLog(c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var result usage.UsageLogResult
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}
	if len(result.Entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(result.Entries))
	}
	if result.Entries[0].ProviderName != "primary-openai" {
		t.Fatalf("ProviderName = %q, want %q", result.Entries[0].ProviderName, "primary-openai")
	}
	if result.Entries[0].Provider != "openai" {
		t.Fatalf("Provider = %q, want %q", result.Entries[0].Provider, "openai")
	}
}

func TestUsageLog_Error(t *testing.T) {
	reader := &mockUsageReader{
		usageLogErr: core.NewProviderError("test", http.StatusBadGateway, "upstream failed", nil),
	}
	h := NewHandler(reader, nil)
	c, rec := newHandlerContext("/admin/api/v1/usage/log")

	if err := h.UsageLog(c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.Code != http.StatusBadGateway {
		t.Errorf("expected 502, got %d", rec.Code)
	}
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
	c, rec := newHandlerContext("/admin/api/v1/usage/log?model=gpt-4&provider=openai&user_path=/team&search=test&limit=10&offset=5")

	if err := h.UsageLog(c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
	if reader.lastUsageLog.UserPath != "/team" {
		t.Errorf("expected user_path /team, got %q", reader.lastUsageLog.UserPath)
	}
}

// --- AuditLog handler tests ---

func TestAuditLog_NilReader(t *testing.T) {
	h := NewHandler(nil, nil)
	c, rec := newHandlerContext("/admin/api/v1/audit/log")

	if err := h.AuditLog(c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}

	var result auditlog.LogListResult
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}
	if len(result.Entries) != 0 {
		t.Errorf("expected 0 entries, got %d", len(result.Entries))
	}
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
	c, rec := newHandlerContext("/admin/api/v1/audit/log?days=7")

	if err := h.AuditLog(c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}

	var result auditlog.LogListResult
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}
	if len(result.Entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(result.Entries))
	}
	if result.Total != 1 {
		t.Errorf("expected total 1, got %d", result.Total)
	}
	if result.Entries[0].ID != "log-1" {
		t.Errorf("expected entry id log-1, got %s", result.Entries[0].ID)
	}
	if result.Entries[0].Data == nil || result.Entries[0].Data.RequestBody == nil {
		t.Errorf("expected request body data to be present")
	}
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
	c, rec := newHandlerContext("/admin/api/v1/audit/log?days=7")

	if err := h.AuditLog(c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var result auditlog.LogListResult
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}
	if len(result.Entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(result.Entries))
	}
	if result.Entries[0].ProviderName != "primary-openai" {
		t.Fatalf("ProviderName = %q, want %q", result.Entries[0].ProviderName, "primary-openai")
	}
	if result.Entries[0].Provider != "openai" {
		t.Fatalf("Provider = %q, want %q", result.Entries[0].Provider, "openai")
	}
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
	c, rec := newHandlerContext("/admin/api/v1/audit/log?model=gpt-4&provider=openai&method=post&path=/v1/chat/completions&user_path=/team&error_type=provider_error&status_code=502&stream=true&search=timeout&limit=10&offset=5")

	if err := h.AuditLog(c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}

	if reader.lastQuery.RequestedModel != "gpt-4" {
		t.Errorf("expected requested model filter gpt-4, got %q", reader.lastQuery.RequestedModel)
	}
	if reader.lastQuery.Provider != "openai" {
		t.Errorf("expected provider filter openai, got %q", reader.lastQuery.Provider)
	}
	if reader.lastQuery.Method != http.MethodPost {
		t.Errorf("expected method POST, got %q", reader.lastQuery.Method)
	}
	if reader.lastQuery.Path != "/v1/chat/completions" {
		t.Errorf("expected path filter to match, got %q", reader.lastQuery.Path)
	}
	if reader.lastQuery.UserPath != "/team" {
		t.Errorf("expected user_path filter to match, got %q", reader.lastQuery.UserPath)
	}
	if reader.lastQuery.ErrorType != "provider_error" {
		t.Errorf("expected error_type provider_error, got %q", reader.lastQuery.ErrorType)
	}
	if reader.lastQuery.StatusCode == nil || *reader.lastQuery.StatusCode != 502 {
		t.Errorf("expected status_code 502, got %+v", reader.lastQuery.StatusCode)
	}
	if reader.lastQuery.Stream == nil || !*reader.lastQuery.Stream {
		t.Errorf("expected stream filter true, got %+v", reader.lastQuery.Stream)
	}
	if reader.lastQuery.Search != "timeout" {
		t.Errorf("expected search timeout, got %q", reader.lastQuery.Search)
	}
	if reader.lastQuery.Limit != 10 || reader.lastQuery.Offset != 5 {
		t.Errorf("expected limit/offset 10/5, got %d/%d", reader.lastQuery.Limit, reader.lastQuery.Offset)
	}
}

func TestAuditLog_InvalidStatusCode(t *testing.T) {
	reader := &mockAuditReader{
		logResult: &auditlog.LogListResult{Entries: []auditlog.LogEntry{}},
	}
	h := NewHandler(nil, nil, WithAuditReader(reader))
	c, rec := newHandlerContext("/admin/api/v1/audit/log?status_code=foo")

	if err := h.AuditLog(c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
	if !containsString(rec.Body.String(), "invalid_request_error") {
		t.Errorf("expected invalid_request_error in body, got: %s", rec.Body.String())
	}
}

func TestAuditLog_InvalidStream(t *testing.T) {
	reader := &mockAuditReader{
		logResult: &auditlog.LogListResult{Entries: []auditlog.LogEntry{}},
	}
	h := NewHandler(nil, nil, WithAuditReader(reader))
	c, rec := newHandlerContext("/admin/api/v1/audit/log?stream=maybe")

	if err := h.AuditLog(c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
	if !containsString(rec.Body.String(), "invalid_request_error") {
		t.Errorf("expected invalid_request_error in body, got: %s", rec.Body.String())
	}
}

func TestAuditLog_Error(t *testing.T) {
	reader := &mockAuditReader{
		logErr: core.NewProviderError("test", http.StatusBadGateway, "upstream failed", nil),
	}
	h := NewHandler(nil, nil, WithAuditReader(reader))
	c, rec := newHandlerContext("/admin/api/v1/audit/log")

	if err := h.AuditLog(c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.Code != http.StatusBadGateway {
		t.Errorf("expected 502, got %d", rec.Code)
	}
}

func TestAuditConversation_NilReader(t *testing.T) {
	h := NewHandler(nil, nil)
	c, rec := newHandlerContext("/admin/api/v1/audit/conversation?log_id=log-1")

	if err := h.AuditConversation(c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}

	var result auditlog.ConversationResult
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}
	if result.AnchorID != "log-1" {
		t.Errorf("expected anchor log-1, got %q", result.AnchorID)
	}
	if len(result.Entries) != 0 {
		t.Errorf("expected empty entries, got %d", len(result.Entries))
	}
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
	c, rec := newHandlerContext("/admin/api/v1/audit/conversation?log_id=log-2&limit=80")

	if err := h.AuditConversation(c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
	if reader.lastConversationID != "log-2" || reader.lastConversationLim != 80 {
		t.Errorf("expected call with log-2/80, got %q/%d", reader.lastConversationID, reader.lastConversationLim)
	}
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
	c, rec := newHandlerContext("/admin/api/v1/audit/conversation?log_id=log-2")

	if err := h.AuditConversation(c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var result auditlog.ConversationResult
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}
	if len(result.Entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(result.Entries))
	}
	if result.Entries[0].ProviderName != "primary-openai" {
		t.Fatalf("ProviderName = %q, want %q", result.Entries[0].ProviderName, "primary-openai")
	}
	if result.Entries[0].Provider != "openai" {
		t.Fatalf("Provider = %q, want %q", result.Entries[0].Provider, "openai")
	}
}

func TestAuditConversation_MissingLogID(t *testing.T) {
	reader := &mockAuditReader{}
	h := NewHandler(nil, nil, WithAuditReader(reader))
	c, rec := newHandlerContext("/admin/api/v1/audit/conversation")

	if err := h.AuditConversation(c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestAuditConversation_InvalidLimit(t *testing.T) {
	reader := &mockAuditReader{}
	h := NewHandler(nil, nil, WithAuditReader(reader))
	c, rec := newHandlerContext("/admin/api/v1/audit/conversation?log_id=log-1&limit=bad")

	if err := h.AuditConversation(c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestAuditConversation_Error(t *testing.T) {
	reader := &mockAuditReader{
		conversationErr: core.NewProviderError("test", http.StatusBadGateway, "upstream failed", nil),
	}
	h := NewHandler(nil, nil, WithAuditReader(reader))
	c, rec := newHandlerContext("/admin/api/v1/audit/conversation?log_id=log-1")

	if err := h.AuditConversation(c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.Code != http.StatusBadGateway {
		t.Errorf("expected 502, got %d", rec.Code)
	}
}

// --- ListModels handler tests ---

func TestListModels_NilRegistry(t *testing.T) {
	h := NewHandler(nil, nil)
	c, rec := newHandlerContext("/admin/api/v1/models")

	if err := h.ListModels(c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
	if rec.Body.String() != "[]\n" {
		t.Errorf("expected empty JSON array, got: %q", rec.Body.String())
	}
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
	if err := registry.Initialize(context.Background()); err != nil {
		t.Fatalf("failed to initialize registry: %v", err)
	}

	h := NewHandler(nil, registry)
	c, rec := newHandlerContext("/admin/api/v1/models")

	if err := h.ListModels(c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}

	var models []providers.ModelWithProvider
	if err := json.Unmarshal(rec.Body.Bytes(), &models); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("expected 2 models, got %d", len(models))
	}
	// Should be sorted by model ID
	if models[0].Model.ID != "claude-3" {
		t.Errorf("expected first model to be claude-3, got %s", models[0].Model.ID)
	}
	if models[1].Model.ID != "gpt-4" {
		t.Errorf("expected second model to be gpt-4, got %s", models[1].Model.ID)
	}
	if models[0].ProviderType != "test" {
		t.Errorf("expected provider type 'test', got %s", models[0].ProviderType)
	}
}

func TestListModels_EmptyRegistry(t *testing.T) {
	// A registry with no providers initialized — ListModelsWithProvider returns nil
	registry := providers.NewModelRegistry()

	h := NewHandler(nil, registry)
	c, rec := newHandlerContext("/admin/api/v1/models")

	if err := h.ListModels(c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
	if rec.Body.String() != "[]\n" {
		t.Errorf("expected empty JSON array, got: %q", rec.Body.String())
	}
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
	if err := registry.Initialize(context.Background()); err != nil {
		t.Fatalf("failed to initialize registry: %v", err)
	}

	h := NewHandler(nil, registry)

	t.Run("FilterTextGeneration", func(t *testing.T) {
		c, rec := newHandlerContext("/admin/api/v1/models?category=text_generation")
		if err := h.ListModels(c); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if rec.Code != http.StatusOK {
			t.Errorf("expected 200, got %d", rec.Code)
		}
		var models []providers.ModelWithProvider
		if err := json.Unmarshal(rec.Body.Bytes(), &models); err != nil {
			t.Fatalf("failed to unmarshal: %v", err)
		}
		if len(models) != 1 {
			t.Fatalf("expected 1 model, got %d", len(models))
		}
		if models[0].Model.ID != "gpt-4o" {
			t.Errorf("expected gpt-4o, got %s", models[0].Model.ID)
		}
	})

	t.Run("FilterAll", func(t *testing.T) {
		c, rec := newHandlerContext("/admin/api/v1/models?category=all")
		if err := h.ListModels(c); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		var models []providers.ModelWithProvider
		if err := json.Unmarshal(rec.Body.Bytes(), &models); err != nil {
			t.Fatalf("failed to unmarshal: %v", err)
		}
		if len(models) != 3 {
			t.Errorf("expected 3 models for 'all', got %d", len(models))
		}
	})

	t.Run("NoFilter", func(t *testing.T) {
		c, rec := newHandlerContext("/admin/api/v1/models")
		if err := h.ListModels(c); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		var models []providers.ModelWithProvider
		if err := json.Unmarshal(rec.Body.Bytes(), &models); err != nil {
			t.Fatalf("failed to unmarshal: %v", err)
		}
		if len(models) != 3 {
			t.Errorf("expected 3 models without filter, got %d", len(models))
		}
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
	if err := registry.Initialize(context.Background()); err != nil {
		t.Fatalf("failed to initialize registry: %v", err)
	}

	h := NewHandler(nil, registry)
	c, rec := newHandlerContext("/admin/api/v1/models?category=bogus_category")

	if err := h.ListModels(c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
	body := rec.Body.String()
	if !containsString(body, "invalid_request_error") {
		t.Errorf("expected invalid_request_error in body, got: %s", body)
	}
	if !containsString(body, "invalid category") {
		t.Errorf("expected 'invalid category' in body, got: %s", body)
	}
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
	if err := registry.Initialize(context.Background()); err != nil {
		t.Fatalf("failed to initialize registry: %v", err)
	}

	h := NewHandler(nil, registry)
	c, rec := newHandlerContext("/admin/api/v1/models")

	if err := h.ListModels(c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var models []providers.ModelWithProvider
	if err := json.Unmarshal(rec.Body.Bytes(), &models); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("expected 2 models, got %d", len(models))
	}

	if models[0].Selector != "openai/gpt-3.5-turbo" {
		t.Fatalf("models[0].Selector = %q, want %q", models[0].Selector, "openai/gpt-3.5-turbo")
	}
	if models[0].ProviderName != "openai" {
		t.Fatalf("models[0].ProviderName = %q, want %q", models[0].ProviderName, "openai")
	}
	if models[1].Selector != "openrouter/openai/gpt-3.5-turbo" {
		t.Fatalf("models[1].Selector = %q, want %q", models[1].Selector, "openrouter/openai/gpt-3.5-turbo")
	}
	if models[1].ProviderName != "openrouter" {
		t.Fatalf("models[1].ProviderName = %q, want %q", models[1].ProviderName, "openrouter")
	}
}

// --- ListCategories handler tests ---

func TestListCategories_NilRegistry(t *testing.T) {
	h := NewHandler(nil, nil)
	c, rec := newHandlerContext("/admin/api/v1/models/categories")

	if err := h.ListCategories(c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
	if rec.Body.String() != "[]\n" {
		t.Errorf("expected empty JSON array, got: %q", rec.Body.String())
	}
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
	if err := registry.Initialize(context.Background()); err != nil {
		t.Fatalf("failed to initialize registry: %v", err)
	}

	h := NewHandler(nil, registry)
	c, rec := newHandlerContext("/admin/api/v1/models/categories")

	if err := h.ListCategories(c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}

	var cats []providers.CategoryCount
	if err := json.Unmarshal(rec.Body.Bytes(), &cats); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}
	if len(cats) != 7 {
		t.Fatalf("expected 7 categories, got %d", len(cats))
	}

	// Find "all" count
	for _, cat := range cats {
		if cat.Category == core.CategoryAll {
			if cat.Count != 2 {
				t.Errorf("All count = %d, want 2", cat.Count)
			}
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

	if err := registry.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}
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
	c, rec := newHandlerContext("/admin/api/v1/providers/status")

	if err := h.ProviderStatus(c); err != nil {
		t.Fatalf("ProviderStatus() error = %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var body providerStatusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}
	if body.Summary.Total != 2 {
		t.Fatalf("summary.total = %d, want 2", body.Summary.Total)
	}
	if body.Summary.Healthy != 1 {
		t.Fatalf("summary.healthy = %d, want 1", body.Summary.Healthy)
	}
	if body.Summary.Unhealthy != 1 {
		t.Fatalf("summary.unhealthy = %d, want 1", body.Summary.Unhealthy)
	}
	if body.Summary.OverallStatus != "degraded" {
		t.Fatalf("summary.overall_status = %q, want degraded", body.Summary.OverallStatus)
	}

	byName := make(map[string]providerStatusItemResponse, len(body.Providers))
	for _, provider := range body.Providers {
		byName[provider.Name] = provider
	}

	primary, ok := byName["openai_primary"]
	if !ok {
		t.Fatalf("missing openai_primary in %#v", body.Providers)
	}
	if primary.Type != "openai" {
		t.Fatalf("primary.Type = %q, want openai", primary.Type)
	}
	if primary.Status != "healthy" {
		t.Fatalf("primary.Status = %q, want healthy", primary.Status)
	}
	if primary.Runtime.DiscoveredModelCount != 1 {
		t.Fatalf("primary discovered models = %d, want 1", primary.Runtime.DiscoveredModelCount)
	}
	if primary.Config.BaseURL != "https://primary.example.com/v1" {
		t.Fatalf("primary base_url = %q, want primary endpoint", primary.Config.BaseURL)
	}

	backup, ok := byName["openai_backup"]
	if !ok {
		t.Fatalf("missing openai_backup in %#v", body.Providers)
	}
	if backup.Type != "openai" {
		t.Fatalf("backup.Type = %q, want openai", backup.Type)
	}
	if backup.Status != "unhealthy" {
		t.Fatalf("backup.Status = %q, want unhealthy", backup.Status)
	}
	if backup.Runtime.DiscoveredModelCount != 0 {
		t.Fatalf("backup discovered models = %d, want 0", backup.Runtime.DiscoveredModelCount)
	}
	if backup.Config.BaseURL != "https://backup.example.com/v1" {
		t.Fatalf("backup base_url = %q, want backup endpoint", backup.Config.BaseURL)
	}
	if !strings.Contains(backup.LastError, "upstream unavailable") {
		t.Fatalf("backup last_error = %q, want model fetch failure", backup.LastError)
	}
}

func TestClassifyProviderStatus_RegisteredZeroModelProviderIsConfigured(t *testing.T) {
	status, label, reason, _ := classifyProviderStatus(
		providers.SanitizedProviderConfig{Name: "openai"},
		providers.ProviderRuntimeSnapshot{
			Name:       "openai",
			Registered: true,
		},
	)

	if status != "degraded" {
		t.Fatalf("status = %q, want degraded", status)
	}
	if label != "Configured" {
		t.Fatalf("label = %q, want Configured", label)
	}
	if reason != "provider is configured but has not exposed models yet" {
		t.Fatalf("reason = %q, want configured zero-model reason", reason)
	}
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

	if status != "degraded" {
		t.Fatalf("status = %q, want degraded", status)
	}
	if label != "Starting" {
		t.Fatalf("label = %q, want Starting", label)
	}
	if reason != "serving cached model inventory while live refresh finishes" {
		t.Fatalf("reason = %q, want cached inventory reason", reason)
	}
}

func TestDashboardConfig_ReturnsAllowlistedRuntimeFlags(t *testing.T) {
	h := NewHandler(nil, nil, WithDashboardRuntimeConfig(DashboardConfigResponse{
		FeatureFallbackMode:  "auto",
		LoggingEnabled:       "on",
		UsageEnabled:         "off",
		GuardrailsEnabled:    "on",
		CacheEnabled:         "on",
		RedisURL:             "on",
		SemanticCacheEnabled: "off",
	}))
	c, rec := newHandlerContext("/admin/api/v1/dashboard/config")

	if err := h.DashboardConfig(c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var body DashboardConfigResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}
	if got := body.FeatureFallbackMode; got != "auto" {
		t.Fatalf("FEATURE_FALLBACK_MODE = %q, want auto", got)
	}
	if got := body.LoggingEnabled; got != "on" {
		t.Fatalf("LOGGING_ENABLED = %q, want on", got)
	}
	if got := body.UsageEnabled; got != "off" {
		t.Fatalf("USAGE_ENABLED = %q, want off", got)
	}
	if got := body.GuardrailsEnabled; got != "on" {
		t.Fatalf("GUARDRAILS_ENABLED = %q, want on", got)
	}
	if got := body.CacheEnabled; got != "on" {
		t.Fatalf("CACHE_ENABLED = %q, want on", got)
	}
	if got := body.RedisURL; got != "on" {
		t.Fatalf("REDIS_URL = %q, want on", got)
	}
	if got := body.SemanticCacheEnabled; got != "off" {
		t.Fatalf("SEMANTIC_CACHE_ENABLED = %q, want off", got)
	}
	if rec.Body.String() == "" || strings.Contains(rec.Body.String(), "UNRELATED_FLAG") {
		t.Fatal("UNRELATED_FLAG should not be exposed")
	}
}

func TestCacheOverview_FeatureUnavailableWhenCacheDisabled(t *testing.T) {
	h := NewHandler(&mockUsageReader{}, nil, WithDashboardRuntimeConfig(DashboardConfigResponse{
		CacheEnabled: "off",
	}))
	c, rec := newHandlerContext("/admin/api/v1/cache/overview")

	if err := h.CacheOverview(c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rec.Code)
	}
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
	c, rec := newHandlerContext("/admin/api/v1/cache/overview?days=30&user_path=/team")

	if err := h.CacheOverview(c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var body usage.CacheOverview
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}
	if body.Summary.TotalHits != 4 {
		t.Fatalf("total_hits = %d, want 4", body.Summary.TotalHits)
	}
	if len(body.Daily) != 1 || body.Daily[0].ExactHits != 3 {
		t.Fatalf("unexpected daily payload: %+v", body.Daily)
	}
	if reader.lastCacheOverview.CacheMode != usage.CacheModeCached {
		t.Fatalf("CacheMode = %q, want %q", reader.lastCacheOverview.CacheMode, usage.CacheModeCached)
	}
	if reader.lastCacheOverview.UserPath != "/team" {
		t.Fatalf("UserPath = %q, want %q", reader.lastCacheOverview.UserPath, "/team")
	}
}

func TestCacheOverview_ReturnsErrorWhenReaderFails(t *testing.T) {
	reader := &mockUsageReader{cacheErr: errors.New("boom")}
	h := NewHandler(reader, nil, WithDashboardRuntimeConfig(DashboardConfigResponse{
		CacheEnabled: "on",
	}))
	c, rec := newHandlerContext("/admin/api/v1/cache/overview?days=30")

	if err := h.CacheOverview(c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rec.Code)
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}
	errorBody, ok := body["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected error payload, got %v", body)
	}
	if got, ok := errorBody["type"].(string); !ok || got != "internal_error" {
		t.Fatalf("error.type = %#v, want %q", errorBody["type"], "internal_error")
	}
	if got, ok := errorBody["message"].(string); !ok || got != "an unexpected error occurred" {
		t.Fatalf("error.message = %#v, want %q", errorBody["message"], "an unexpected error occurred")
	} else if strings.Contains(got, "boom") {
		t.Fatalf("error.message leaked reader error: %q", got)
	}
	if _, ok := errorBody["param"]; !ok {
		t.Fatalf("error.param missing from payload: %v", errorBody)
	}
	if _, ok := errorBody["code"]; !ok {
		t.Fatalf("error.code missing from payload: %v", errorBody)
	}
	if reader.lastCacheOverview.CacheMode != usage.CacheModeCached {
		t.Fatalf("CacheMode = %q, want %q", reader.lastCacheOverview.CacheMode, usage.CacheModeCached)
	}
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
			c, rec := newHandlerContext("/test")

			if err := handleError(c, tt.err); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if rec.Code != tt.expectedStatus {
				t.Errorf("expected status %d, got %d", tt.expectedStatus, rec.Code)
			}
			body := rec.Body.String()
			if !containsString(body, tt.expectedType) {
				t.Errorf("expected %s in body, got: %s", tt.expectedType, body)
			}
		})
	}
}

func TestHandleError_UnexpectedError(t *testing.T) {
	c, rec := newHandlerContext("/test")

	if err := handleError(c, errors.New("something broke")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", rec.Code)
	}
	body := rec.Body.String()
	if !containsString(body, "an unexpected error occurred") {
		t.Errorf("expected generic message, got: %s", body)
	}
	if containsString(body, "something broke") {
		t.Errorf("original error should be hidden, got: %s", body)
	}
}

// containsString is a small helper to check substring presence.
func containsString(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && stringContains(s, substr))
}

func stringContains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func newContext(query string) *echo.Context {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/test?"+query, nil)
	rec := httptest.NewRecorder()
	return e.NewContext(req, rec)
}

func TestParseUsageParams_DaysDefault(t *testing.T) {
	c := newContext("")
	params, err := parseUsageParams(c)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if params.Interval != "daily" {
		t.Errorf("expected interval 'daily', got %q", params.Interval)
	}

	today := time.Now().UTC()
	expectedEnd := time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, time.UTC)
	expectedStart := expectedEnd.AddDate(0, 0, -29)

	if !params.EndDate.Equal(expectedEnd) {
		t.Errorf("expected end date %v, got %v", expectedEnd, params.EndDate)
	}
	if !params.StartDate.Equal(expectedStart) {
		t.Errorf("expected start date %v, got %v", expectedStart, params.StartDate)
	}
}

func TestParseUsageParams_UsesTimezoneHeaderForDefaultRange(t *testing.T) {
	originalTimeNow := timeNow
	timeNow = func() time.Time {
		return time.Date(2026, 1, 15, 23, 30, 0, 0, time.UTC)
	}
	defer func() {
		timeNow = originalTimeNow
	}()

	c := newContext("")
	c.Request().Header.Set(dashboardTimeZoneHeader, "Europe/Warsaw")

	params, err := parseUsageParams(c)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	location, err := time.LoadLocation("Europe/Warsaw")
	if err != nil {
		t.Fatalf("failed to load location: %v", err)
	}

	expectedEnd := time.Date(2026, 1, 16, 0, 0, 0, 0, location)
	expectedStart := expectedEnd.AddDate(0, 0, -29)

	if params.TimeZone != "Europe/Warsaw" {
		t.Errorf("expected timezone %q, got %q", "Europe/Warsaw", params.TimeZone)
	}
	if !params.EndDate.Equal(expectedEnd) {
		t.Errorf("expected end date %v, got %v", expectedEnd, params.EndDate)
	}
	if !params.StartDate.Equal(expectedStart) {
		t.Errorf("expected start date %v, got %v", expectedStart, params.StartDate)
	}
}

func TestParseUsageParams_DaysExplicit(t *testing.T) {
	c := newContext("days=7")
	params, err := parseUsageParams(c)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	today := time.Now().UTC()
	expectedEnd := time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, time.UTC)
	expectedStart := expectedEnd.AddDate(0, 0, -6)

	if !params.StartDate.Equal(expectedStart) {
		t.Errorf("expected start date %v, got %v", expectedStart, params.StartDate)
	}
	if !params.EndDate.Equal(expectedEnd) {
		t.Errorf("expected end date %v, got %v", expectedEnd, params.EndDate)
	}
}

func TestParseUsageParams_StartAndEndDate(t *testing.T) {
	c := newContext("start_date=2026-01-01&end_date=2026-01-31")
	params, err := parseUsageParams(c)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expectedStart := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	expectedEnd := time.Date(2026, 1, 31, 0, 0, 0, 0, time.UTC)

	if !params.StartDate.Equal(expectedStart) {
		t.Errorf("expected start date %v, got %v", expectedStart, params.StartDate)
	}
	if !params.EndDate.Equal(expectedEnd) {
		t.Errorf("expected end date %v, got %v", expectedEnd, params.EndDate)
	}
}

func TestParseUsageParams_OnlyStartDate(t *testing.T) {
	c := newContext("start_date=2026-01-15")
	params, err := parseUsageParams(c)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expectedStart := time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC)
	today := time.Now().UTC()
	expectedEnd := time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, time.UTC)

	if !params.StartDate.Equal(expectedStart) {
		t.Errorf("expected start date %v, got %v", expectedStart, params.StartDate)
	}
	if !params.EndDate.Equal(expectedEnd) {
		t.Errorf("expected end date %v, got %v", expectedEnd, params.EndDate)
	}
}

func TestParseUsageParams_OnlyEndDate(t *testing.T) {
	c := newContext("end_date=2026-02-10")
	params, err := parseUsageParams(c)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expectedEnd := time.Date(2026, 2, 10, 0, 0, 0, 0, time.UTC)
	expectedStart := expectedEnd.AddDate(0, 0, -29)

	if !params.StartDate.Equal(expectedStart) {
		t.Errorf("expected start date %v, got %v", expectedStart, params.StartDate)
	}
	if !params.EndDate.Equal(expectedEnd) {
		t.Errorf("expected end date %v, got %v", expectedEnd, params.EndDate)
	}
}

func TestParseUsageParams_InvalidStartDate(t *testing.T) {
	c := newContext("start_date=invalid")
	_, err := parseUsageParams(c)
	if err == nil {
		t.Fatal("expected error for invalid start_date, got nil")
	}

	var gatewayErr *core.GatewayError
	if !errors.As(err, &gatewayErr) {
		t.Fatalf("expected GatewayError, got %T", err)
	}
	if gatewayErr.HTTPStatusCode() != http.StatusBadRequest {
		t.Errorf("expected status 400, got %d", gatewayErr.HTTPStatusCode())
	}
}

func TestParseUsageParams_InvalidEndDate(t *testing.T) {
	c := newContext("start_date=2026-01-01&end_date=also-invalid")
	_, err := parseUsageParams(c)
	if err == nil {
		t.Fatal("expected error for invalid end_date, got nil")
	}

	var gatewayErr *core.GatewayError
	if !errors.As(err, &gatewayErr) {
		t.Fatalf("expected GatewayError, got %T", err)
	}
	if gatewayErr.HTTPStatusCode() != http.StatusBadRequest {
		t.Errorf("expected status 400, got %d", gatewayErr.HTTPStatusCode())
	}
}

func TestParseUsageParams_InvalidUserPath(t *testing.T) {
	c := newContext("user_path=/team/../alpha")
	_, err := parseUsageParams(c)
	if err == nil {
		t.Fatal("expected error for invalid user_path, got nil")
	}

	var gatewayErr *core.GatewayError
	if !errors.As(err, &gatewayErr) {
		t.Fatalf("expected GatewayError, got %T", err)
	}
	if gatewayErr.Message != `invalid user_path: user path cannot contain '.' or '..' segments` {
		t.Fatalf("message = %q, want invalid user_path message", gatewayErr.Message)
	}
	if gatewayErr.HTTPStatusCode() != http.StatusBadRequest {
		t.Errorf("expected status 400, got %d", gatewayErr.HTTPStatusCode())
	}
}

func TestParseUsageParams_IntervalWeekly(t *testing.T) {
	c := newContext("interval=weekly")
	params, err := parseUsageParams(c)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if params.Interval != "weekly" {
		t.Errorf("expected interval 'weekly', got %q", params.Interval)
	}
}

func TestParseUsageParams_IntervalMonthly(t *testing.T) {
	c := newContext("interval=monthly")
	params, err := parseUsageParams(c)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if params.Interval != "monthly" {
		t.Errorf("expected interval 'monthly', got %q", params.Interval)
	}
}

func TestParseUsageParams_IntervalInvalid(t *testing.T) {
	c := newContext("interval=hourly")
	params, err := parseUsageParams(c)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if params.Interval != "daily" {
		t.Errorf("expected default interval 'daily', got %q", params.Interval)
	}
}

func TestParseUsageParams_IntervalEmpty(t *testing.T) {
	c := newContext("")
	params, err := parseUsageParams(c)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if params.Interval != "daily" {
		t.Errorf("expected default interval 'daily', got %q", params.Interval)
	}
}

// Ensure usage.UsageQueryParams is the type used (compile check)
var _ = func() usage.UsageQueryParams {
	return usage.UsageQueryParams{
		StartDate: time.Time{},
		EndDate:   time.Time{},
		Interval:  "daily",
	}
}
