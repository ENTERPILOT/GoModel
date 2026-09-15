package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/budget"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/ratelimit"
	"github.com/enterpilot/gomodel/internal/usage"
)

type fakeUsageSummarizer struct {
	summary   *usage.UsageSummary
	err       error
	gotParams usage.UsageQueryParams
}

func (f *fakeUsageSummarizer) GetSummary(_ context.Context, params usage.UsageQueryParams) (*usage.UsageSummary, error) {
	f.gotParams = params
	return f.summary, f.err
}

// fakeBudgetStatusChecker enforces nothing and reports canned statuses,
// satisfying both BudgetChecker and the status upgrade interface.
type fakeBudgetStatusChecker struct {
	results     []budget.CheckResult
	err         error
	gotSubjects budget.Subjects
}

func (f *fakeBudgetStatusChecker) Check(context.Context, budget.Subjects, time.Time) error {
	return nil
}

func (f *fakeBudgetStatusChecker) StatusesFor(_ context.Context, subjects budget.Subjects, _ time.Time) ([]budget.CheckResult, error) {
	f.gotSubjects = subjects
	return f.results, f.err
}

type fakeRateLimiterWithStatus struct {
	statuses []ratelimit.Status
	gotPath  string
}

func (f *fakeRateLimiterWithStatus) Acquire(ratelimit.Subjects, time.Time) (*ratelimit.Reservation, error) {
	return &ratelimit.Reservation{}, nil
}

func (f *fakeRateLimiterWithStatus) RouteAvailable(string, string) bool { return true }

func (f *fakeRateLimiterWithStatus) StatusesForUserPath(userPath string, _ time.Time) []ratelimit.Status {
	f.gotPath = userPath
	return f.statuses
}

func getUsageStatus(t *testing.T, cfg *Config, target string, headers map[string]string) (*httptest.ResponseRecorder, usageStatusResponse) {
	t.Helper()
	srv := New(&mockProvider{}, cfg)
	req := httptest.NewRequest(http.MethodGet, target, nil)
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	var body usageStatusResponse
	if rec.Code == http.StatusOK {
		err := json.Unmarshal(rec.Body.Bytes(), &body)
		require.NoError(t, err, "decode body: %v (body: %s)", err, rec.Body.String())
	}
	return rec, body
}

func TestUsageStatusReportsManagedKeyPath(t *testing.T) {
	requests := int64(7)
	summarizer := &fakeUsageSummarizer{summary: &usage.UsageSummary{TotalRequests: 7, TotalTokens: 1234}}
	budgets := &fakeBudgetStatusChecker{results: []budget.CheckResult{{
		Budget:      budget.Budget{Scope: budget.ScopeUserPath, Subject: "/team", PeriodSeconds: budget.PeriodDailySeconds, Amount: 10},
		PeriodStart: time.Date(2026, time.July, 6, 0, 0, 0, 0, time.UTC),
		PeriodEnd:   time.Date(2026, time.July, 7, 0, 0, 0, 0, time.UTC),
		Spent:       12,
		HasUsage:    true,
		Remaining:   -2,
	}}}
	limiter := &fakeRateLimiterWithStatus{statuses: []ratelimit.Status{{
		Rule:              ratelimit.Rule{Scope: ratelimit.ScopeUserPath, Subject: "/team", PeriodSeconds: 60, MaxRequests: &requests},
		RequestsUsed:      3,
		RequestsRemaining: &requests,
	}}}

	cfg := &Config{
		Authenticator: mockAuthenticator{
			enabled:   true,
			tokenToID: map[string]string{"sk_gom_test": "key-1"},
			tokenPath: map[string]string{"sk_gom_test": "/team/alice"},
		},
		UsageSummarizer: summarizer,
		BudgetChecker:   budgets,
		RateLimiter:     limiter,
	}

	rec, body := getUsageStatus(t, cfg, "/v1/usage", map[string]string{"Authorization": "Bearer sk_gom_test"})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, "/team/alice", body.UserPath)

	for name, got := range map[string]string{
		"summarizer": summarizer.gotParams.UserPath,
		"budgets":    budgets.gotSubjects.UserPath,
		"ratelimits": limiter.gotPath,
	} {
		require.Equal(t, "/team/alice", got, "%s queried path %q, want /team/alice", name, got)
	}

	require.NotNil(t, body.Usage)
	require.Equal(t, 7, body.Usage.TotalRequests)
	require.Equal(t, int64(1234), body.Usage.TotalTokens)
	window := summarizer.gotParams.EndDate.Sub(summarizer.gotParams.StartDate)
	require.Equal(t, 29*24*time.Hour, window)
	require.Len(t, body.Budgets, 1)

	b := body.Budgets[0]
	require.Equal(t, "/team", b.UserPath)
	require.Equal(t, "daily", b.PeriodLabel)
	require.Equal(t, float64(12), b.Spent)
	require.Equal(t, float64(-2), b.Remaining)
	require.True(t, b.Exceeded, "daily /team budget should be exceeded: %+v", b)
	require.Len(t, body.RateLimits, 1)

	rl := body.RateLimits[0]
	require.Equal(t, "/team", rl.UserPath)
	require.Equal(t, "minute", rl.PeriodLabel)
	require.Equal(t, int64(3), rl.RequestsUsed)
	require.NotNil(t, rl.MaxRequests)
	require.Equal(t, int64(7), *rl.MaxRequests, "expected the /team minute rule: %+v", rl)
}

