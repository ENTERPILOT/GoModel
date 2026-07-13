package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/enterpilot/gomodel/internal/budget"
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
	results []budget.CheckResult
	err     error
	gotPath string
}

func (f *fakeBudgetStatusChecker) Check(context.Context, string, time.Time) error { return nil }

func (f *fakeBudgetStatusChecker) StatusesForPath(_ context.Context, userPath string, _ time.Time) ([]budget.CheckResult, error) {
	f.gotPath = userPath
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
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode body: %v (body: %s)", err, rec.Body.String())
		}
	}
	return rec, body
}

func TestUsageStatusReportsManagedKeyPath(t *testing.T) {
	requests := int64(7)
	summarizer := &fakeUsageSummarizer{summary: &usage.UsageSummary{TotalRequests: 7, TotalTokens: 1234}}
	budgets := &fakeBudgetStatusChecker{results: []budget.CheckResult{{
		Budget:      budget.Budget{UserPath: "/team", PeriodSeconds: budget.PeriodDailySeconds, Amount: 10},
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
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	if body.UserPath != "/team/alice" {
		t.Fatalf("user_path = %q, want /team/alice", body.UserPath)
	}
	for name, got := range map[string]string{
		"summarizer": summarizer.gotParams.UserPath,
		"budgets":    budgets.gotPath,
		"ratelimits": limiter.gotPath,
	} {
		if got != "/team/alice" {
			t.Fatalf("%s queried path %q, want /team/alice", name, got)
		}
	}

	if body.Usage == nil || body.Usage.TotalRequests != 7 || body.Usage.TotalTokens != 1234 {
		t.Fatalf("usage = %+v, want 7 requests / 1234 tokens", body.Usage)
	}
	if window := summarizer.gotParams.EndDate.Sub(summarizer.gotParams.StartDate); window != 29*24*time.Hour {
		t.Fatalf("default window = %s, want 29 days between inclusive bounds", window)
	}

	if len(body.Budgets) != 1 {
		t.Fatalf("budgets = %d, want 1", len(body.Budgets))
	}
	b := body.Budgets[0]
	if b.UserPath != "/team" || b.PeriodLabel != "daily" || b.Spent != 12 || b.Remaining != -2 || !b.Exceeded {
		t.Fatalf("budget status = %+v, want exceeded daily /team budget", b)
	}

	if len(body.RateLimits) != 1 {
		t.Fatalf("rate_limits = %d, want 1", len(body.RateLimits))
	}
	rl := body.RateLimits[0]
	if rl.UserPath != "/team" || rl.PeriodLabel != "minute" || rl.RequestsUsed != 3 || rl.MaxRequests == nil || *rl.MaxRequests != 7 {
		t.Fatalf("rate limit status = %+v, want /team minute rule with 3 used", rl)
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
		Budget:      budget.Budget{UserPath: "/team", PeriodSeconds: budget.PeriodDailySeconds, Amount: 10},
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
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}

	if len(body.Budgets) != 1 {
		t.Fatalf("budgets = %d, want 1", len(body.Budgets))
	}
	b := body.Budgets[0]
	if b.UsageRatio != 1.2 {
		t.Fatalf("budget usage_ratio = %v, want 1.2 (unclamped)", b.UsageRatio)
	}
	if b.ResetsInSeconds <= 85*60 || b.ResetsInSeconds > 90*60 {
		t.Fatalf("budget resets_in_seconds = %d, want ~90 minutes", b.ResetsInSeconds)
	}

	if len(body.RateLimits) != 2 {
		t.Fatalf("rate_limits = %d, want 2", len(body.RateLimits))
	}
	windowed, concurrent := body.RateLimits[0], body.RateLimits[1]
	if windowed.RequestsUsageRatio == nil || *windowed.RequestsUsageRatio != 3.0/10.0 {
		t.Fatalf("windowed requests_usage_ratio = %v, want 0.3", windowed.RequestsUsageRatio)
	}
	if windowed.TokensUsageRatio == nil || *windowed.TokensUsageRatio != 1.2 {
		t.Fatalf("windowed tokens_usage_ratio = %v, want 1.2 (unclamped)", windowed.TokensUsageRatio)
	}
	if !windowed.Exhausted {
		t.Fatal("windowed rule with zero tokens remaining must be exhausted")
	}
	if windowed.ResetsInSeconds == nil || *windowed.ResetsInSeconds <= 0 || *windowed.ResetsInSeconds > 45 {
		t.Fatalf("windowed resets_in_seconds = %v, want within (0, 45]", windowed.ResetsInSeconds)
	}
	if concurrent.RequestsUsageRatio == nil || *concurrent.RequestsUsageRatio != 1.0 {
		t.Fatalf("concurrent requests_usage_ratio = %v, want 1.0 (from in-flight)", concurrent.RequestsUsageRatio)
	}
	if !concurrent.Exhausted {
		t.Fatal("concurrent rule at capacity must be exhausted")
	}
	if concurrent.ResetsInSeconds != nil || concurrent.TokensUsageRatio != nil {
		t.Fatalf("concurrent rule resets/tokens ratio = %v/%v, want both omitted", concurrent.ResetsInSeconds, concurrent.TokensUsageRatio)
	}
}

func TestUsageStatusMasterKeyUsesHeaderPath(t *testing.T) {
	summarizer := &fakeUsageSummarizer{summary: &usage.UsageSummary{}}
	cfg := &Config{MasterKey: "secret", UsageSummarizer: summarizer}

	rec, body := getUsageStatus(t, cfg, "/v1/usage?start_date=2026-07-01&end_date=2026-07-06", map[string]string{
		"Authorization":       "Bearer secret",
		"X-GoModel-User-Path": "/team",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	if body.UserPath != "/team" {
		t.Fatalf("user_path = %q, want /team", body.UserPath)
	}
	if body.Usage == nil || body.Usage.StartDate != "2026-07-01" || body.Usage.EndDate != "2026-07-06" {
		t.Fatalf("usage window = %+v, want 2026-07-01..2026-07-06", body.Usage)
	}
	if summarizer.gotParams.UserPath != "/team" {
		t.Fatalf("summarizer path = %q, want /team", summarizer.gotParams.UserPath)
	}
}

func TestUsageStatusWithoutDependenciesReturnsEmptyStatus(t *testing.T) {
	rec, body := getUsageStatus(t, &Config{}, "/v1/usage", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	if body.UserPath != "/" {
		t.Fatalf("user_path = %q, want /", body.UserPath)
	}
	if body.Usage != nil {
		t.Fatalf("usage = %+v, want null without a summarizer", body.Usage)
	}
	if body.Budgets == nil || len(body.Budgets) != 0 || body.RateLimits == nil || len(body.RateLimits) != 0 {
		t.Fatalf("budgets/rate_limits = %v/%v, want empty arrays", body.Budgets, body.RateLimits)
	}
}

func TestUsageStatusRequiresAuth(t *testing.T) {
	rec, _ := getUsageStatus(t, &Config{MasterKey: "secret"}, "/v1/usage", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
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
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (body: %s)", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestUsageStatusClampsOversizedDays(t *testing.T) {
	summarizer := &fakeUsageSummarizer{summary: &usage.UsageSummary{}}
	rec, _ := getUsageStatus(t, &Config{UsageSummarizer: summarizer}, "/v1/usage?days=1000", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	if window := summarizer.gotParams.EndDate.Sub(summarizer.gotParams.StartDate); window != 364*24*time.Hour {
		t.Fatalf("window = %s, want 364 days between inclusive bounds (365-day cap)", window)
	}
}

func TestUsageStatusRejectsInvalidUserPathHeader(t *testing.T) {
	rec, _ := getUsageStatus(t, &Config{}, "/v1/usage", map[string]string{
		"X-GoModel-User-Path": "/team/../secrets",
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body: %s)", rec.Code, rec.Body.String())
	}
}

func TestUsageStatusBudgetErrors(t *testing.T) {
	t.Run("unavailable budgets degrade to empty", func(t *testing.T) {
		budgets := &fakeBudgetStatusChecker{err: budget.ErrUnavailable}
		rec, body := getUsageStatus(t, &Config{BudgetChecker: budgets}, "/v1/usage", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
		}
		if len(body.Budgets) != 0 {
			t.Fatalf("budgets = %v, want empty", body.Budgets)
		}
	})
	t.Run("store failure surfaces as 503", func(t *testing.T) {
		budgets := &fakeBudgetStatusChecker{err: errors.New("store down")}
		rec, _ := getUsageStatus(t, &Config{BudgetChecker: budgets}, "/v1/usage", nil)
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503 (body: %s)", rec.Code, rec.Body.String())
		}
	})
}

func TestUsageStatusSummaryErrorSurfacesAs503(t *testing.T) {
	summarizer := &fakeUsageSummarizer{err: errors.New("query failed")}
	rec, _ := getUsageStatus(t, &Config{UsageSummarizer: summarizer}, "/v1/usage", nil)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (body: %s)", rec.Code, rec.Body.String())
	}
}
