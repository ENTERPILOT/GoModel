package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/echotest"
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
func (s *staticRuleStore) ReplaceConfigRules(context.Context, []ratelimit.Rule) error {
	return nil
}
func (s *staticRuleStore) LoadCounters(context.Context) ([]ratelimit.WindowSnapshot, error) {
	return nil, nil
}
func (s *staticRuleStore) SaveCounters(context.Context, []ratelimit.WindowSnapshot) error {
	return nil
}
func (s *staticRuleStore) DeleteCounter(context.Context, ratelimit.RuleScope, string, int64) error {
	return nil
}
func (s *staticRuleStore) DeleteAllCounters(context.Context) error { return nil }
func (s *staticRuleStore) Close() error                            { return nil }

func newTestRateLimitService(t *testing.T, rules ...ratelimit.Rule) *ratelimit.Service {
	t.Helper()
	normalized := make([]ratelimit.Rule, 0, len(rules))
	for _, rule := range rules {
		item, err := ratelimit.NormalizeRule(rule)
		require.NoError(t, err)

		normalized = append(normalized, item)
	}
	service, err := ratelimit.NewService(context.Background(), &staticRuleStore{rules: normalized})
	require.NoError(t, err)

	return service
}

func newRateLimitTestContext(t *testing.T, userPath string) (*echo.Context, *httptest.ResponseRecorder) {
	t.Helper()
	c, rec := echotest.Post(t, "/v1/chat/completions", nil)
	if userPath != "" {
		c.SetRequest(c.Request().WithContext(core.WithEffectiveUserPath(c.Request().Context(), userPath)))
	}
	return c, rec
}

func rateLimitRuleWithRequests(path string, maxRequests int64) ratelimit.Rule {
	return ratelimit.Rule{Subject: path, PeriodSeconds: ratelimit.PeriodMinuteSeconds, MaxRequests: &maxRequests}
}

func TestEnforceRateLimitNilLimiterIsNoop(t *testing.T) {
	c, rec := newRateLimitTestContext(t, "/team")
	release, err := enforceRateLimit(c, nil, rateLimitRoute{})
	require.NoError(t, err)

	release()
	require.Empty(t, rec.Header())
}

func TestEnforceRateLimitSetsSuccessHeaders(t *testing.T) {
	service := newTestRateLimitService(t, rateLimitRuleWithRequests("/team", 5))
	c, rec := newRateLimitTestContext(t, "/team/alice")

	release, err := enforceRateLimit(c, service, rateLimitRoute{})
	require.NoError(t, err)

	defer release()
	require.Equal(t, "5", rec.Header().Get("x-ratelimit-limit-requests"))
	require.Equal(t, "4", rec.Header().Get("x-ratelimit-remaining-requests"))

	reset, err := strconv.Atoi(rec.Header().Get("x-ratelimit-reset-requests"))
	require.NoError(t, err)
	require.GreaterOrEqual(t, reset, 1)
	require.LessOrEqual(t, reset, 60)
	require.Empty(t, rec.Header().Get("x-ratelimit-limit-tokens"))
}

func TestEnforceRateLimitBreachReturns429WithHeaders(t *testing.T) {
	service := newTestRateLimitService(t, rateLimitRuleWithRequests("/team", 1))

	c, _ := newRateLimitTestContext(t, "/team/alice")
	_, err := enforceRateLimit(c, service, rateLimitRoute{})
	require.NoError(t, err)

	c2, _ := newRateLimitTestContext(t, "/team/alice")
	_, err = enforceRateLimit(c2, service, rateLimitRoute{})
	require.Error(t, err)

	var gatewayErr *core.GatewayError
	require.ErrorAs(t, err, &gatewayErr)
	require.Equal(t, http.StatusTooManyRequests, gatewayErr.HTTPStatusCode())
	require.Equal(t, core.ErrorTypeRateLimit, gatewayErr.Type)
	require.NotNil(t, gatewayErr.Code)
	require.Equal(t, "rate_limit_exceeded", *gatewayErr.Code)

	headerErr, ok := err.(*gatewayErrorWithResponseHeaders)
	require.True(t, ok)

	headers := headerErr.ResponseHeaders()
	retryAfter, convErr := strconv.Atoi(headers.Get("Retry-After"))
	require.NoError(t, convErr)
	require.GreaterOrEqual(t, retryAfter, 1)
	require.LessOrEqual(t, retryAfter, 120, "Retry-After = %q, want 1..120 (sliding-window recovery can pass the boundary)", headers.Get("Retry-After"))
	require.Equal(t, "0", headers.Get("x-ratelimit-remaining-requests"))
	require.Equal(t, "1", headers.Get("x-ratelimit-limit-requests"))
}

