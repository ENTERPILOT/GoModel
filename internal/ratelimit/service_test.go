package ratelimit

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/enterpilot/gomodel/config"
	"github.com/stretchr/testify/require"
)

// memStore is a minimal in-memory Store for service tests.
type memStore struct {
	rules    []Rule
	counters []WindowSnapshot
	// written stamps each row the way a real store's updated_at column does.
	written map[string]int64
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

func (m *memStore) LoadCounters(context.Context) ([]WindowSnapshot, error) {
	return append([]WindowSnapshot(nil), m.counters...), nil
}

// snapshotIdentity is the primary key every store keys a window row by.
func snapshotIdentity(s WindowSnapshot) string {
	return s.Scope + "\x00" + s.Subject + "\x00" + s.Partition + "\x00" + strconv.FormatInt(s.PeriodSeconds, 10)
}

// SaveCounters mirrors the real stores: upsert by identity, then collect rows
// that went two periods without a write.
func (m *memStore) SaveCounters(_ context.Context, snapshots []WindowSnapshot) error {
	now := time.Now().Unix()
	if m.written == nil {
		m.written = make(map[string]int64)
	}
	for _, snap := range snapshots {
		m.written[snapshotIdentity(snap)] = now
		replaced := false
		for i, existing := range m.counters {
			if snapshotIdentity(existing) == snapshotIdentity(snap) {
				m.counters[i] = snap
				replaced = true
				break
			}
		}
		if !replaced {
			m.counters = append(m.counters, snap)
		}
	}
	kept := m.counters[:0]
	for _, snap := range m.counters {
		if m.written[snapshotIdentity(snap)]+2*snap.PeriodSeconds >= now {
			kept = append(kept, snap)
		}
	}
	m.counters = kept
	return nil
}

func (m *memStore) DeleteCounter(_ context.Context, scope RuleScope, subject string, periodSeconds int64) error {
	kept := m.counters[:0]
	for _, snap := range m.counters {
		if snap.Scope != string(scope) || snap.Subject != subject || snap.PeriodSeconds != periodSeconds {
			kept = append(kept, snap)
		}
	}
	m.counters = kept
	return nil
}

func (m *memStore) DeleteAllCounters(context.Context) error {
	m.counters = nil
	return nil
}

// onPath builds request subjects for user-path-only tests.
func onPath(path string) Subjects { return Subjects{UserPath: path} }

func newTestService(t *testing.T, rules ...Rule) *Service {
	t.Helper()
	store := &memStore{}
	err := store.UpsertRules(context.Background(), rules)
	require.NoError(t, err)

	service, err := NewService(context.Background(), store)
	require.NoError(t, err)

	t.Cleanup(service.Close)
	return service
}

func TestServiceRejectsQuotaTemplatesWhenDisabled(t *testing.T) {
	template := Rule{
		Scope: ScopeUserPath, Subject: "/customers", PerChild: true,
		PeriodSeconds: PeriodMinuteSeconds, MaxRequests: new(int64(10)),
	}
	_, err := NewService(context.Background(), &memStore{rules: []Rule{template}}, WithQuotaTemplates(false))
	require.ErrorIs(t, err, ErrQuotaTemplatesUnavailable)

	store := &memStore{}
	service, err := NewService(context.Background(), store, WithQuotaTemplates(false))
	require.NoError(t, err)
	err = service.UpsertRules(context.Background(), []Rule{template})
	require.ErrorIs(t, err, ErrQuotaTemplatesUnavailable)
	err = service.ReplaceConfigRules(context.Background(), []Rule{template})
	require.ErrorIs(t, err, ErrQuotaTemplatesUnavailable)
	require.Empty(t, store.rules)
}

func TestSeedConfiguredRulesCarriesPerChild(t *testing.T) {
	store := &memStore{}
	service, err := NewService(context.Background(), store)
	require.NoError(t, err)

	err = seedConfiguredRules(context.Background(), service, config.RateLimitsConfig{
		UserPaths: []config.RateLimitUserPathConfig{{
			Path: "/users", PerChild: true,
			Limits: []config.RateLimitRuleConfig{{Period: "minute", MaxRequests: new(int64(100))}},
		}},
	})
	require.NoError(t, err)

	rules := service.Rules()
	require.Len(t, rules, 1)
	require.True(t, rules[0].PerChild)
	require.Equal(t, "/users", rules[0].Subject)
}

// windowBase is aligned to every supported period, keeping sliding-window
// math in tests exact.
var windowBase = time.Unix(1_000_000_200, 0).UTC() // 1_000_000_200 % 600 == 0

func TestAcquireEnforcesRequestLimit(t *testing.T) {
	service := newTestService(t, Rule{
		Subject:       "/team",
		PeriodSeconds: PeriodMinuteSeconds,
		MaxRequests:   new(int64(2)),
	})

	for i := range 2 {
		_, err := service.Acquire(onPath("/team/alice"), windowBase)
		require.NoError(t, err, "Acquire() %d failed: %v", i, err)
	}
	_, err := service.Acquire(onPath("/team/bob"), windowBase)
	var exceeded *ExceededError
	require.ErrorAs(t, err, &exceeded)
	require.Equal(t, ScopeRequests, exceeded.Scope)
	require.Equal(t, int64(2), exceeded.Limit)

	// Recovery can extend past the next boundary: the burst still weighs in
	// as the previous window right after the rollover.
	require.Greater(t, exceeded.RetryAfter, time.Duration(0))
	require.LessOrEqual(t, exceeded.RetryAfter, 2*time.Minute)
}

// TestRetryAfterReflectsSlidingWindowRecovery pins Retry-After to the exact
// first second a retry would be admitted, not the next bucket boundary.
func TestRetryAfterReflectsSlidingWindowRecovery(t *testing.T) {
	t.Run("requests", func(t *testing.T) {
		service := newTestService(t, Rule{
			Subject:       "/",
			PeriodSeconds: PeriodMinuteSeconds,
			MaxRequests:   new(int64(10)),
		})
		for i := range 10 {
			_, err := service.Acquire(onPath("/"), windowBase)
			require.NoError(t, err, "Acquire() %d failed: %v", i, err)
		}
		_, err := service.Acquire(onPath("/"), windowBase)
		var exceeded *ExceededError
		require.ErrorAs(t, err, &exceeded)

		// At the boundary (60s) the previous window still weighs 10*(60/60);
		// one second later it decays to 9 and a request fits.
		require.Equal(t, 61*time.Second, exceeded.RetryAfter)
		_, err = service.Acquire(onPath("/"), windowBase.Add(exceeded.RetryAfter-time.Second))
		require.Error(t, err)
		_, err = service.Acquire(onPath("/"), windowBase.Add(exceeded.RetryAfter))
		require.NoError(t, err)
	})

	t.Run("tokens overshoot", func(t *testing.T) {
		service := newTestService(t, Rule{
			Subject:       "/",
			PeriodSeconds: PeriodMinuteSeconds,
			MaxTokens:     new(int64(10)),
		})
		// A single response overshoots the token window threefold; recovery
		// needs the rollover plus enough decay: 30*(60-41)/60 = 9 < 10.
		service.RecordTokens(onPath("/"), 30, windowBase)
		_, err := service.Acquire(onPath("/"), windowBase)
		var exceeded *ExceededError
		require.ErrorAs(t, err, &exceeded)
		require.Equal(t, 101*time.Second, exceeded.RetryAfter)
		_, err = service.Acquire(onPath("/"), windowBase.Add(exceeded.RetryAfter-time.Second))
		require.Error(t, err)
		_, err = service.Acquire(onPath("/"), windowBase.Add(exceeded.RetryAfter))
		require.NoError(t, err)
	})
}

// TestAcquireReportsLongestBlockingRule pins the breach report to the rule
// that blocks longest: with minute and day windows both exhausted, honoring
// the minute rule's Retry-After would just earn a second 429.
func TestAcquireReportsLongestBlockingRule(t *testing.T) {
	service := newTestService(t,
		Rule{Subject: "/", PeriodSeconds: PeriodMinuteSeconds, MaxRequests: new(int64(1))},
		Rule{Subject: "/", PeriodSeconds: PeriodDaySeconds, MaxRequests: new(int64(1))},
	)
	_, err := service.Acquire(onPath("/"), windowBase)
	require.NoError(t, err)

	_, err = service.Acquire(onPath("/"), windowBase)
	var exceeded *ExceededError
	require.ErrorAs(t, err, &exceeded)
	require.Equal(t, PeriodDaySeconds, exceeded.Rule.PeriodSeconds)
	require.Greater(t, exceeded.RetryAfter, time.Minute)
}

func TestAcquireRequestsShareSubtreeCounter(t *testing.T) {
	service := newTestService(t, Rule{
		Subject:       "/team",
		PeriodSeconds: PeriodMinuteSeconds,
		MaxRequests:   new(int64(1)),
	})
	_, err := service.Acquire(onPath("/team/alice"), windowBase)
	require.NoError(t, err)
	_, err = service.Acquire(onPath("/team/bob"), windowBase)
	require.Error(t, err)
	// A sibling path outside the rule subtree is unlimited.
	_, err = service.Acquire(onPath("/team-alpha"), windowBase)
	require.NoError(t, err)
}

func TestPerChildRuleIsolatesDirectChildrenAndSharesDescendants(t *testing.T) {
	service := newTestService(t, Rule{
		Scope: ScopeUserPath, Subject: "/users", PerChild: true,
		PeriodSeconds: PeriodMinuteSeconds, MaxRequests: new(int64(1)),
	})
	_, err := service.Acquire(onPath("/users/alice/app"), windowBase)
	require.NoError(t, err)

	_, err = service.Acquire(onPath("/users/alice/other"), windowBase)
	var exceeded *ExceededError
	require.ErrorAs(t, err, &exceeded)
	require.Equal(t, "/users", exceeded.Rule.Subject)
	require.Equal(t, "/users/alice", exceeded.Rule.EffectiveSubject, "resolved exceeded rule = %+v", exceeded.Rule)
	_, err = service.Acquire(onPath("/users/bob/app"), windowBase)
	require.NoError(t, err)
	_, err = service.Acquire(onPath("/users"), windowBase)
	require.NoError(t, err)

	statuses := service.StatusesForUserPath("/users/alice/app", windowBase)
	require.Len(t, statuses, 1)
	require.Equal(t, int64(1), statuses[0].RequestsUsed)
	require.Equal(t, "/users/alice", statuses[0].Rule.EffectiveSubject)
	got := service.StatusesForUserPath("/users", windowBase)
	require.Empty(t, got)

	global := service.Statuses(windowBase)
	require.Len(t, global, 1)
	require.Nil(t, global[0].RequestsRemaining)
	require.Equal(t, int64(0), global[0].RequestsUsed)
	err = service.ResetRule(ScopeUserPath, "/users", PeriodMinuteSeconds)
	require.NoError(t, err)

	for _, child := range []string{"/users/alice", "/users/bob"} {
		got := service.StatusesForUserPath(child, windowBase)
		require.Len(t, got, 1)
		require.Equal(t, int64(0), got[0].RequestsUsed, "%s status after template reset = %+v", child, got)
	}
}

func TestPerChildExpiryCleanupIsBoundedAndPreservesStaticCounters(t *testing.T) {
	service := newTestService(t,
		Rule{Scope: ScopeUserPath, Subject: "/", PeriodSeconds: PeriodHourSeconds, MaxRequests: new(int64(1000))},
		Rule{Scope: ScopeUserPath, Subject: "/users", PerChild: true, PeriodSeconds: PeriodHourSeconds, MaxRequests: new(int64(1000))},
	)
	now := time.Now().UTC()
	for i := range maxExpiryCleanupBatch + 6 {
		path := "/users/child-" + strconv.Itoa(i)
		_, err := service.Acquire(onPath(path), now)
		require.NoError(t, err, "Acquire(%q) failed: %v", path, err)
	}

	limiter := service.limiter
	limiter.mu.Lock()
	cleanupAt := now.Add(3 * time.Hour).Unix()
	defer limiter.mu.Unlock()
	require.True(t, limiter.pruneCounterExpiries(cleanupAt), "first cleanup reported no remaining due batch")
	require.Len(t, limiter.requests, 7, "request counters after first cleanup")
	require.False(t, limiter.pruneCounterExpiries(cleanupAt), "second cleanup still reports due entries")
	require.Len(t, limiter.requests, 1, "only the static counter should remain after cleanup")
}

func TestAcquireSlidingWindowWeighsPreviousWindow(t *testing.T) {
	service := newTestService(t, Rule{
		Subject:       "/",
		PeriodSeconds: PeriodMinuteSeconds,
		MaxRequests:   new(int64(10)),
	})

	for i := range 10 {
		_, err := service.Acquire(onPath("/"), windowBase)
		require.NoError(t, err, "Acquire() %d failed: %v", i, err)
	}
	_, err := service.Acquire(onPath("/"), windowBase)
	require.Error(t, err)

	// One second into the next window the previous window still weighs
	// 10*(59/60) -> 9, so exactly one request fits.
	next := windowBase.Add(61 * time.Second)
	_, err = service.Acquire(onPath("/"), next)
	require.NoError(t, err)
	_, err = service.Acquire(onPath("/"), next)
	require.Error(t, err)
	// Two full windows later all history is gone.
	_, err = service.Acquire(onPath("/"), windowBase.Add(3*time.Minute))
	require.NoError(t, err)
}

func TestTokenLimitIsPostAccounted(t *testing.T) {
	service := newTestService(t, Rule{
		Subject:       "/team",
		PeriodSeconds: PeriodMinuteSeconds,
		MaxTokens:     new(int64(100)),
	})
	// Tokens are unknown before the response: the first request passes.
	_, err := service.Acquire(onPath("/team/alice"), windowBase)
	require.NoError(t, err)

	service.RecordTokens(onPath("/team/alice"), 150, windowBase)

	_, err = service.Acquire(onPath("/team/alice"), windowBase.Add(time.Second))
	var exceeded *ExceededError
	require.ErrorAs(t, err, &exceeded)
	require.Equal(t, ScopeTokens, exceeded.Scope)
	// The token window rolls over like the request window.
	_, err = service.Acquire(onPath("/team/alice"), windowBase.Add(3*time.Minute))
	require.NoError(t, err)
}

func TestConcurrencyLimitHeldUntilRelease(t *testing.T) {
	service := newTestService(t, Rule{
		Subject:       "/team",
		PeriodSeconds: PeriodConcurrent,
		MaxRequests:   new(int64(1)),
	})

	first, err := service.Acquire(onPath("/team/alice"), windowBase)
	require.NoError(t, err)

	_, err = service.Acquire(onPath("/team/bob"), windowBase)
	var exceeded *ExceededError
	require.ErrorAs(t, err, &exceeded)
	require.Equal(t, ScopeConcurrency, exceeded.Scope)

	first.Release()
	first.Release() // idempotent: must not free a second slot

	second, err := service.Acquire(onPath("/team/bob"), windowBase)
	require.NoError(t, err)
	_, err = service.Acquire(onPath("/team/carol"), windowBase)
	require.Error(t, err)

	second.Release()
}

func TestAcquireHeadersReportMostConstrainedRule(t *testing.T) {
	service := newTestService(t,
		Rule{Subject: "/", PeriodSeconds: PeriodMinuteSeconds, MaxRequests: new(int64(100)), MaxTokens: new(int64(1000))},
		Rule{Subject: "/team", PeriodSeconds: PeriodMinuteSeconds, MaxRequests: new(int64(5))},
	)

	reservation, err := service.Acquire(onPath("/team/alice"), windowBase)
	require.NoError(t, err)

	headers := reservation.Headers()
	require.True(t, headers.HasRequests)
	require.Equal(t, int64(5), headers.RequestLimit)
	require.Equal(t, int64(4), headers.RequestRemaining)
	require.True(t, headers.HasTokens)
	require.Equal(t, int64(1000), headers.TokenLimit)
	require.Equal(t, int64(1000), headers.TokenRemaining)
	require.Greater(t, headers.RequestResetAfter, time.Duration(0))
	require.LessOrEqual(t, headers.RequestResetAfter, time.Minute)
}

func TestAcquireWithoutMatchingRulesIsUnlimited(t *testing.T) {
	service := newTestService(t, Rule{
		Subject:       "/team",
		PeriodSeconds: PeriodMinuteSeconds,
		MaxRequests:   new(int64(1)),
	})

	for i := range 5 {
		reservation, err := service.Acquire(onPath("/other"), windowBase)
		require.NoError(t, err, "Acquire() %d failed: %v", i, err)
		require.False(t, reservation.Headers().HasRequests)
	}
}

func TestRejectedAcquireDoesNotConsumeCounters(t *testing.T) {
	service := newTestService(t,
		Rule{Subject: "/team", PeriodSeconds: PeriodConcurrent, MaxRequests: new(int64(1))},
		Rule{Subject: "/team", PeriodSeconds: PeriodMinuteSeconds, MaxRequests: new(int64(10))},
	)

	held, err := service.Acquire(onPath("/team"), windowBase)
	require.NoError(t, err)
	// Concurrency breach: the request-window counter must stay untouched.
	_, err = service.Acquire(onPath("/team"), windowBase)
	require.Error(t, err)

	statuses := service.Statuses(windowBase)
	for _, status := range statuses {
		if status.Rule.PeriodSeconds == PeriodMinuteSeconds {
			require.Equal(t, int64(1), status.RequestsUsed, "rejected attempt must not count")
		}
	}
	held.Release()
}

func TestStatusesAndResets(t *testing.T) {
	service := newTestService(t, Rule{
		Subject:       "/team",
		PeriodSeconds: PeriodMinuteSeconds,
		MaxRequests:   new(int64(2)),
		MaxTokens:     new(int64(100)),
	})
	_, err := service.Acquire(onPath("/team"), windowBase)
	require.NoError(t, err)

	service.RecordTokens(onPath("/team"), 40, windowBase)

	statuses := service.Statuses(windowBase)
	require.Len(t, statuses, 1)

	status := statuses[0]
	require.Equal(t, int64(1), status.RequestsUsed)
	require.NotNil(t, status.RequestsRemaining)
	require.Equal(t, int64(1), *status.RequestsRemaining)
	require.Equal(t, int64(40), status.TokensUsed)
	require.NotNil(t, status.TokensRemaining)
	require.Equal(t, int64(60), *status.TokensRemaining)
	require.False(t, status.WindowStart.IsZero())
	require.True(t, status.WindowEnd.Equal(status.WindowStart.Add(time.Minute)), "window = %s..%s, want one minute", status.WindowStart, status.WindowEnd)
	err = service.ResetRule(ScopeUserPath, "/team", PeriodMinuteSeconds)
	require.NoError(t, err)

	status = service.Statuses(windowBase)[0]
	require.Equal(t, int64(0), status.RequestsUsed)
	require.Equal(t, int64(0), status.TokensUsed)

	service.RecordTokens(onPath("/team"), 40, windowBase)
	err = service.ResetAll()
	require.NoError(t, err)
	status = service.Statuses(windowBase)[0]
	require.Equal(t, int64(0), status.TokensUsed)
}

func TestStatusesForUserPathFiltersByScopeAndSubtree(t *testing.T) {
	service := newTestService(t,
		Rule{Subject: "/team", PeriodSeconds: PeriodMinuteSeconds, MaxRequests: new(int64(5))},
		Rule{Subject: "/team/alice", PeriodSeconds: PeriodConcurrent, MaxRequests: new(int64(2))},
		Rule{Subject: "/other", PeriodSeconds: PeriodMinuteSeconds, MaxRequests: new(int64(5))},
		Rule{Scope: ScopeProvider, Subject: "openai", PeriodSeconds: PeriodMinuteSeconds, MaxRequests: new(int64(100))},
	)
	_, err := service.Acquire(onPath("/team/alice"), windowBase)
	require.NoError(t, err)

	statuses := service.StatusesForUserPath("/team/alice", windowBase)
	require.Len(t, statuses, 2)

	bySubject := map[string]Status{}
	for _, status := range statuses {
		require.Equal(t, ScopeUserPath, status.Rule.Scope)

		bySubject[status.Rule.Subject] = status
	}
	team, ok := bySubject["/team"]
	require.True(t, ok)
	require.Equal(t, int64(1), team.RequestsUsed)
	require.NotNil(t, team.RequestsRemaining)
	require.Equal(t, int64(4), *team.RequestsRemaining)

	alice, ok := bySubject["/team/alice"]
	require.True(t, ok)
	require.Equal(t, int64(1), alice.InFlight)
	got := service.StatusesForUserPath("/unlimited", windowBase)
	require.Empty(t, got)

	var nilService *Service
	got = nilService.StatusesForUserPath("/team", windowBase)
	require.Nil(t, got)
	got = service.StatusesForUserPath("/te:am", windowBase)
	require.Nil(t, got)
	got = service.StatusesForUserPath("/team/alice", time.Time{})
	require.Len(t, got, 2)
}

func TestUpsertDeleteAndHasTokenRules(t *testing.T) {
	service := newTestService(t)
	require.False(t, service.HasTokenRules())
	err := service.UpsertRules(context.Background(), []Rule{
		{Subject: "/team", PeriodSeconds: PeriodMinuteSeconds, MaxTokens: new(int64(100)), Source: SourceManual},
	})
	require.NoError(t, err)
	require.True(t, service.HasTokenRules())
	err = service.DeleteRule(context.Background(), ScopeUserPath, "/team", PeriodMinuteSeconds)
	require.NoError(t, err)
	require.Empty(t, service.Rules())
	err = service.DeleteRule(context.Background(), ScopeUserPath, "/team", PeriodMinuteSeconds)
	require.ErrorIs(t, err, ErrNotFound)
}

func TestNilServiceIsSafe(t *testing.T) {
	var service *Service
	reservation, err := service.Acquire(onPath("/team"), windowBase)
	require.NoError(t, err)

	reservation.Release()
	service.RecordTokens(onPath("/team"), 10, windowBase)
	statuses := service.Statuses(windowBase)
	require.Nil(t, statuses)
	require.True(t, service.routeAvailableAt("openai", "openai/gpt-4o", windowBase))
}

func TestProviderScopedRules(t *testing.T) {
	service := newTestService(t, Rule{
		Scope:         ScopeProvider,
		Subject:       "openai",
		PeriodSeconds: PeriodMinuteSeconds,
		MaxRequests:   new(int64(1)),
	})

	route := Subjects{UserPath: "/team/alice", Provider: "openai", Model: "openai/gpt-4o"}
	_, err := service.Acquire(route, windowBase)
	require.NoError(t, err)

	// The provider counter is shared across consumers and models.
	other := Subjects{UserPath: "/other", Provider: "OpenAI", Model: "openai/gpt-4o-mini"}
	_, err = service.Acquire(other, windowBase)
	var exceeded *ExceededError
	require.ErrorAs(t, err, &exceeded)
	require.Equal(t, ScopeProvider, exceeded.Rule.Scope)
	msg := exceeded.Error()
	require.Contains(t, msg, "provider openai")
	// Another provider is unaffected.
	_, err = service.Acquire(Subjects{UserPath: "/team", Provider: "anthropic", Model: "anthropic/claude"}, windowBase)
	require.NoError(t, err)
	// Requests with no resolved route (batch) skip provider rules.
	_, err = service.Acquire(onPath("/team/alice"), windowBase)
	require.NoError(t, err)
}

func TestModelScopedRules(t *testing.T) {
	t.Run("bare subject matches any provider", func(t *testing.T) {
		service := newTestService(t, Rule{
			Scope:         ScopeModel,
			Subject:       "gpt-4o",
			PeriodSeconds: PeriodMinuteSeconds,
			MaxRequests:   new(int64(1)),
		})
		_, err := service.Acquire(Subjects{UserPath: "/", Provider: "openai", Model: "openai/gpt-4o"}, windowBase)
		require.NoError(t, err)
		_, err = service.Acquire(Subjects{UserPath: "/", Provider: "azure", Model: "GPT-4o"}, windowBase)
		require.Error(t, err)
		_, err = service.Acquire(Subjects{UserPath: "/", Provider: "openai", Model: "openai/gpt-4o-mini"}, windowBase)
		require.NoError(t, err)
	})

	t.Run("qualified subject pins one provider", func(t *testing.T) {
		service := newTestService(t, Rule{
			Scope:         ScopeModel,
			Subject:       "openai/gpt-4o",
			PeriodSeconds: PeriodMinuteSeconds,
			MaxRequests:   new(int64(1)),
		})
		// Matches the qualified model, and the bare model when the provider agrees.
		_, err := service.Acquire(Subjects{UserPath: "/", Provider: "openai", Model: "gpt-4o"}, windowBase)
		require.NoError(t, err)
		_, err = service.Acquire(Subjects{UserPath: "/", Provider: "openai", Model: "openai/gpt-4o"}, windowBase)
		require.Error(t, err)
		// The same model id on another provider is a different subject.
		_, err = service.Acquire(Subjects{UserPath: "/", Provider: "azure", Model: "gpt-4o"}, windowBase)
		require.NoError(t, err)
	})
}

func TestRouteAvailableProbesWithoutConsuming(t *testing.T) {
	service := newTestService(t,
		Rule{Scope: ScopeProvider, Subject: "openai", PeriodSeconds: PeriodMinuteSeconds, MaxRequests: new(int64(1))},
		Rule{Scope: ScopeProvider, Subject: "openai", PeriodSeconds: PeriodConcurrent, MaxRequests: new(int64(1))},
		Rule{Subject: "/team", PeriodSeconds: PeriodMinuteSeconds, MaxRequests: new(int64(1))},
	)

	// Probing repeatedly consumes nothing.
	for i := range 3 {
		require.True(t, service.routeAvailableAt("openai", "openai/gpt-4o", windowBase), "RouteAvailable() probe %d = false, want true", i)
	}

	held, err := service.Acquire(Subjects{UserPath: "/other", Provider: "openai", Model: "openai/gpt-4o"}, windowBase)
	require.NoError(t, err)

	// The request window is exhausted and the concurrency slot is held.
	require.False(t, service.routeAvailableAt("openai", "openai/gpt-4o", windowBase))
	require.True(t, service.routeAvailableAt("anthropic", "anthropic/claude", windowBase))

	held.Release()
	// User-path saturation must not affect route availability: switching
	// targets cannot relieve a consumer limit.
	_, err = service.Acquire(onPath("/team/app"), windowBase)
	require.NoError(t, err)
	require.True(t, service.routeAvailableAt("anthropic", "anthropic/claude", windowBase))
}

func TestRecordTokensChargesProviderAndModelWindows(t *testing.T) {
	service := newTestService(t,
		Rule{Scope: ScopeProvider, Subject: "openai", PeriodSeconds: PeriodMinuteSeconds, MaxTokens: new(int64(100))},
		Rule{Scope: ScopeModel, Subject: "gpt-4o", PeriodSeconds: PeriodMinuteSeconds, MaxTokens: new(int64(50))},
	)

	// Usage entries carry the executed provider name and a bare model id.
	service.RecordTokens(Subjects{UserPath: "/team", Provider: "openai", Model: "gpt-4o"}, 60, windowBase)

	byScope := map[RuleScope]Status{}
	for _, status := range service.Statuses(windowBase) {
		byScope[status.Rule.Scope] = status
	}
	require.Equal(t, int64(60), byScope[ScopeProvider].TokensUsed)
	require.Equal(t, int64(60), byScope[ScopeModel].TokensUsed)

	// The model window (limit 50) is exhausted; the provider window is not.
	require.False(t, service.routeAvailableAt("openai", "openai/gpt-4o", windowBase))
	require.True(t, service.routeAvailableAt("openai", "openai/gpt-4o-mini", windowBase))
}
