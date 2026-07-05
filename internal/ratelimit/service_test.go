package ratelimit

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// memStore is a minimal in-memory Store for service tests.
type memStore struct {
	rules []Rule
}

func (m *memStore) ListRules(context.Context) ([]Rule, error) {
	return append([]Rule(nil), m.rules...), nil
}

func (m *memStore) UpsertRules(_ context.Context, rules []Rule) error {
	normalized, err := normalizeRulesForUpsert(rules)
	if err != nil {
		return err
	}
	for _, rule := range normalized {
		replaced := false
		for i, existing := range m.rules {
			if keyForRule(existing) == keyForRule(rule) {
				m.rules[i] = rule
				replaced = true
				break
			}
		}
		if !replaced {
			m.rules = append(m.rules, rule)
		}
	}
	return nil
}

func (m *memStore) DeleteRule(_ context.Context, scope RuleScope, subject string, periodSeconds int64) error {
	key := ruleKey{scope: scope, subject: subject, periodSeconds: periodSeconds}
	for i, existing := range m.rules {
		if keyForRule(existing) == key {
			m.rules = append(m.rules[:i], m.rules[i+1:]...)
			return nil
		}
	}
	return ErrNotFound
}

func (m *memStore) ReplaceConfigRules(ctx context.Context, rules []Rule) error {
	kept := m.rules[:0]
	for _, existing := range m.rules {
		if existing.Source != SourceConfig {
			kept = append(kept, existing)
		}
	}
	m.rules = kept
	for i := range rules {
		rules[i].Source = SourceConfig
	}
	return m.UpsertRules(ctx, rules)
}

func (m *memStore) Close() error { return nil }

func int64Ptr(v int64) *int64 { return &v }

// onPath builds request subjects for user-path-only tests.
func onPath(path string) Subjects { return Subjects{UserPath: path} }

func newTestService(t *testing.T, rules ...Rule) *Service {
	t.Helper()
	store := &memStore{}
	if err := store.UpsertRules(context.Background(), rules); err != nil {
		t.Fatalf("seed rules: %v", err)
	}
	service, err := NewService(context.Background(), store)
	if err != nil {
		t.Fatalf("NewService() failed: %v", err)
	}
	return service
}

// windowBase is aligned to every supported period, keeping sliding-window
// math in tests exact.
var windowBase = time.Unix(1_000_000_200, 0).UTC() // 1_000_000_200 % 600 == 0

func TestAcquireEnforcesRequestLimit(t *testing.T) {
	service := newTestService(t, Rule{
		Subject:       "/team",
		PeriodSeconds: PeriodMinuteSeconds,
		MaxRequests:   int64Ptr(2),
	})

	for i := 0; i < 2; i++ {
		if _, err := service.Acquire(onPath("/team/alice"), windowBase); err != nil {
			t.Fatalf("Acquire() %d failed: %v", i, err)
		}
	}
	_, err := service.Acquire(onPath("/team/bob"), windowBase)
	var exceeded *ExceededError
	if !errors.As(err, &exceeded) {
		t.Fatalf("Acquire() error = %v, want ExceededError", err)
	}
	if exceeded.Scope != ScopeRequests {
		t.Fatalf("scope = %q, want requests", exceeded.Scope)
	}
	if exceeded.Limit != 2 {
		t.Fatalf("limit = %d, want 2", exceeded.Limit)
	}
	// Recovery can extend past the next boundary: the burst still weighs in
	// as the previous window right after the rollover.
	if exceeded.RetryAfter <= 0 || exceeded.RetryAfter > 2*time.Minute {
		t.Fatalf("retry after = %s, want within (0, 2m]", exceeded.RetryAfter)
	}
}