// A per-child template reports the parent it was declared on as `subject`
// while `user_path` names the child partition the caller was actually
// charged against; a plain rule reports the same path for both.
func TestUsageStatusReportsPerChildSubjects(t *testing.T) {
	requests := int64(7)
	tests := []struct {
		name             string
		perChild         bool
		effectiveSubject string
		wantUserPath     string
	}{
		{
			name:         "plain user path rule",
			wantUserPath: "/team",
		},
		{
			name:             "per-child template",
			perChild:         true,
			effectiveSubject: "/team/alice",
			wantUserPath:     "/team/alice",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{
				UsageSummarizer: &fakeUsageSummarizer{summary: &usage.UsageSummary{}},
				BudgetChecker: &fakeBudgetStatusChecker{results: []budget.CheckResult{{
					Budget: budget.Budget{
						Scope:            budget.ScopeUserPath,
						Subject:          "/team",
						PerChild:         tt.perChild,
						EffectiveSubject: tt.effectiveSubject,
						PeriodSeconds:    budget.PeriodDailySeconds,
						Amount:           10,
					},
				}}},
				RateLimiter: &fakeRateLimiterWithStatus{statuses: []ratelimit.Status{{
					Rule: ratelimit.Rule{
						Scope:            ratelimit.ScopeUserPath,
						Subject:          "/team",
						PerChild:         tt.perChild,
						EffectiveSubject: tt.effectiveSubject,
						PeriodSeconds:    60,
						MaxRequests:      &requests,
					},
				}}},
			}

			rec, body := getUsageStatus(t, cfg, "/v1/usage", map[string]string{core.UserPathHeader: "/team/alice"})
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			require.Len(t, body.Budgets, 1)

			b := body.Budgets[0]
			assert.Equal(t, "/team", b.Subject)
			assert.Equal(t, tt.perChild, b.PerChild)
			assert.Equal(t, tt.wantUserPath, b.UserPath)
			require.Len(t, body.RateLimits, 1)

			rl := body.RateLimits[0]
			assert.Equal(t, "/team", rl.Subject)
			assert.Equal(t, tt.perChild, rl.PerChild)
			assert.Equal(t, tt.wantUserPath, rl.UserPath)
		})
	}
}

