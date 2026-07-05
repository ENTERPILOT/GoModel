package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/labstack/echo/v5"

	"gomodel/internal/core"
	"gomodel/internal/ratelimit"
)

// staticRuleStore serves a fixed rule set so tests can build a real
// ratelimit.Service without a database.
type staticRuleStore struct {
	rules []ratelimit.Rule
}

func (s *staticRuleStore) ListRules(context.Context) ([]ratelimit.Rule, error) {
	return append([]ratelimit.Rule(nil), s.rules...), nil
}
func (s *staticRuleStore) UpsertRules(context.Context, []ratelimit.Rule) error        { return nil }
func (s *staticRuleStore) DeleteRule(context.Context, string, int64) error            { return nil }
func (s *staticRuleStore) ReplaceConfigRules(context.Context, []ratelimit.Rule) error { return nil }
func (s *staticRuleStore) Close() error                                               { return nil }

func newTestRateLimitService(t *testing.T, rules ...ratelimit.Rule) *ratelimit.Service {
	t.Helper()
	normalized := make([]ratelimit.Rule, 0, len(rules))
	for _, rule := range rules {
		item, err := ratelimit.NormalizeRule(rule)
		if err != nil {
			t.Fatalf("NormalizeRule() failed: %v", err)
		}
		normalized = append(normalized, item)
	}
	service, err := ratelimit.NewService(context.Background(), &staticRuleStore{rules: normalized})
	if err != nil {
		t.Fatalf("NewService() failed: %v", err)
	}
	return service
}

func newRateLimitTestContext(userPath string) (*echo.Context, *httptest.ResponseRecorder) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	if userPath != "" {
		req = req.WithContext(core.WithEffectiveUserPath(req.Context(), userPath))
	}
	rec := httptest.NewRecorder()
	return e.NewContext(req, rec), rec
}

func rateLimitRuleWithRequests(path string, maxRequests int64) ratelimit.Rule {
	return ratelimit.Rule{UserPath: path, PeriodSeconds: ratelimit.PeriodMinuteSeconds, MaxRequests: &maxRequests}
}

func TestEnforceRateLimitNilLimiterIsNoop(t *testing.T) {
	c, rec := newRateLimitTestContext("/team")
	release, err := enforceRateLimit(c, nil)
	if err != nil {
		t.Fatalf("enforceRateLimit() error = %v", err)
	}
	release()
	if len(rec.Header()) != 0 {
		t.Fatalf("headers = %v, want none", rec.Header())
	}
}

func TestEnforceRateLimitSetsSuccessHeaders(t *testing.T) {
	service := newTestRateLimitService(t, rateLimitRuleWithRequests("/team", 5))
	c, rec := newRateLimitTestContext("/team/alice")

	release, err := enforceRateLimit(c, service)
	if err != nil {
		t.Fatalf("enforceRateLimit() error = %v", err)
	}
	defer release()

	if got := rec.Header().Get("x-ratelimit-limit-requests"); got != "5" {
		t.Fatalf("x-ratelimit-limit-requests = %q, want 5", got)
	}
	if got := rec.Header().Get("x-ratelimit-remaining-requests"); got != "4" {
		t.Fatalf("x-ratelimit-remaining-requests = %q, want 4", got)
	}
	reset, err := strconv.Atoi(rec.Header().Get("x-ratelimit-reset-requests"))
	if err != nil || reset < 1 || reset > 60 {
		t.Fatalf("x-ratelimit-reset-requests = %q, want 1..60", rec.Header().Get("x-ratelimit-reset-requests"))
	}
	if rec.Header().Get("x-ratelimit-limit-tokens") != "" {
		t.Fatal("token headers set without a token rule")
	}
}