// TestRetryAfterReflectsSlidingWindowRecovery pins Retry-After to the exact
// first second a retry would be admitted, not the next bucket boundary.
func TestRetryAfterReflectsSlidingWindowRecovery(t *testing.T) {
	t.Run("requests", func(t *testing.T) {
		service := newTestService(t, Rule{
			Subject:       "/",
			PeriodSeconds: PeriodMinuteSeconds,
			MaxRequests:   int64Ptr(10),
		})
		for i := 0; i < 10; i++ {
			if _, err := service.Acquire(onPath("/"), windowBase); err != nil {
				t.Fatalf("Acquire() %d failed: %v", i, err)
			}
		}
		_, err := service.Acquire(onPath("/"), windowBase)
		var exceeded *ExceededError
		if !errors.As(err, &exceeded) {
			t.Fatalf("Acquire() error = %v, want ExceededError", err)
		}
		// At the boundary (60s) the previous window still weighs 10*(60/60);
		// one second later it decays to 9 and a request fits.
		if exceeded.RetryAfter != 61*time.Second {
			t.Fatalf("retry after = %s, want 61s", exceeded.RetryAfter)
		}
		if _, err := service.Acquire(onPath("/"), windowBase.Add(exceeded.RetryAfter-time.Second)); err == nil {
			t.Fatal("Acquire() one second before Retry-After succeeded")
		}
		if _, err := service.Acquire(onPath("/"), windowBase.Add(exceeded.RetryAfter)); err != nil {
			t.Fatalf("Acquire() at Retry-After failed: %v", err)
		}
	})

	t.Run("tokens overshoot", func(t *testing.T) {
		service := newTestService(t, Rule{
			Subject:       "/",
			PeriodSeconds: PeriodMinuteSeconds,
			MaxTokens:     int64Ptr(10),
		})
		// A single response overshoots the token window threefold; recovery
		// needs the rollover plus enough decay: 30*(60-41)/60 = 9 < 10.
		service.RecordTokens(onPath("/"), 30, windowBase)
		_, err := service.Acquire(onPath("/"), windowBase)
		var exceeded *ExceededError
		if !errors.As(err, &exceeded) {
			t.Fatalf("Acquire() error = %v, want ExceededError", err)
		}
		if exceeded.RetryAfter != 101*time.Second {
			t.Fatalf("retry after = %s, want 101s", exceeded.RetryAfter)
		}
		if _, err := service.Acquire(onPath("/"), windowBase.Add(exceeded.RetryAfter-time.Second)); err == nil {
			t.Fatal("Acquire() one second before Retry-After succeeded")
		}
		if _, err := service.Acquire(onPath("/"), windowBase.Add(exceeded.RetryAfter)); err != nil {
			t.Fatalf("Acquire() at Retry-After failed: %v", err)
		}
	})
}

// TestAcquireReportsLongestBlockingRule pins the breach report to the rule
// that blocks longest: with minute and day windows both exhausted, honoring
// the minute rule's Retry-After would just earn a second 429.
func TestAcquireReportsLongestBlockingRule(t *testing.T) {
	service := newTestService(t,
		Rule{Subject: "/", PeriodSeconds: PeriodMinuteSeconds, MaxRequests: int64Ptr(1)},
		Rule{Subject: "/", PeriodSeconds: PeriodDaySeconds, MaxRequests: int64Ptr(1)},
	)

	if _, err := service.Acquire(onPath("/"), windowBase); err != nil {
		t.Fatalf("Acquire() failed: %v", err)
	}
	_, err := service.Acquire(onPath("/"), windowBase)
	var exceeded *ExceededError
	if !errors.As(err, &exceeded) {
		t.Fatalf("Acquire() error = %v, want ExceededError", err)
	}
	if exceeded.Rule.PeriodSeconds != PeriodDaySeconds {
		t.Fatalf("exceeded rule period = %d, want day (longest recovery)", exceeded.Rule.PeriodSeconds)
	}
	if exceeded.RetryAfter <= time.Minute {
		t.Fatalf("retry after = %s, want the day window's recovery", exceeded.RetryAfter)
	}
}

func TestAcquireRequestsShareSubtreeCounter(t *testing.T) {
	service := newTestService(t, Rule{
		Subject:       "/team",
		PeriodSeconds: PeriodMinuteSeconds,
		MaxRequests:   int64Ptr(1),
	})

	if _, err := service.Acquire(onPath("/team/alice"), windowBase); err != nil {
		t.Fatalf("Acquire() failed: %v", err)
	}
	if _, err := service.Acquire(onPath("/team/bob"), windowBase); err == nil {
		t.Fatal("Acquire() for sibling under the same rule succeeded, want rejection")
	}
	// A sibling path outside the rule subtree is unlimited.
	if _, err := service.Acquire(onPath("/team-alpha"), windowBase); err != nil {
		t.Fatalf("Acquire() outside subtree failed: %v", err)
	}
}