func TestEnforceRateLimitDefaultsToRootPath(t *testing.T) {
	service := newTestRateLimitService(t, rateLimitRuleWithRequests("/", 1))

	c, _ := newRateLimitTestContext(t, "")
	_, err := enforceRateLimit(c, service, rateLimitRoute{})
	require.NoError(t, err)

	c2, _ := newRateLimitTestContext(t, "")
	_, err = enforceRateLimit(c2, service, rateLimitRoute{})
	require.Error(t, err)
}

func TestEnforceRateLimitReleaseReturnsConcurrencySlot(t *testing.T) {
	maxInFlight := int64(1)
	service := newTestRateLimitService(t, ratelimit.Rule{
		Subject:       "/team",
		PeriodSeconds: ratelimit.PeriodConcurrent,
		MaxRequests:   &maxInFlight,
	})

	c, _ := newRateLimitTestContext(t, "/team")
	release, err := enforceRateLimit(c, service, rateLimitRoute{})
	require.NoError(t, err)

	c2, _ := newRateLimitTestContext(t, "/team")
	_, err = enforceRateLimit(c2, service, rateLimitRoute{})
	require.Error(t, err)

	release()
	c3, _ := newRateLimitTestContext(t, "/team")
	release3, err := enforceRateLimit(c3, service, rateLimitRoute{})
	require.NoError(t, err)

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
	err := // The concurrency slot is released immediately, so repeated submissions
		// are bounded by the request window, not the in-flight cap.
		enforcer(context.Background())
	require.NoError(t, err)
	err = enforcer(context.Background())
	require.NoError(t, err)
	require.Error(t, enforcer(context.Background()))
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

	c, _ := newRateLimitTestContext(t, "/team")
	first, err := enforceAdmission(c, service, checker, route)
	require.NoError(t, err)
	require.NoError(t, first.saturatedRoute)

	first.release()

	c2, _ := newRateLimitTestContext(t, "/team")
	second, err := enforceAdmission(c2, service, checker, route)
	require.NoError(t, err)

	defer second.release()
	require.Error(t, second.saturatedRoute)

	var gatewayErr *core.GatewayError
	require.ErrorAs(t, second.saturatedRoute, &gatewayErr)
	require.Equal(t, http.StatusTooManyRequests, gatewayErr.HTTPStatusCode())

	ctx := second.dispatchContext(context.Background())
	require.Error(t, core.PrimaryRouteSaturated(ctx))

	// The deferred request still consumed its consumer window.
	for _, status := range service.Statuses(time.Now().UTC()) {
		if status.Rule.Scope == ratelimit.ScopeUserPath {
			require.Equal(t, int64(2), status.RequestsUsed, "deferred request still counts")
		}
	}
	require.Equal(t, 2, checker.calls)
}

// Without failover targets the saturated route stays an outright 429.
func TestEnforceAdmissionRejectsSaturatedRouteWithoutFailovers(t *testing.T) {
	service := newTestRateLimitService(t, rateLimitProviderRule("openai", 1))
	checker := &countingBudgetChecker{}
	route := rateLimitRoute{provider: "openai", model: "openai/gpt-4o"}

	c, _ := newRateLimitTestContext(t, "/team")
	adm, err := enforceAdmission(c, service, checker, route)
	require.NoError(t, err)

	adm.release()

	c2, _ := newRateLimitTestContext(t, "/team")
	_, err = enforceAdmission(c2, service, checker, route)
	require.Error(t, err)
}

// Consumer breaches never defer: switching targets cannot relieve them.
func TestEnforceAdmissionNeverDefersConsumerBreaches(t *testing.T) {
	service := newTestRateLimitService(t, rateLimitRuleWithRequests("/team", 1))
	checker := &countingBudgetChecker{}
	route := rateLimitRoute{provider: "openai", model: "openai/gpt-4o"}.withFailovers(3)

	c, _ := newRateLimitTestContext(t, "/team")
	adm, err := enforceAdmission(c, service, checker, route)
	require.NoError(t, err)

	adm.release()

	c2, _ := newRateLimitTestContext(t, "/team")
	_, err = enforceAdmission(c2, service, checker, route)
	require.Error(t, err)

	var gatewayErr *core.GatewayError
	require.ErrorAs(t, err, &gatewayErr)
	require.Equal(t, http.StatusTooManyRequests, gatewayErr.HTTPStatusCode())
}
