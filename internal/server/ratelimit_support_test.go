package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/ratelimit"
)

// staticRuleStore serves a fixed rule set so tests can build a real
// ratelimit.Service without a database.
type staticRuleStore struct {
	rules []ratelimit.Rule
}

func (s *staticRuleStore) ListRules(context.Context) ([]ratelimit.Rule, error) {
	return append([]ratelimit.Rule(nil), s.rules...), nil
}
func (s *staticRuleStore) UpsertRules(context.Context, []ratelimit.Rule) error { return nil }
func (s *staticRuleStore) DeleteRule(context.Context, ratelimit.RuleScope, string, int64) error {
	return nil
}
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
	return ratelimit.Rule{Subject: path, PeriodSeconds: ratelimit.PeriodMinuteSeconds, MaxRequests: &maxRequests}
}

func TestEnforceRateLimitNilLimiterIsNoop(t *testing.T) {
	c, rec := newRateLimitTestContext("/team")
	release, err := enforceRateLimit(c, nil, rateLimitRoute{})
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

	release, err := enforceRateLimit(c, service, rateLimitRoute{})
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
	if _, err := enforceRateLimit(c, service, rateLimitRoute{}); err != nil {
		t.Fatalf("first enforceRateLimit() error = %v", err)
	}

	c2, _ := newRateLimitTestContext("/team/alice")
	_, err := enforceRateLimit(c2, service, rateLimitRoute{})
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
	if _, err := enforceRateLimit(c, service, rateLimitRoute{}); err != nil {
		t.Fatalf("enforceRateLimit() error = %v", err)
	}
	c2, _ := newRateLimitTestContext("")
	if _, err := enforceRateLimit(c2, service, rateLimitRoute{}); err == nil {
		t.Fatal("root rule did not apply to requests without a user path")
	}
}

func TestEnforceRateLimitReleaseReturnsConcurrencySlot(t *testing.T) {
	maxInFlight := int64(1)
	service := newTestRateLimitService(t, ratelimit.Rule{
		Subject:       "/team",
		PeriodSeconds: ratelimit.PeriodConcurrent,
		MaxRequests:   &maxInFlight,
	})

	c, _ := newRateLimitTestContext("/team")
	release, err := enforceRateLimit(c, service, rateLimitRoute{})
	if err != nil {
		t.Fatalf("enforceRateLimit() error = %v", err)
	}
	c2, _ := newRateLimitTestContext("/team")
	if _, err := enforceRateLimit(c2, service, rateLimitRoute{}); err == nil {
		t.Fatal("second in-flight request admitted over the concurrency cap")
	}
	release()
	c3, _ := newRateLimitTestContext("/team")
	release3, err := enforceRateLimit(c3, service, rateLimitRoute{})
	if err != nil {
		t.Fatalf("enforceRateLimit() after release error = %v", err)
	}
	release3()
}

func TestBatchRateLimitEnforcerCountsAndReleases(t *testing.T) {
	maxInFlight := int64(1)
	requestLimit := int64(2)
	service := newTestRateLimitService(t,
		ratelimit.Rule{Subject: "/", PeriodSeconds: ratelimit.PeriodConcurrent, MaxRequests: &maxInFlight},
		ratelimit.Rule{Subject: "/", PeriodSeconds: ratelimit.PeriodMinuteSeconds, MaxRequests: &requestLimit},
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

func rateLimitProviderRule(provider string, maxRequests int64) ratelimit.Rule {
	return ratelimit.Rule{Scope: ratelimit.ScopeProvider, Subject: provider, PeriodSeconds: ratelimit.PeriodMinuteSeconds, MaxRequests: &maxRequests}
}

// A saturated provider/model route with failover targets defers to the sweep:
// the request is admitted against consumer limits and the 429 is stamped for
// dispatch instead of being returned.
func TestEnforceAdmissionDefersSaturatedRouteToFailover(t *testing.T) {
	service := newTestRateLimitService(t,
		rateLimitProviderRule("openai", 1),
		rateLimitRuleWithRequests("/", 10),
	)
	checker := &countingBudgetChecker{}
	route := rateLimitRoute{provider: "openai", model: "openai/gpt-4o"}.withFailovers(1)

	c, _ := newRateLimitTestContext("/team")
	first, err := enforceAdmission(c, service, checker, route)
	if err != nil {
		t.Fatalf("first enforceAdmission() error = %v", err)
	}
	if first.saturatedRoute != nil {
		t.Fatal("first admission reported a saturated route")
	}
	first.release()

	c2, _ := newRateLimitTestContext("/team")
	second, err := enforceAdmission(c2, service, checker, route)
	if err != nil {
		t.Fatalf("second enforceAdmission() error = %v (saturated route must defer to failover)", err)
	}
	defer second.release()
	if second.saturatedRoute == nil {
		t.Fatal("second admission did not report the saturated route")
	}
	var gatewayErr *core.GatewayError
	if !errors.As(second.saturatedRoute, &gatewayErr) || gatewayErr.HTTPStatusCode() != http.StatusTooManyRequests {
		t.Fatalf("saturated route error = %v, want 429 gateway error", second.saturatedRoute)
	}
	ctx := second.dispatchContext(context.Background())
	if core.PrimaryRouteSaturated(ctx) == nil {
		t.Fatal("dispatchContext did not stamp the saturation marker")
	}

	// The deferred request still consumed its consumer window.
	for _, status := range service.Statuses(time.Now().UTC()) {
		if status.Rule.Scope == ratelimit.ScopeUserPath && status.RequestsUsed != 2 {
			t.Fatalf("user-path requests used = %d, want 2 (deferred request still counts)", status.RequestsUsed)
		}
	}
	if checker.calls != 2 {
		t.Fatalf("budget checker calls = %d, want 2", checker.calls)
	}
}

// Without failover targets the saturated route stays an outright 429.
func TestEnforceAdmissionRejectsSaturatedRouteWithoutFailovers(t *testing.T) {
	service := newTestRateLimitService(t, rateLimitProviderRule("openai", 1))
	checker := &countingBudgetChecker{}
	route := rateLimitRoute{provider: "openai", model: "openai/gpt-4o"}

	c, _ := newRateLimitTestContext("/team")
	adm, err := enforceAdmission(c, service, checker, route)
	if err != nil {
		t.Fatalf("first enforceAdmission() error = %v", err)
	}
	adm.release()

	c2, _ := newRateLimitTestContext("/team")
	if _, err := enforceAdmission(c2, service, checker, route); err == nil {
		t.Fatal("saturated route without failovers admitted, want 429")
	}
}

// Consumer breaches never defer: switching targets cannot relieve them.
func TestEnforceAdmissionNeverDefersConsumerBreaches(t *testing.T) {
	service := newTestRateLimitService(t, rateLimitRuleWithRequests("/team", 1))
	checker := &countingBudgetChecker{}
	route := rateLimitRoute{provider: "openai", model: "openai/gpt-4o"}.withFailovers(3)

	c, _ := newRateLimitTestContext("/team")
	adm, err := enforceAdmission(c, service, checker, route)
	if err != nil {
		t.Fatalf("first enforceAdmission() error = %v", err)
	}
	adm.release()

	c2, _ := newRateLimitTestContext("/team")
	_, err = enforceAdmission(c2, service, checker, route)
	if err == nil {
		t.Fatal("consumer breach with failovers admitted, want 429")
	}
	var gatewayErr *core.GatewayError
	if !errors.As(err, &gatewayErr) || gatewayErr.HTTPStatusCode() != http.StatusTooManyRequests {
		t.Fatalf("error = %v, want 429 gateway error", err)
	}
}