func TestAcquireSlidingWindowWeighsPreviousWindow(t *testing.T) {
	service := newTestService(t, Rule{
		Subject:       "/",
		PeriodSeconds: PeriodMinuteSeconds,
		MaxRequests:   int64Ptr(10),
	})

	for i := 0; i < 10; i++ {
		if _, err := service.Acquire(onPath("/"), windowBase); err != nil {
			t.Fatalf("Acquire() %d failed: %v", i, err)
		}
	}
	if _, err := service.Acquire(onPath("/"), windowBase); err == nil {
		t.Fatal("Acquire() over limit succeeded")
	}

	// One second into the next window the previous window still weighs
	// 10*(59/60) -> 9, so exactly one request fits.
	next := windowBase.Add(61 * time.Second)
	if _, err := service.Acquire(onPath("/"), next); err != nil {
		t.Fatalf("Acquire() at window boundary failed: %v", err)
	}
	if _, err := service.Acquire(onPath("/"), next); err == nil {
		t.Fatal("Acquire() succeeded, want sliding-window rejection")
	}

	// Two full windows later all history is gone.
	if _, err := service.Acquire(onPath("/"), windowBase.Add(3*time.Minute)); err != nil {
		t.Fatalf("Acquire() after windows expired failed: %v", err)
	}
}

func TestTokenLimitIsPostAccounted(t *testing.T) {
	service := newTestService(t, Rule{
		Subject:       "/team",
		PeriodSeconds: PeriodMinuteSeconds,
		MaxTokens:     int64Ptr(100),
	})

	// Tokens are unknown before the response: the first request passes.
	if _, err := service.Acquire(onPath("/team/alice"), windowBase); err != nil {
		t.Fatalf("Acquire() failed: %v", err)
	}
	service.RecordTokens(onPath("/team/alice"), 150, windowBase)

	_, err := service.Acquire(onPath("/team/alice"), windowBase.Add(time.Second))
	var exceeded *ExceededError
	if !errors.As(err, &exceeded) {
		t.Fatalf("Acquire() error = %v, want ExceededError", err)
	}
	if exceeded.Scope != ScopeTokens {
		t.Fatalf("scope = %q, want tokens", exceeded.Scope)
	}

	// The token window rolls over like the request window.
	if _, err := service.Acquire(onPath("/team/alice"), windowBase.Add(3*time.Minute)); err != nil {
		t.Fatalf("Acquire() after token window expired failed: %v", err)
	}
}

func TestConcurrencyLimitHeldUntilRelease(t *testing.T) {
	service := newTestService(t, Rule{
		Subject:       "/team",
		PeriodSeconds: PeriodConcurrent,
		MaxRequests:   int64Ptr(1),
	})

	first, err := service.Acquire(onPath("/team/alice"), windowBase)
	if err != nil {
		t.Fatalf("Acquire() failed: %v", err)
	}
	_, err = service.Acquire(onPath("/team/bob"), windowBase)
	var exceeded *ExceededError
	if !errors.As(err, &exceeded) {
		t.Fatalf("Acquire() error = %v, want ExceededError", err)
	}
	if exceeded.Scope != ScopeConcurrency {
		t.Fatalf("scope = %q, want concurrency", exceeded.Scope)
	}

	first.Release()
	first.Release() // idempotent: must not free a second slot

	second, err := service.Acquire(onPath("/team/bob"), windowBase)
	if err != nil {
		t.Fatalf("Acquire() after release failed: %v", err)
	}
	if _, err := service.Acquire(onPath("/team/carol"), windowBase); err == nil {
		t.Fatal("Acquire() succeeded, want concurrency rejection after single release")
	}
	second.Release()
}

func TestAcquireHeadersReportMostConstrainedRule(t *testing.T) {
	service := newTestService(t,
		Rule{Subject: "/", PeriodSeconds: PeriodMinuteSeconds, MaxRequests: int64Ptr(100), MaxTokens: int64Ptr(1000)},
		Rule{Subject: "/team", PeriodSeconds: PeriodMinuteSeconds, MaxRequests: int64Ptr(5)},
	)

	reservation, err := service.Acquire(onPath("/team/alice"), windowBase)
	if err != nil {
		t.Fatalf("Acquire() failed: %v", err)
	}
	headers := reservation.Headers()
	if !headers.HasRequests {
		t.Fatal("HasRequests = false, want true")
	}
	if headers.RequestLimit != 5 {
		t.Fatalf("request limit = %d, want 5 (most constrained)", headers.RequestLimit)
	}
	if headers.RequestRemaining != 4 {
		t.Fatalf("request remaining = %d, want 4", headers.RequestRemaining)
	}
	if !headers.HasTokens {
		t.Fatal("HasTokens = false, want true")
	}
	if headers.TokenLimit != 1000 || headers.TokenRemaining != 1000 {
		t.Fatalf("token limit/remaining = %d/%d, want 1000/1000", headers.TokenLimit, headers.TokenRemaining)
	}
	if headers.RequestResetAfter <= 0 || headers.RequestResetAfter > time.Minute {
		t.Fatalf("request reset = %s, want within (0, 1m]", headers.RequestResetAfter)
	}
}

