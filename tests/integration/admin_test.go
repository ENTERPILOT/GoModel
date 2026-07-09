//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"gomodel/internal/providers"
	"gomodel/internal/usage"
	"gomodel/tests/integration/dbassert"
)

func TestAdminUsageSummary_PostgreSQL(t *testing.T) {
	fixture := SetupTestServer(t, TestServerConfig{
		DBType:                "postgresql",
		UsageEnabled:          true,
		AdminEndpointsEnabled: true,
		OnlyModelInteractions: false,
	})

	// Clear existing usage entries
	dbassert.ClearUsage(t, fixture.PgPool)

	// Send 2 chat requests
	for range 2 {
		payload := newChatRequest("gpt-4", "Hello!")
		resp := sendChatRequest(t, fixture.ServerURL, payload)
		require.Equal(t, 200, resp.StatusCode)
		closeBody(resp)
	}

	// Wait for usage buffer to flush (flush interval is 1s in tests)
	time.Sleep(2 * time.Second)

	// Query admin API
	resp, err := http.Get(fixture.ServerURL + "/admin/usage/summary?days=30")
	require.NoError(t, err)
	defer closeBody(resp)

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	var summary usage.UsageSummary
	require.NoError(t, json.Unmarshal(body, &summary))

	assert.Equal(t, 2, summary.TotalRequests, "expected 2 total requests")
	assert.Equal(t, int64(20), summary.TotalInput, "expected 20 input tokens (2 * 10)")
	assert.Equal(t, int64(16), summary.TotalOutput, "expected 16 output tokens (2 * 8)")
	assert.Equal(t, int64(36), summary.TotalTokens, "expected 36 total tokens (2 * 18)")

	fixture.FlushAndClose(t)
}

func TestAdminDailyUsage_PostgreSQL(t *testing.T) {
	fixture := SetupTestServer(t, TestServerConfig{
		DBType:                "postgresql",
		UsageEnabled:          true,
		AdminEndpointsEnabled: true,
		OnlyModelInteractions: false,
	})

	// Clear existing usage entries
	dbassert.ClearUsage(t, fixture.PgPool)

	// Send requests
	for range 2 {
		payload := newChatRequest("gpt-4", "Hello!")
		resp := sendChatRequest(t, fixture.ServerURL, payload)
		require.Equal(t, 200, resp.StatusCode)
		closeBody(resp)
	}

	// Wait for usage buffer to flush
	time.Sleep(2 * time.Second)

	// Query admin API
	resp, err := http.Get(fixture.ServerURL + "/admin/usage/daily?days=30")
	require.NoError(t, err)
	defer closeBody(resp)

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	var daily []usage.DailyUsage
	require.NoError(t, json.Unmarshal(body, &daily))

	require.NotEmpty(t, daily, "expected at least one daily entry")

	// Find today's entry
	today := time.Now().UTC().Format("2006-01-02")
	var todayEntry *usage.DailyUsage
	for i := range daily {
		if daily[i].Date == today {
			todayEntry = &daily[i]
			break
		}
	}
	require.NotNil(t, todayEntry, "expected entry for today %s", today)
	assert.Equal(t, 2, todayEntry.Requests, "expected 2 requests today")
	assert.Equal(t, int64(20), todayEntry.InputTokens, "expected 20 input tokens")
	assert.Equal(t, int64(16), todayEntry.OutputTokens, "expected 16 output tokens")
	assert.Equal(t, int64(36), todayEntry.TotalTokens, "expected 36 total tokens")

	fixture.FlushAndClose(t)
}

func TestAdminUsageSummary_MongoDB(t *testing.T) {
	fixture := SetupTestServer(t, TestServerConfig{
		DBType:                "mongodb",
		UsageEnabled:          true,
		AdminEndpointsEnabled: true,
		OnlyModelInteractions: false,
	})

	// Clear existing usage entries
	dbassert.ClearUsageMongo(t, fixture.MongoDb)

	// Send 2 chat requests
	for range 2 {
		payload := newChatRequest("gpt-4", "Hello!")
		resp := sendChatRequest(t, fixture.ServerURL, payload)
		require.Equal(t, 200, resp.StatusCode)
		closeBody(resp)
	}

	// Wait for usage buffer to flush
	time.Sleep(2 * time.Second)

	// Query admin API
	resp, err := http.Get(fixture.ServerURL + "/admin/usage/summary?days=30")
	require.NoError(t, err)
	defer closeBody(resp)

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	var summary usage.UsageSummary
	require.NoError(t, json.Unmarshal(body, &summary))

	assert.Equal(t, 2, summary.TotalRequests, "expected 2 total requests")
	assert.Equal(t, int64(20), summary.TotalInput, "expected 20 input tokens (2 * 10)")
	assert.Equal(t, int64(16), summary.TotalOutput, "expected 16 output tokens (2 * 8)")
	assert.Equal(t, int64(36), summary.TotalTokens, "expected 36 total tokens (2 * 18)")

	fixture.FlushAndClose(t)
}

