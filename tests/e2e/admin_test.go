//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"gomodel/internal/admin"
	"gomodel/internal/providers"
	"gomodel/internal/usage"
)

type e2ePricingRecalculator struct {
	calls  int
	params usage.RecalculatePricingParams
	result usage.RecalculatePricingResult
}

func (r *e2ePricingRecalculator) RecalculatePricing(_ context.Context, params usage.RecalculatePricingParams, _ usage.PricingResolver) (usage.RecalculatePricingResult, error) {
	r.calls++
	r.params = params
	if r.result.Status == "" {
		r.result.Status = "ok"
	}
	return r.result, nil
}

func TestAdminAPI_EndpointsEnabled_E2E(t *testing.T) {
	ts := setupAdminServer(t, "", true, false)
	defer ts.Close()

	endpoints := []string{
		"/admin/usage/summary",
		"/admin/usage/daily",
		"/admin/audit/log",
		"/admin/audit/conversation?log_id=test",
		"/admin/models",
	}

	for _, ep := range endpoints {
		t.Run(ep, func(t *testing.T) {
			resp, err := http.Get(ts.URL + ep)
			require.NoError(t, err)
			defer closeBody(resp)

			assert.Equal(t, http.StatusOK, resp.StatusCode, "endpoint %s should return 200", ep)

			body, err := io.ReadAll(resp.Body)
			require.NoError(t, err)

			// Should be valid JSON
			assert.True(t, json.Valid(body), "response should be valid JSON for %s, got: %s", ep, string(body))
		})
	}
}

func TestAdminAPI_EndpointsDisabled_E2E(t *testing.T) {
	ts := setupAdminServer(t, "", false, false)
	defer ts.Close()

	endpoints := []string{
		"/admin/usage/summary",
		"/admin/usage/daily",
		"/admin/audit/log",
		"/admin/audit/conversation?log_id=test",
		"/admin/models",
	}

	for _, ep := range endpoints {
		t.Run(ep, func(t *testing.T) {
			resp, err := http.Get(ts.URL + ep)
			require.NoError(t, err)
			defer closeBody(resp)

			assert.Equal(t, http.StatusNotFound, resp.StatusCode, "endpoint %s should return 404 when disabled", ep)
		})
	}
}

func TestAdminAPI_RequiresAuth_E2E(t *testing.T) {
	ts := setupAdminServer(t, testMasterKey, true, false)
	defer ts.Close()

	t.Run("without auth returns 401", func(t *testing.T) {
		resp, err := http.Get(ts.URL + "/admin/models")
		require.NoError(t, err)
		defer closeBody(resp)

		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	})

	t.Run("with valid auth returns 200", func(t *testing.T) {
		req, err := http.NewRequest(http.MethodGet, ts.URL+"/admin/models", nil)
		require.NoError(t, err)
		req.Header.Set("Authorization", "Bearer "+testMasterKey)

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer closeBody(resp)

		assert.Equal(t, http.StatusOK, resp.StatusCode)
	})
}