func TestAcquireWithoutMatchingRulesIsUnlimited(t *testing.T) {
	service := newTestService(t, Rule{
		Subject:       "/team",
		PeriodSeconds: PeriodMinuteSeconds,
		MaxRequests:   int64Ptr(1),
	})

	for i := 0; i < 5; i++ {
		reservation, err := service.Acquire(onPath("/other"), windowBase)
		if err != nil {
			t.Fatalf("Acquire() %d failed: %v", i, err)
		}
		if reservation.Headers().HasRequests {
			t.Fatal("headers set for unmatched path")
		}
	}
}

func TestRejectedAcquireDoesNotConsumeCounters(t *testing.T) {
	service := newTestService(t,
		Rule{Subject: "/team", PeriodSeconds: PeriodConcurrent, MaxRequests: int64Ptr(1)},
		Rule{Subject: "/team", PeriodSeconds: PeriodMinuteSeconds, MaxRequests: int64Ptr(10)},
	)

	held, err := service.Acquire(onPath("/team"), windowBase)
	if err != nil {
		t.Fatalf("Acquire() failed: %v", err)
	}
	// Concurrency breach: the request-window counter must stay untouched.
	if _, err := service.Acquire(onPath("/team"), windowBase); err == nil {
		t.Fatal("Acquire() succeeded, want concurrency rejection")
	}
	statuses := service.Statuses(windowBase)
	for _, status := range statuses {
		if status.Rule.PeriodSeconds == PeriodMinuteSeconds && status.RequestsUsed != 1 {
			t.Fatalf("requests used = %d, want 1 (rejected attempt must not count)", status.RequestsUsed)
		}
	}
	held.Release()
}

func TestStatusesAndResets(t *testing.T) {
	service := newTestService(t, Rule{
		Subject:       "/team",
		PeriodSeconds: PeriodMinuteSeconds,
		MaxRequests:   int64Ptr(2),
		MaxTokens:     int64Ptr(100),
	})

	if _, err := service.Acquire(onPath("/team"), windowBase); err != nil {
		t.Fatalf("Acquire() failed: %v", err)
	}
	service.RecordTokens(onPath("/team"), 40, windowBase)

	statuses := service.Statuses(windowBase)
	if len(statuses) != 1 {
		t.Fatalf("statuses = %d, want 1", len(statuses))
	}
	status := statuses[0]
	if status.RequestsUsed != 1 || status.RequestsRemaining == nil || *status.RequestsRemaining != 1 {
		t.Fatalf("requests used/remaining = %d/%v, want 1/1", status.RequestsUsed, status.RequestsRemaining)
	}
	if status.TokensUsed != 40 || status.TokensRemaining == nil || *status.TokensRemaining != 60 {
		t.Fatalf("tokens used/remaining = %d/%v, want 40/60", status.TokensUsed, status.TokensRemaining)
	}
	if status.WindowStart.IsZero() || !status.WindowEnd.Equal(status.WindowStart.Add(time.Minute)) {
		t.Fatalf("window = %s..%s, want one minute", status.WindowStart, status.WindowEnd)
	}

	if err := service.ResetRule(ScopeUserPath, "/team", PeriodMinuteSeconds); err != nil {
		t.Fatalf("ResetRule() failed: %v", err)
	}
	status = service.Statuses(windowBase)[0]
	if status.RequestsUsed != 0 || status.TokensUsed != 0 {
		t.Fatalf("after reset used = %d/%d, want 0/0", status.RequestsUsed, status.TokensUsed)
	}

	service.RecordTokens(onPath("/team"), 40, windowBase)
	if err := service.ResetAll(); err != nil {
		t.Fatalf("ResetAll() failed: %v", err)
	}
	if status := service.Statuses(windowBase)[0]; status.TokensUsed != 0 {
		t.Fatalf("after reset-all tokens used = %d, want 0", status.TokensUsed)
	}
}