func TestEnforceRateLimitBreachReturns429WithHeaders(t *testing.T) {
	service := newTestRateLimitService(t, rateLimitRuleWithRequests("/team", 1))

	c, _ := newRateLimitTestContext("/team/alice")
	if _, err := enforceRateLimit(c, service); err != nil {
		t.Fatalf("first enforceRateLimit() error = %v", err)
	}

	c2, _ := newRateLimitTestContext("/team/alice")
	_, err := enforceRateLimit(c2, service)
	if err == nil {
		t.Fatal("second enforceRateLimit() succeeded, want breach")
	}

	var gatewayErr *core.GatewayError
	if !errors.As(err, &gatewayErr) {
		t.Fatalf("error %T does not unwrap to GatewayError", err)
	}
	if gatewayErr.HTTPStatusCode() != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", gatewayErr.HTTPStatusCode())
	}
	if gatewayErr.Type != core.ErrorTypeRateLimit {
		t.Fatalf("type = %q, want rate_limit_error", gatewayErr.Type)
	}
	if gatewayErr.Code == nil || *gatewayErr.Code != "rate_limit_exceeded" {
		t.Fatalf("code = %v, want rate_limit_exceeded", gatewayErr.Code)
	}

	headerErr, ok := err.(*gatewayErrorWithResponseHeaders)
	if !ok {
		t.Fatalf("error %T does not carry response headers", err)
	}
	headers := headerErr.ResponseHeaders()
	retryAfter, convErr := strconv.Atoi(headers.Get("Retry-After"))
	if convErr != nil || retryAfter < 1 || retryAfter > 120 {
		t.Fatalf("Retry-After = %q, want 1..120 (sliding-window recovery can pass the boundary)", headers.Get("Retry-After"))
	}
	if got := headers.Get("x-ratelimit-remaining-requests"); got != "0" {
		t.Fatalf("x-ratelimit-remaining-requests = %q, want 0", got)
	}
	if got := headers.Get("x-ratelimit-limit-requests"); got != "1" {
		t.Fatalf("x-ratelimit-limit-requests = %q, want 1", got)
	}
}

func TestEnforceRateLimitDefaultsToRootPath(t *testing.T) {
	service := newTestRateLimitService(t, rateLimitRuleWithRequests("/", 1))

	c, _ := newRateLimitTestContext("")
	if _, err := enforceRateLimit(c, service); err != nil {
		t.Fatalf("enforceRateLimit() error = %v", err)
	}
	c2, _ := newRateLimitTestContext("")
	if _, err := enforceRateLimit(c2, service); err == nil {
		t.Fatal("root rule did not apply to requests without a user path")
	}
}

func TestEnforceRateLimitReleaseReturnsConcurrencySlot(t *testing.T) {
	maxInFlight := int64(1)
	service := newTestRateLimitService(t, ratelimit.Rule{
		UserPath:      "/team",
		PeriodSeconds: ratelimit.PeriodConcurrent,
		MaxRequests:   &maxInFlight,
	})

	c, _ := newRateLimitTestContext("/team")
	release, err := enforceRateLimit(c, service)
	if err != nil {
		t.Fatalf("enforceRateLimit() error = %v", err)
	}
	c2, _ := newRateLimitTestContext("/team")
	if _, err := enforceRateLimit(c2, service); err == nil {
		t.Fatal("second in-flight request admitted over the concurrency cap")
	}
	release()
	c3, _ := newRateLimitTestContext("/team")
	release3, err := enforceRateLimit(c3, service)
	if err != nil {
		t.Fatalf("enforceRateLimit() after release error = %v", err)
	}
	release3()
}

func TestBatchRateLimitEnforcerCountsAndReleases(t *testing.T) {
	maxInFlight := int64(1)
	requestLimit := int64(2)
	service := newTestRateLimitService(t,
		ratelimit.Rule{UserPath: "/", PeriodSeconds: ratelimit.PeriodConcurrent, MaxRequests: &maxInFlight},
		ratelimit.Rule{UserPath: "/", PeriodSeconds: ratelimit.PeriodMinuteSeconds, MaxRequests: &requestLimit},
	)
	enforcer := batchRateLimitEnforcer(service)

	// The concurrency slot is released immediately, so repeated submissions
	// are bounded by the request window, not the in-flight cap.
	if err := enforcer(context.Background()); err != nil {
		t.Fatalf("first batch submission rejected: %v", err)
	}
	if err := enforcer(context.Background()); err != nil {
		t.Fatalf("second batch submission rejected: %v", err)
	}
	if err := enforcer(context.Background()); err == nil {
		t.Fatal("third batch submission admitted over the request window")
	}
}