func TestAdminAPI_PricingRecalculationNoMasterKey_E2E(t *testing.T) {
	recalculator := &e2ePricingRecalculator{
		result: usage.RecalculatePricingResult{Status: "ok", Matched: 1, Recalculated: 1, WithPricing: 1},
	}
	ts := setupE2EAdminServer(t, e2eServerOptions{
		adminOptions: []admin.Option{admin.WithUsagePricingRecalculator(recalculator)},
	})
	defer ts.Close()

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/admin/usage/recalculate-pricing", strings.NewReader(`{
		"confirmation": "recalculate",
		"user_path": "/team/recalc"
	}`))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer closeBody(resp)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, 1, recalculator.calls)
	assert.Equal(t, "/team/recalc", recalculator.params.UserPath)

	var result usage.RecalculatePricingResult
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	assert.Equal(t, int64(1), result.Recalculated)
}

func TestAdminDashboard_Enabled_E2E(t *testing.T) {
	ts := setupAdminServer(t, "", true, true)
	defer ts.Close()

	t.Run("dashboard returns 200 HTML", func(t *testing.T) {
		resp, err := http.Get(ts.URL + "/admin/dashboard")
		require.NoError(t, err)
		defer closeBody(resp)

		assert.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Contains(t, resp.Header.Get("Content-Type"), "text/html")
	})

	t.Run("static CSS returns 200", func(t *testing.T) {
		resp, err := http.Get(ts.URL + "/admin/static/css/dashboard.css")
		require.NoError(t, err)
		defer closeBody(resp)

		assert.Equal(t, http.StatusOK, resp.StatusCode)
	})
}

func TestAdminDashboard_Disabled_E2E(t *testing.T) {
	ts := setupAdminServer(t, "", true, false)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/admin/dashboard")
	require.NoError(t, err)
	defer closeBody(resp)

	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestAdminDashboard_SkipsAuth_E2E(t *testing.T) {
	ts := setupAdminServer(t, testMasterKey, true, true)
	defer ts.Close()

	t.Run("dashboard is public (200 without auth)", func(t *testing.T) {
		resp, err := http.Get(ts.URL + "/admin/dashboard")
		require.NoError(t, err)
		defer closeBody(resp)

		assert.Equal(t, http.StatusOK, resp.StatusCode)
	})

	t.Run("API is protected (401 without auth)", func(t *testing.T) {
		resp, err := http.Get(ts.URL + "/admin/models")
		require.NoError(t, err)
		defer closeBody(resp)

		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	})
}

func TestAdminAPI_ModelsEndpoint_E2E(t *testing.T) {
	ts := setupAdminServer(t, "", true, false)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/admin/models")
	require.NoError(t, err)
	defer closeBody(resp)

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	var models []providers.ModelWithProvider
	require.NoError(t, json.Unmarshal(body, &models))

	// TestProvider returns 3 models
	assert.Len(t, models, 3)

	// Should be sorted by model ID
	for i := 1; i < len(models); i++ {
		assert.True(t, models[i-1].Model.ID < models[i].Model.ID,
			"models should be sorted, but %s >= %s", models[i-1].Model.ID, models[i].Model.ID)
	}

	// Each model should have provider_type
	for _, m := range models {
		assert.Equal(t, "test", m.ProviderType, "model %s should have provider_type 'test'", m.Model.ID)
	}
}

func TestAdminAPI_UsageEndpoints_E2E(t *testing.T) {
	const (
		expectedRequests                         = 2
		mockProviderInputTokensPerRequest  int64 = 10
		mockProviderOutputTokensPerRequest int64 = 20
		expectedInputTokens                      = mockProviderInputTokensPerRequest * expectedRequests
		expectedOutputTokens                     = mockProviderOutputTokensPerRequest * expectedRequests
		expectedTotalTokens                      = expectedInputTokens + expectedOutputTokens
	)

	// Mock provider usage is 10 input + 20 output tokens per request, and this test sends 2 requests.
	requestDate := time.Now().UTC()
	today := requestDate.Format("2006-01-02")
	yesterday := requestDate.Add(-24 * time.Hour).Format("2006-01-02")

	usageFixture := setupSQLiteUsageFixture(t)
	ts := setupE2EAdminServer(t, e2eServerOptions{
		adminUsageReader: usageFixture.reader,
		usageLogger:      usageFixture.logger,
	})
	defer ts.Close()

	for i := 0; i < expectedRequests; i++ {
		resp := sendJSONRequest(t, ts.URL+chatCompletionsPath, defaultChatReq("Hello usage"))
		require.Equal(t, http.StatusOK, resp.StatusCode)
		closeBody(resp)
	}
	usageFixture.flush(t)

	t.Run("summary includes persisted usage", func(t *testing.T) {
		resp, err := http.Get(ts.URL + "/admin/usage/summary")
		require.NoError(t, err)
		defer closeBody(resp)

		require.Equal(t, http.StatusOK, resp.StatusCode)

		var summary usage.UsageSummary
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&summary))
		assert.Equal(t, expectedRequests, summary.TotalRequests)
		assert.Equal(t, expectedInputTokens, summary.TotalInput)
		assert.Equal(t, expectedOutputTokens, summary.TotalOutput)
		assert.Equal(t, expectedTotalTokens, summary.TotalTokens)
	})

	t.Run("daily includes persisted usage", func(t *testing.T) {
		resp, err := http.Get(ts.URL + "/admin/usage/daily?days=7")
		require.NoError(t, err)
		defer closeBody(resp)

		require.Equal(t, http.StatusOK, resp.StatusCode)

		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)

		var daily []usage.DailyUsage
		require.NoError(t, json.Unmarshal(body, &daily))
		require.NotEmpty(t, daily)

		var todayEntry *usage.DailyUsage
		for i := range daily {
			if daily[i].Date == today || daily[i].Date == yesterday {
				todayEntry = &daily[i]
				break
			}
		}
		require.NotNil(t, todayEntry, "expected daily usage entry for %s or %s", today, yesterday)
		assert.Equal(t, expectedRequests, todayEntry.Requests)
		assert.Equal(t, expectedInputTokens, todayEntry.InputTokens)
		assert.Equal(t, expectedOutputTokens, todayEntry.OutputTokens)
		assert.Equal(t, expectedTotalTokens, todayEntry.TotalTokens)
	})

	t.Run("query params accepted", func(t *testing.T) {
		resp, err := http.Get(ts.URL + "/admin/usage/daily?days=7&interval=weekly")
		require.NoError(t, err)
		defer closeBody(resp)

		require.Equal(t, http.StatusOK, resp.StatusCode)

		var weekly []usage.DailyUsage
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&weekly))
	})
}
