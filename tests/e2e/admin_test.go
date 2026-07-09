//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"slices"
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

	// Each endpoint asserts the response shape it advertises in its handler,
	// not just "valid JSON". A regression that returned `{"error":"..."}` with
	// a 200 status would otherwise slip through the smoke test.
	cases := []struct {
		name     string
		endpoint string
		check    func(t *testing.T, body []byte)
	}{
		{
			name:     "usage summary returns aggregate counters",
			endpoint: "/admin/usage/summary",
			check: func(t *testing.T, body []byte) {
				var summary usage.UsageSummary
				require.NoError(t, json.Unmarshal(body, &summary))
				assert.GreaterOrEqual(t, summary.TotalRequests, 0)
				assert.GreaterOrEqual(t, summary.TotalInput, int64(0))
				assert.GreaterOrEqual(t, summary.TotalOutput, int64(0))
				assert.GreaterOrEqual(t, summary.TotalTokens, int64(0))
			},
		},
		{
			name:     "daily usage returns a rollup array",
			endpoint: "/admin/usage/daily",
			check: func(t *testing.T, body []byte) {
				var daily []usage.DailyUsage
				require.NoError(t, json.Unmarshal(body, &daily))
				for i, entry := range daily {
					assert.NotEmpty(t, entry.Date, "entry %d should have a date label", i)
				}
			},
		},
		{
			name:     "audit log returns paginated entries envelope",
			endpoint: "/admin/audit/log",
			check: func(t *testing.T, body []byte) {
				var envelope struct {
					Entries []map[string]any `json:"entries"`
					Total   int              `json:"total"`
					Limit   int              `json:"limit"`
					Offset  int              `json:"offset"`
				}
				require.NoError(t, json.Unmarshal(body, &envelope))
				// entries is `[]` (not null) even when empty per the handler contract.
				assert.NotNil(t, envelope.Entries)
				assert.GreaterOrEqual(t, envelope.Total, 0)
			},
		},
		{
			name:     "audit conversation returns anchor + entries",
			endpoint: "/admin/audit/conversation?log_id=test",
			check: func(t *testing.T, body []byte) {
				var conv struct {
					AnchorID string           `json:"anchor_id"`
					Entries  []map[string]any `json:"entries"`
				}
				require.NoError(t, json.Unmarshal(body, &conv))
				assert.Equal(t, "test", conv.AnchorID,
					"conversation must echo the requested log_id as anchor")
				assert.NotNil(t, conv.Entries)
			},
		},
		{
			name:     "models returns provider-tagged model list",
			endpoint: "/admin/models",
			check: func(t *testing.T, body []byte) {
				var models []providers.ModelWithProvider
				require.NoError(t, json.Unmarshal(body, &models))
				require.NotEmpty(t, models, "registered test provider should expose at least one model")
				for _, m := range models {
					assert.NotEmpty(t, m.Model.ID, "every model should have an id")
					assert.NotEmpty(t, m.ProviderType, "every model should have a provider_type")
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.endpoint, func(t *testing.T) {
			resp, err := http.Get(ts.URL + tc.endpoint)
			require.NoError(t, err)
			defer closeBody(resp)

			require.Equal(t, http.StatusOK, resp.StatusCode, "endpoint %s should return 200", tc.endpoint)

			body, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			require.True(t, json.Valid(body), "response should be valid JSON for %s, got: %s", tc.endpoint, string(body))

			tc.check(t, body)
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

	t.Run("dashboard returns 200 HTML with expected markup", func(t *testing.T) {
		resp, err := http.Get(ts.URL + "/admin/dashboard")
		require.NoError(t, err)
		defer closeBody(resp)

		require.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Contains(t, resp.Header.Get("Content-Type"), "text/html")

		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		html := string(body)

		// Guard against regressions that return a 200 with an empty/placeholder
		// document. The dashboard layout pins these markers.
		assert.Contains(t, html, "<title>GoModel Dashboard</title>",
			"dashboard HTML should carry the expected <title>")
		assert.Contains(t, html, "css/dashboard.css",
			"dashboard HTML should reference its stylesheet bundle")
		assert.Contains(t, html, "js/dashboard.js",
			"dashboard HTML should reference its script bundle")
	})

	t.Run("static CSS returns 200 with css content", func(t *testing.T) {
		resp, err := http.Get(ts.URL + "/admin/static/css/dashboard.css")
		require.NoError(t, err)
		defer closeBody(resp)

		require.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Contains(t, resp.Header.Get("Content-Type"), "text/css",
			"static CSS asset must be served with a CSS content-type")

		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		assert.NotEmpty(t, body, "CSS bundle must not be empty")
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

	usageFixture := setupSQLiteUsageFixture(t)
	ts := setupE2EAdminServer(t, e2eServerOptions{
		adminUsageReader: usageFixture.reader,
		usageLogger:      usageFixture.logger,
	})
	defer ts.Close()

	// Mock provider usage is 10 input + 20 output tokens per request, and this test sends 2 requests.
	requestWindowStart := time.Now().UTC()
	for range expectedRequests {
		resp := sendJSONRequest(t, ts.URL+chatCompletionsPath, defaultChatReq("Hello usage"))
		require.Equal(t, http.StatusOK, resp.StatusCode)
		closeBody(resp)
	}
	usageFixture.flush(t)
	requestWindowEnd := time.Now().UTC()
	expectedDailyDates := []string{requestWindowStart.Format("2006-01-02")}
	if endDate := requestWindowEnd.Format("2006-01-02"); endDate != expectedDailyDates[0] {
		expectedDailyDates = append(expectedDailyDates, endDate)
	}

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

		var matchedEntries []usage.DailyUsage
		for i := range daily {
			if slices.Contains(expectedDailyDates, daily[i].Date) {
				matchedEntries = append(matchedEntries, daily[i])
			}
		}
		require.NotEmpty(t, matchedEntries, "expected daily usage entry for one of %v", expectedDailyDates)

		var actualRequests int
		var actualInputTokens int64
		var actualOutputTokens int64
		var actualTotalTokens int64
		for _, entry := range matchedEntries {
			actualRequests += entry.Requests
			actualInputTokens += entry.InputTokens
			actualOutputTokens += entry.OutputTokens
			actualTotalTokens += entry.TotalTokens
		}

		assert.Equal(t, expectedRequests, actualRequests)
		assert.Equal(t, expectedInputTokens, actualInputTokens)
		assert.Equal(t, expectedOutputTokens, actualOutputTokens)
		assert.Equal(t, expectedTotalTokens, actualTotalTokens)
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