func TestUsageStatusDerivedFields(t *testing.T) {
	maxRequests := int64(10)
	maxTokens := int64(100)
	requestsLeft := int64(7)
	tokensLeft := int64(0)
	concurrentMax := int64(2)
	concurrentLeft := int64(0)
	now := time.Now().UTC()
	windowEnd := now.Add(45 * time.Second)
	periodEnd := now.Add(90 * time.Minute)

	budgets := &fakeBudgetStatusChecker{results: []budget.CheckResult{{
		Budget:      budget.Budget{Scope: budget.ScopeUserPath, Subject: "/team", PeriodSeconds: budget.PeriodDailySeconds, Amount: 10},
		PeriodStart: periodEnd.Add(-24 * time.Hour),
		PeriodEnd:   periodEnd,
		Spent:       12,
		HasUsage:    true,
		Remaining:   -2,
	}}}
	limiter := &fakeRateLimiterWithStatus{statuses: []ratelimit.Status{
		{
			Rule:              ratelimit.Rule{Scope: ratelimit.ScopeUserPath, Subject: "/team", PeriodSeconds: 60, MaxRequests: &maxRequests, MaxTokens: &maxTokens},
			WindowStart:       windowEnd.Add(-time.Minute),
			WindowEnd:         windowEnd,
			RequestsUsed:      3,
			RequestsRemaining: &requestsLeft,
			TokensUsed:        120,
			TokensRemaining:   &tokensLeft,
		},
		{
			Rule:              ratelimit.Rule{Scope: ratelimit.ScopeUserPath, Subject: "/team", PeriodSeconds: ratelimit.PeriodConcurrent, MaxRequests: &concurrentMax},
			InFlight:          2,
			RequestsRemaining: &concurrentLeft,
		},
	}}

	rec, body := getUsageStatus(t, &Config{BudgetChecker: budgets, RateLimiter: limiter}, "/v1/usage", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Len(t, body.Budgets, 1)

	b := body.Budgets[0]
	require.Equal(t, 1.2, b.UsageRatio)
	require.Greater(t, b.ResetsInSeconds, int64(85*60))
	require.LessOrEqual(t, b.ResetsInSeconds, int64(90*60))
	require.Len(t, body.RateLimits, 2)

	windowed, concurrent := body.RateLimits[0], body.RateLimits[1]
	require.NotNil(t, windowed.RequestsUsageRatio)
	require.Equal(t, 3.0/10.0, *windowed.RequestsUsageRatio)
	require.NotNil(t, windowed.TokensUsageRatio)
	require.Equal(t, 1.2, *windowed.TokensUsageRatio)
	require.True(t, windowed.Exhausted)
	require.NotNil(t, windowed.ResetsInSeconds)
	require.Greater(t, *windowed.ResetsInSeconds, int64(0))
	require.LessOrEqual(t, *windowed.ResetsInSeconds, int64(45))
	require.NotNil(t, concurrent.RequestsUsageRatio)
	require.Equal(t, 1.0, *concurrent.RequestsUsageRatio)
	require.True(t, concurrent.Exhausted)
	require.Nil(t, concurrent.ResetsInSeconds)
	require.Nil(t, concurrent.TokensUsageRatio)
}

func TestUsageStatusMasterKeyUsesHeaderPath(t *testing.T) {
	summarizer := &fakeUsageSummarizer{summary: &usage.UsageSummary{}}
	cfg := &Config{MasterKey: "secret", UsageSummarizer: summarizer}

	rec, body := getUsageStatus(t, cfg, "/v1/usage?start_date=2026-07-01&end_date=2026-07-06", map[string]string{
		"Authorization":       "Bearer secret",
		"X-GoModel-User-Path": "/team",
	})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, "/team", body.UserPath)
	require.NotNil(t, body.Usage)
	require.Equal(t, "2026-07-01", body.Usage.StartDate)
	require.Equal(t, "2026-07-06", body.Usage.EndDate)
	require.Equal(t, "/team", summarizer.gotParams.UserPath)
}

func TestUsageStatusCacheMode(t *testing.T) {
	tests := []struct {
		name   string
		target string
		want   string
	}{
		{name: "absent leaves the reader default", target: "/v1/usage", want: ""},
		{name: "cached", target: "/v1/usage?cache_mode=cached", want: "cached"},
		{name: "all", target: "/v1/usage?cache_mode=all", want: "all"},
		{name: "surrounding whitespace is trimmed", target: "/v1/usage?cache_mode=%20all%20", want: "all"},
		{name: "unknown value reaches the reader, which defaults it", target: "/v1/usage?cache_mode=bogus", want: "bogus"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			summarizer := &fakeUsageSummarizer{summary: &usage.UsageSummary{}}
			rec, _ := getUsageStatus(t, &Config{UsageSummarizer: summarizer}, tc.target, nil)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			require.Equal(t, tc.want, summarizer.gotParams.CacheMode)
		})
	}
}