func TestUpsertDeleteAndHasTokenRules(t *testing.T) {
	service := newTestService(t)
	if service.HasTokenRules() {
		t.Fatal("HasTokenRules() = true for empty service")
	}
	if err := service.UpsertRules(context.Background(), []Rule{
		{Subject: "/team", PeriodSeconds: PeriodMinuteSeconds, MaxTokens: int64Ptr(100), Source: SourceManual},
	}); err != nil {
		t.Fatalf("UpsertRules() failed: %v", err)
	}
	if !service.HasTokenRules() {
		t.Fatal("HasTokenRules() = false, want true")
	}
	if err := service.DeleteRule(context.Background(), ScopeUserPath, "/team", PeriodMinuteSeconds); err != nil {
		t.Fatalf("DeleteRule() failed: %v", err)
	}
	if len(service.Rules()) != 0 {
		t.Fatalf("rules = %d, want 0", len(service.Rules()))
	}
	if err := service.DeleteRule(context.Background(), ScopeUserPath, "/team", PeriodMinuteSeconds); !errors.Is(err, ErrNotFound) {
		t.Fatalf("DeleteRule() error = %v, want ErrNotFound", err)
	}
}

func TestNilServiceIsSafe(t *testing.T) {
	var service *Service
	reservation, err := service.Acquire(onPath("/team"), windowBase)
	if err != nil {
		t.Fatalf("nil service Acquire() error = %v", err)
	}
	reservation.Release()
	service.RecordTokens(onPath("/team"), 10, windowBase)
	if statuses := service.Statuses(windowBase); statuses != nil {
		t.Fatalf("nil service Statuses() = %v, want nil", statuses)
	}
	if !service.routeAvailableAt("openai", "openai/gpt-4o", windowBase) {
		t.Fatal("nil service RouteAvailable() = false, want true")
	}
}

func TestProviderScopedRules(t *testing.T) {
	service := newTestService(t, Rule{
		Scope:         ScopeProvider,
		Subject:       "openai",
		PeriodSeconds: PeriodMinuteSeconds,
		MaxRequests:   int64Ptr(1),
	})

	route := Subjects{UserPath: "/team/alice", Provider: "openai", Model: "openai/gpt-4o"}
	if _, err := service.Acquire(route, windowBase); err != nil {
		t.Fatalf("Acquire() failed: %v", err)
	}
	// The provider counter is shared across consumers and models.
	other := Subjects{UserPath: "/other", Provider: "OpenAI", Model: "openai/gpt-4o-mini"}
	_, err := service.Acquire(other, windowBase)
	var exceeded *ExceededError
	if !errors.As(err, &exceeded) {
		t.Fatalf("Acquire() error = %v, want ExceededError", err)
	}
	if exceeded.Rule.Scope != ScopeProvider {
		t.Fatalf("rule scope = %q, want provider", exceeded.Rule.Scope)
	}
	if msg := exceeded.Error(); !strings.Contains(msg, "provider openai") {
		t.Fatalf("error = %q, want provider subject label", msg)
	}
	// Another provider is unaffected.
	if _, err := service.Acquire(Subjects{UserPath: "/team", Provider: "anthropic", Model: "anthropic/claude"}, windowBase); err != nil {
		t.Fatalf("Acquire() for other provider failed: %v", err)
	}
	// Requests with no resolved route (batch) skip provider rules.
	if _, err := service.Acquire(onPath("/team/alice"), windowBase); err != nil {
		t.Fatalf("Acquire() without route failed: %v", err)
	}
}

func TestModelScopedRules(t *testing.T) {
	t.Run("bare subject matches any provider", func(t *testing.T) {
		service := newTestService(t, Rule{
			Scope:         ScopeModel,
			Subject:       "gpt-4o",
			PeriodSeconds: PeriodMinuteSeconds,
			MaxRequests:   int64Ptr(1),
		})
		if _, err := service.Acquire(Subjects{UserPath: "/", Provider: "openai", Model: "openai/gpt-4o"}, windowBase); err != nil {
			t.Fatalf("Acquire() failed: %v", err)
		}
		if _, err := service.Acquire(Subjects{UserPath: "/", Provider: "azure", Model: "GPT-4o"}, windowBase); err == nil {
			t.Fatal("Acquire() for same model via other provider succeeded, want shared counter rejection")
		}
		if _, err := service.Acquire(Subjects{UserPath: "/", Provider: "openai", Model: "openai/gpt-4o-mini"}, windowBase); err != nil {
			t.Fatalf("Acquire() for different model failed: %v", err)
		}
	})

	t.Run("qualified subject pins one provider", func(t *testing.T) {
		service := newTestService(t, Rule{
			Scope:         ScopeModel,
			Subject:       "openai/gpt-4o",
			PeriodSeconds: PeriodMinuteSeconds,
			MaxRequests:   int64Ptr(1),
		})
		// Matches the qualified model, and the bare model when the provider agrees.
		if _, err := service.Acquire(Subjects{UserPath: "/", Provider: "openai", Model: "gpt-4o"}, windowBase); err != nil {
			t.Fatalf("Acquire() failed: %v", err)
		}
		if _, err := service.Acquire(Subjects{UserPath: "/", Provider: "openai", Model: "openai/gpt-4o"}, windowBase); err == nil {
			t.Fatal("Acquire() succeeded, want shared counter rejection")
		}
		// The same model id on another provider is a different subject.
		if _, err := service.Acquire(Subjects{UserPath: "/", Provider: "azure", Model: "gpt-4o"}, windowBase); err != nil {
			t.Fatalf("Acquire() for other provider failed: %v", err)
		}
	})
}