func TestAdminDailyUsage_WithInterval_PostgreSQL(t *testing.T) {
	fixture := SetupTestServer(t, TestServerConfig{
		DBType:                "postgresql",
		UsageEnabled:          true,
		AdminEndpointsEnabled: true,
		OnlyModelInteractions: false,
	})

	// Send a request so there's data
	payload := newChatRequest("gpt-4", "Hello!")
	resp := sendChatRequest(t, fixture.ServerURL, payload)
	require.Equal(t, 200, resp.StatusCode)
	closeBody(resp)

	// Wait for usage buffer to flush
	time.Sleep(2 * time.Second)

	// Query with weekly interval
	resp, err := http.Get(fixture.ServerURL + "/admin/usage/daily?interval=weekly")
	require.NoError(t, err)
	defer closeBody(resp)

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	var daily []usage.DailyUsage
	require.NoError(t, json.Unmarshal(body, &daily))

	// Should return valid JSON array (may be empty or have entries)
	assert.True(t, json.Valid(body), "response should be valid JSON")

	fixture.FlushAndClose(t)
}

func TestAdminPricingRecalculationNoMasterKey_PostgreSQL(t *testing.T) {
	fixture := SetupTestServer(t, TestServerConfig{
		DBType:                           "postgresql",
		UsageEnabled:                     true,
		UsagePricingRecalculationEnabled: true,
		AdminEndpointsEnabled:            true,
		OnlyModelInteractions:            false,
	})

	dbassert.ClearUsage(t, fixture.PgPool)

	payload := newChatRequest("gpt-4", "Hello!")
	resp := sendChatRequest(t, fixture.ServerURL, payload)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	closeBody(resp)

	time.Sleep(2 * time.Second)

	req, err := http.NewRequest(http.MethodPost, fixture.ServerURL+"/admin/usage/recalculate-pricing", bytes.NewBufferString(`{"confirmation":"recalculate"}`))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	resp, err = http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer closeBody(resp)

	require.Equal(t, http.StatusOK, resp.StatusCode)

	var result usage.RecalculatePricingResult
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	assert.Equal(t, "ok", result.Status)
	assert.GreaterOrEqual(t, result.Matched, int64(1))
	assert.GreaterOrEqual(t, result.Recalculated, int64(1))

	fixture.FlushAndClose(t)
}

func TestAdminPricingRecalculationRefreshesRewriteSavings_PostgreSQL(t *testing.T) {
	fixture := SetupTestServer(t, TestServerConfig{
		DBType:                           "postgresql",
		UsageEnabled:                     true,
		UsagePricingRecalculationEnabled: true,
		AdminEndpointsEnabled:            true,
		OnlyModelInteractions:            false,
	})

	dbassert.ClearUsage(t, fixture.PgPool)

	payload := newChatRequest("gpt-4", "Hello!")
	resp := sendChatRequest(t, fixture.ServerURL, payload)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	closeBody(resp)

	time.Sleep(2 * time.Second)

	// Simulate a request-rewriter savings estimate persisted with a stale
	// cost, as if pricing changed after the row was written.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	tag, err := fixture.PgPool.Exec(ctx,
		"UPDATE usage SET rewrite_tokens_saved = 500000, rewrite_cost_saved = 123.0")
	require.NoError(t, err)
	require.GreaterOrEqual(t, tag.RowsAffected(), int64(1))

	req, err := http.NewRequest(http.MethodPost, fixture.ServerURL+"/admin/usage/recalculate-pricing", bytes.NewBufferString(`{"confirmation":"recalculate"}`))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	resp, err = http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer closeBody(resp)

	require.Equal(t, http.StatusOK, resp.StatusCode)

	var result usage.RecalculatePricingResult
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	assert.Equal(t, "ok", result.Status)
	require.GreaterOrEqual(t, result.Recalculated, int64(1))

	// The fixture prices models at a flat $1000/Mtok input, so 500k saved
	// tokens must recalculate to exactly $500 (input-cost delta), replacing
	// the stale $123 value.
	var costSaved *float64
	require.NoError(t, fixture.PgPool.QueryRow(ctx,
		"SELECT rewrite_cost_saved FROM usage WHERE rewrite_tokens_saved = 500000 LIMIT 1").Scan(&costSaved))
	require.NotNil(t, costSaved, "rewrite_cost_saved must be repriced, not cleared")
	assert.InDelta(t, 500.0, *costSaved, 1e-9)

	fixture.FlushAndClose(t)
}

func TestAdminModels_PostgreSQL(t *testing.T) {
	fixture := SetupTestServer(t, TestServerConfig{
		DBType:                "postgresql",
		UsageEnabled:          false,
		AdminEndpointsEnabled: true,
		OnlyModelInteractions: false,
	})

	// Query admin models endpoint
	resp, err := http.Get(fixture.ServerURL + "/admin/models")
	require.NoError(t, err)
	defer closeBody(resp)

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	var models []providers.ModelWithProvider
	require.NoError(t, json.Unmarshal(body, &models))

	require.NotEmpty(t, models, "expected at least one model")

	// Should be sorted by model ID
	for i := 1; i < len(models); i++ {
		assert.True(t, models[i-1].Model.ID < models[i].Model.ID,
			"models should be sorted, but %s >= %s", models[i-1].Model.ID, models[i].Model.ID)
	}

	// Each model should have model.id and provider_type
	for _, m := range models {
		assert.NotEmpty(t, m.Model.ID, "model.id should not be empty")
		assert.NotEmpty(t, m.ProviderType, "provider_type should not be empty")
	}

	fixture.FlushAndClose(t)
}