func TestUsageStatusWithoutDependenciesReturnsEmptyStatus(t *testing.T) {
	rec, body := getUsageStatus(t, &Config{}, "/v1/usage", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, "/", body.UserPath)
	require.Nil(t, body.Usage)
	require.NotNil(t, body.Budgets)
	require.Empty(t, body.Budgets)
	require.NotNil(t, body.RateLimits)
	require.Empty(t, body.RateLimits)
}

func TestUsageStatusRequiresAuth(t *testing.T) {
	rec, _ := getUsageStatus(t, &Config{MasterKey: "secret"}, "/v1/usage", nil)
	require.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestUsageStatusRejectsInvalidDates(t *testing.T) {
	for name, target := range map[string]string{
		"malformed start":       "/v1/usage?start_date=garbage",
		"inverted range":        "/v1/usage?start_date=2026-07-06&end_date=2026-07-01",
		"range beyond 365 days": "/v1/usage?start_date=2020-01-01&end_date=2026-01-01",
		"malformed days":        "/v1/usage?days=abc",
		"non-positive days":     "/v1/usage?days=-5",
	} {
		t.Run(name, func(t *testing.T) {
			rec, _ := getUsageStatus(t, &Config{}, target, nil)
			require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
		})
	}
}

func TestUsageStatusClampsOversizedDays(t *testing.T) {
	summarizer := &fakeUsageSummarizer{summary: &usage.UsageSummary{}}
	rec, _ := getUsageStatus(t, &Config{UsageSummarizer: summarizer}, "/v1/usage?days=1000", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	window := summarizer.gotParams.EndDate.Sub(summarizer.gotParams.StartDate)
	require.Equal(t, 364*24*time.Hour, window)
}

func TestUsageStatusRejectsInvalidUserPathHeader(t *testing.T) {
	rec, _ := getUsageStatus(t, &Config{}, "/v1/usage", map[string]string{
		"X-GoModel-User-Path": "/team/../secrets",
	})
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
}

func TestUsageStatusBudgetErrors(t *testing.T) {
	t.Run("unavailable budgets degrade to empty", func(t *testing.T) {
		budgets := &fakeBudgetStatusChecker{err: budget.ErrUnavailable}
		rec, body := getUsageStatus(t, &Config{BudgetChecker: budgets}, "/v1/usage", nil)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		require.Empty(t, body.Budgets)
	})
	t.Run("store failure surfaces as 503", func(t *testing.T) {
		budgets := &fakeBudgetStatusChecker{err: errors.New("store down")}
		rec, _ := getUsageStatus(t, &Config{BudgetChecker: budgets}, "/v1/usage", nil)
		require.Equal(t, http.StatusServiceUnavailable, rec.Code, rec.Body.String())
	})
}

func TestUsageStatusSummaryErrorSurfacesAs503(t *testing.T) {
	summarizer := &fakeUsageSummarizer{err: errors.New("query failed")}
	rec, _ := getUsageStatus(t, &Config{UsageSummarizer: summarizer}, "/v1/usage", nil)
	require.Equal(t, http.StatusServiceUnavailable, rec.Code, rec.Body.String())
}

// TestUsageStatusScopedKeyIgnoresHeaderPath pins that a key bound to a user
// path reports its own subtree even when the request names another path in
// the header: the bound path is the key's access scope and the header cannot
// widen or move it.
func TestUsageStatusScopedKeyIgnoresHeaderPath(t *testing.T) {
	summarizer := &fakeUsageSummarizer{summary: &usage.UsageSummary{}}
	cfg := &Config{
		Authenticator: mockAuthenticator{
			enabled:   true,
			tokenToID: map[string]string{"sk_gom_test": "key-1"},
			tokenPath: map[string]string{"sk_gom_test": "/team/alice"},
		},
		UsageSummarizer: summarizer,
	}

	for _, header := range []string{"/team/bob", "/", "/team"} {
		rec, body := getUsageStatus(t, cfg, "/v1/usage", map[string]string{
			"Authorization":     "Bearer sk_gom_test",
			core.UserPathHeader: header,
		})
		require.Equal(t, http.StatusOK, rec.Code, "header %q: %s", header, rec.Body.String())
		require.Equal(t, "/team/alice", body.UserPath)
		require.Equal(t, "/team/alice", summarizer.gotParams.UserPath, "header %q: reported %q, queried %q, want /team/alice for both", header, body.UserPath, summarizer.gotParams.UserPath)
	}
}
