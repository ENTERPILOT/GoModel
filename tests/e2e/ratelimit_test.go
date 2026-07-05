//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"

	"gomodel/internal/admin"
	"gomodel/internal/core"
	"gomodel/internal/ratelimit"
	"gomodel/internal/usage"
)

func setupRateLimitService(t *testing.T, rules []ratelimit.Rule) *ratelimit.Service {
	t.Helper()

	db, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })

	store, err := ratelimit.NewSQLiteStore(db)
	require.NoError(t, err)

	service, err := ratelimit.NewService(context.Background(), store)
	require.NoError(t, err)
	if len(rules) > 0 {
		require.NoError(t, service.UpsertRules(context.Background(), rules))
	}
	return service
}

func rateLimitInt64(v int64) *int64 { return &v }

func TestRateLimitRequestEnforcement_E2E(t *testing.T) {
	mockServer.ResetRequests()
	service := setupRateLimitService(t, []ratelimit.Rule{{
		Subject:       "/team/rl",
		PeriodSeconds: ratelimit.PeriodMinuteSeconds,
		MaxRequests:   rateLimitInt64(2),
		Source:        ratelimit.SourceManual,
	}})

	ts := httptest.NewServer(setupE2EServer(t, e2eServerOptions{rateLimiter: service}))
	defer ts.Close()

	for i := 0; i < 2; i++ {
		resp := sendBudgetChatRequest(t, ts.URL, "rate limit ok", "rl-ok-"+strconv.Itoa(i), "/team/rl/app")
		require.Equal(t, http.StatusOK, resp.StatusCode)
		require.Equal(t, "2", resp.Header.Get("x-ratelimit-limit-requests"))
		require.Equal(t, strconv.Itoa(1-i), resp.Header.Get("x-ratelimit-remaining-requests"))
		require.NotEmpty(t, resp.Header.Get("x-ratelimit-reset-requests"))
		closeBody(resp)
	}

	blocked := sendBudgetChatRequest(t, ts.URL, "rate limit blocked", "rl-blocked", "/team/rl/app")
	defer closeBody(blocked)
	require.Equal(t, http.StatusTooManyRequests, blocked.StatusCode)
	require.NotEmpty(t, blocked.Header.Get("Retry-After"))
	require.Equal(t, "0", blocked.Header.Get("x-ratelimit-remaining-requests"))

	var envelope core.OpenAIErrorEnvelope
	require.NoError(t, json.NewDecoder(blocked.Body).Decode(&envelope))
	require.Equal(t, core.ErrorTypeRateLimit, envelope.Error.Type)
	require.NotNil(t, envelope.Error.Code)
	require.Equal(t, "rate_limit_exceeded", *envelope.Error.Code)
	require.Contains(t, envelope.Error.Message, "/team/rl")
	require.Len(t, mockServer.Requests(), 2, "blocked request must not reach upstream provider")

	// An unmatched user path is unaffected.
	other := sendBudgetChatRequest(t, ts.URL, "other path", "rl-other", "/other")
	require.Equal(t, http.StatusOK, other.StatusCode)
	require.Empty(t, other.Header.Get("x-ratelimit-limit-requests"))
	closeBody(other)
}

func TestRateLimitTokenEnforcement_E2E(t *testing.T) {
	mockServer.ResetRequests()
	service := setupRateLimitService(t, []ratelimit.Rule{{
		Subject:       "/team/tokens",
		PeriodSeconds: ratelimit.PeriodMinuteSeconds,
		MaxTokens:     rateLimitInt64(5),
		Source:        ratelimit.SourceManual,
	}})

	// Mirror the app wiring: the usage tap feeds recorded token counts into
	// the limiter before delegating to the real usage logger.
	db, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	usageStore, err := usage.NewSQLiteStore(db, 0)
	require.NoError(t, err)
	usageCfg := usage.DefaultConfig()
	usageCfg.Enabled = true
	usageCfg.FlushInterval = 10 * time.Millisecond
	usageLogger := usage.NewLogger(usageStore, usageCfg)
	t.Cleanup(func() { usageLogger.Close() })

	ts := httptest.NewServer(setupE2EServer(t, e2eServerOptions{
		rateLimiter: service,
		usageLogger: ratelimit.NewUsageTap(usageLogger, service),
	}))
	defer ts.Close()

	// Tokens are unknown before the first response, so it passes.
	first := sendBudgetChatRequest(t, ts.URL, "token limit first", "rl-tokens-first", "/team/tokens/app")
	require.Equal(t, http.StatusOK, first.StatusCode)
	closeBody(first)

	// Wait until the response's token usage lands in the limiter window.
	require.Eventually(t, func() bool {
		statuses := service.Statuses(time.Now().UTC())
		return len(statuses) == 1 && statuses[0].TokensUsed >= 5
	}, 2*time.Second, 20*time.Millisecond)

	second := sendBudgetChatRequest(t, ts.URL, "token limit second", "rl-tokens-second", "/team/tokens/app")
	defer closeBody(second)
	require.Equal(t, http.StatusTooManyRequests, second.StatusCode)

	var envelope core.OpenAIErrorEnvelope
	require.NoError(t, json.NewDecoder(second.Body).Decode(&envelope))
	require.NotNil(t, envelope.Error.Code)
	require.Equal(t, "rate_limit_exceeded", *envelope.Error.Code)
	require.Contains(t, envelope.Error.Message, "token limit")
	require.Len(t, mockServer.Requests(), 1, "token-limited request must not reach upstream provider")
}