func TestRouteAvailableProbesWithoutConsuming(t *testing.T) {
	service := newTestService(t,
		Rule{Scope: ScopeProvider, Subject: "openai", PeriodSeconds: PeriodMinuteSeconds, MaxRequests: int64Ptr(1)},
		Rule{Scope: ScopeProvider, Subject: "openai", PeriodSeconds: PeriodConcurrent, MaxRequests: int64Ptr(1)},
		Rule{Subject: "/team", PeriodSeconds: PeriodMinuteSeconds, MaxRequests: int64Ptr(1)},
	)

	// Probing repeatedly consumes nothing.
	for i := 0; i < 3; i++ {
		if !service.routeAvailableAt("openai", "openai/gpt-4o", windowBase) {
			t.Fatalf("RouteAvailable() probe %d = false, want true", i)
		}
	}

	held, err := service.Acquire(Subjects{UserPath: "/other", Provider: "openai", Model: "openai/gpt-4o"}, windowBase)
	if err != nil {
		t.Fatalf("Acquire() failed: %v", err)
	}
	// The request window is exhausted and the concurrency slot is held.
	if service.routeAvailableAt("openai", "openai/gpt-4o", windowBase) {
		t.Fatal("RouteAvailable() = true for saturated provider, want false")
	}
	if !service.routeAvailableAt("anthropic", "anthropic/claude", windowBase) {
		t.Fatal("RouteAvailable() = false for unlimited provider, want true")
	}
	held.Release()

	// User-path saturation must not affect route availability: switching
	// targets cannot relieve a consumer limit.
	if _, err := service.Acquire(onPath("/team/app"), windowBase); err != nil {
		t.Fatalf("Acquire() failed: %v", err)
	}
	if !service.routeAvailableAt("anthropic", "anthropic/claude", windowBase) {
		t.Fatal("RouteAvailable() = false after user-path saturation, want true")
	}
}

func TestRecordTokensChargesProviderAndModelWindows(t *testing.T) {
	service := newTestService(t,
		Rule{Scope: ScopeProvider, Subject: "openai", PeriodSeconds: PeriodMinuteSeconds, MaxTokens: int64Ptr(100)},
		Rule{Scope: ScopeModel, Subject: "gpt-4o", PeriodSeconds: PeriodMinuteSeconds, MaxTokens: int64Ptr(50)},
	)

	// Usage entries carry the executed provider name and a bare model id.
	service.RecordTokens(Subjects{UserPath: "/team", Provider: "openai", Model: "gpt-4o"}, 60, windowBase)

	byScope := map[RuleScope]Status{}
	for _, status := range service.Statuses(windowBase) {
		byScope[status.Rule.Scope] = status
	}
	if byScope[ScopeProvider].TokensUsed != 60 {
		t.Fatalf("provider tokens used = %d, want 60", byScope[ScopeProvider].TokensUsed)
	}
	if byScope[ScopeModel].TokensUsed != 60 {
		t.Fatalf("model tokens used = %d, want 60", byScope[ScopeModel].TokensUsed)
	}

	// The model window (limit 50) is exhausted; the provider window is not.
	if service.routeAvailableAt("openai", "openai/gpt-4o", windowBase) {
		t.Fatal("RouteAvailable() = true for token-exhausted model, want false")
	}
	if !service.routeAvailableAt("openai", "openai/gpt-4o-mini", windowBase) {
		t.Fatal("RouteAvailable() = false for other model, want true")
	}
}