func TestRateLimitConcurrencyEnforcement_E2E(t *testing.T) {
	mockServer.ResetRequests()
	service := setupRateLimitService(t, []ratelimit.Rule{{
		Subject:       "/team/cc",
		PeriodSeconds: ratelimit.PeriodConcurrent,
		MaxRequests:   rateLimitInt64(1),
		Source:        ratelimit.SourceManual,
	}})

	// Hold the first request in flight long enough for the overlap.
	mockServer.SetResponseDelay(750 * time.Millisecond)
	t.Cleanup(func() { mockServer.SetResponseDelay(0) })

	ts := httptest.NewServer(setupE2EServer(t, e2eServerOptions{rateLimiter: service}))
	defer ts.Close()

	// require must stay on the test goroutine, so the held request reports
	// its outcome through channels.
	type asyncResult struct {
		resp *http.Response
		err  error
	}
	firstDone := make(chan asyncResult, 1)
	go func() {
		payload, err := json.Marshal(defaultChatReq("concurrency first"))
		if err != nil {
			firstDone <- asyncResult{err: err}
			return
		}
		req, err := http.NewRequest(http.MethodPost, ts.URL+chatCompletionsPath, bytes.NewReader(payload))
		if err != nil {
			firstDone <- asyncResult{err: err}
			return
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Request-ID", "rl-cc-first")
		req.Header.Set("X-GoModel-User-Path", "/team/cc/app")
		resp, err := http.DefaultClient.Do(req)
		firstDone <- asyncResult{resp: resp, err: err}
	}()

	// Wait until the first request actually holds the concurrency slot.
	require.Eventually(t, func() bool {
		statuses := service.Statuses(time.Now().UTC())
		return len(statuses) == 1 && statuses[0].InFlight == 1
	}, 2*time.Second, 10*time.Millisecond)

	blocked := sendBudgetChatRequest(t, ts.URL, "concurrency blocked", "rl-cc-blocked", "/team/cc/app")
	defer closeBody(blocked)
	require.Equal(t, http.StatusTooManyRequests, blocked.StatusCode)
	require.NotEmpty(t, blocked.Header.Get("Retry-After"))
	var envelope core.OpenAIErrorEnvelope
	require.NoError(t, json.NewDecoder(blocked.Body).Decode(&envelope))
	require.NotNil(t, envelope.Error.Code)
	require.Equal(t, "rate_limit_exceeded", *envelope.Error.Code)
	require.Contains(t, envelope.Error.Message, "concurrent request limit")

	first := <-firstDone
	require.NoError(t, first.err)
	require.Equal(t, http.StatusOK, first.resp.StatusCode)
	closeBody(first.resp)
	require.Len(t, mockServer.Requests(), 1, "blocked request must not reach upstream provider")

	// The slot frees once the held request completes.
	mockServer.SetResponseDelay(0)
	after := sendBudgetChatRequest(t, ts.URL, "concurrency after", "rl-cc-after", "/team/cc/app")
	defer closeBody(after)
	require.Equal(t, http.StatusOK, after.StatusCode)
}

func TestRateLimitProviderScopeEnforcement_E2E(t *testing.T) {
	mockServer.ResetRequests()
	// The e2e fixture registers the mock as provider type/name "test"; the
	// rule caps that provider across all consumers and models.
	service := setupRateLimitService(t, []ratelimit.Rule{{
		Scope:         ratelimit.ScopeProvider,
		Subject:       "test",
		PeriodSeconds: ratelimit.PeriodMinuteSeconds,
		MaxRequests:   rateLimitInt64(1),
		Source:        ratelimit.SourceManual,
	}})

	ts := httptest.NewServer(setupE2EServer(t, e2eServerOptions{rateLimiter: service}))
	defer ts.Close()

	first := sendBudgetChatRequest(t, ts.URL, "provider limit ok", "rl-prov-ok", "/team/a")
	require.Equal(t, http.StatusOK, first.StatusCode)
	require.Equal(t, "1", first.Header.Get("x-ratelimit-limit-requests"))
	closeBody(first)

	// A different consumer path shares the provider counter.
	blocked := sendBudgetChatRequest(t, ts.URL, "provider limit blocked", "rl-prov-blocked", "/team/b")
	defer closeBody(blocked)
	require.Equal(t, http.StatusTooManyRequests, blocked.StatusCode)
	require.NotEmpty(t, blocked.Header.Get("Retry-After"))

	var envelope core.OpenAIErrorEnvelope
	require.NoError(t, json.NewDecoder(blocked.Body).Decode(&envelope))
	require.NotNil(t, envelope.Error.Code)
	require.Equal(t, "rate_limit_exceeded", *envelope.Error.Code)
	require.Contains(t, envelope.Error.Message, "provider test")
	require.Len(t, mockServer.Requests(), 1, "blocked request must not reach upstream provider")
}

func TestRateLimitAdminEndpoints_E2E(t *testing.T) {
	service := setupRateLimitService(t, nil)
	ts := httptest.NewServer(setupE2EServer(t, e2eServerOptions{
		adminEndpointsEnabled: true,
		adminOptions:          []admin.Option{admin.WithRateLimits(service)},
		rateLimiter:           service,
	}))
	defer ts.Close()

	putResp := sendBudgetJSONRequest(t, http.MethodPut, ts.URL+"/admin/rate-limits", map[string]any{
		"user_path":    "/team/admin",
		"limit_key":    map[string]any{"period": "minute"},
		"max_requests": 100,
		"max_tokens":   50000,
	})
	require.Equal(t, http.StatusOK, putResp.StatusCode)
	closeBody(putResp)

	listResp := sendBudgetJSONRequest(t, http.MethodGet, ts.URL+"/admin/rate-limits", nil)
	require.Equal(t, http.StatusOK, listResp.StatusCode)
	var listBody struct {
		RateLimits []struct {
			UserPath          string `json:"user_path"`
			PeriodSeconds     int64  `json:"period_seconds"`
			PeriodLabel       string `json:"period_label"`
			MaxRequests       *int64 `json:"max_requests"`
			MaxTokens         *int64 `json:"max_tokens"`
			RequestsRemaining *int64 `json:"requests_remaining"`
		} `json:"rate_limits"`
	}
	require.NoError(t, json.NewDecoder(listResp.Body).Decode(&listBody))
	closeBody(listResp)
	require.Len(t, listBody.RateLimits, 1)
	require.Equal(t, "/team/admin", listBody.RateLimits[0].UserPath)
	require.Equal(t, int64(60), listBody.RateLimits[0].PeriodSeconds)
	require.Equal(t, "minute", listBody.RateLimits[0].PeriodLabel)
	require.NotNil(t, listBody.RateLimits[0].MaxRequests)
	require.Equal(t, int64(100), *listBody.RateLimits[0].MaxRequests)
	require.NotNil(t, listBody.RateLimits[0].RequestsRemaining)
	require.Equal(t, int64(100), *listBody.RateLimits[0].RequestsRemaining)

	resetResp := sendBudgetJSONRequest(t, http.MethodPost, ts.URL+"/admin/rate-limits/reset-one", map[string]any{
		"user_path": "/team/admin",
		"period":    "minute",
	})
	require.Equal(t, http.StatusOK, resetResp.StatusCode)
	closeBody(resetResp)

	deleteResp := sendBudgetJSONRequest(t, http.MethodDelete, ts.URL+"/admin/rate-limits", map[string]any{
		"user_path": "/team/admin",
		"limit_key": map[string]any{"period": "minute"},
	})
	require.Equal(t, http.StatusOK, deleteResp.StatusCode)
	closeBody(deleteResp)
	require.Empty(t, service.Rules())
}
