package admin

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/echotest"
	"github.com/enterpilot/gomodel/internal/ratelimit"
)

type adminRateLimitStore struct {
	rules []ratelimit.Rule
}

func (s *adminRateLimitStore) ListRules(context.Context) ([]ratelimit.Rule, error) {
	return append([]ratelimit.Rule(nil), s.rules...), nil
}

func (s *adminRateLimitStore) UpsertRules(_ context.Context, rules []ratelimit.Rule) error {
	for _, item := range rules {
		normalized, err := ratelimit.NormalizeRule(item)
		if err != nil {
			return err
		}
		replaced := false
		for i, existing := range s.rules {
			if existing.Scope == normalized.Scope && existing.Subject == normalized.Subject && existing.PeriodSeconds == normalized.PeriodSeconds {
				s.rules[i] = normalized
				replaced = true
				break
			}
		}
		if !replaced {
			s.rules = append(s.rules, normalized)
		}
	}
	return nil
}

func (s *adminRateLimitStore) DeleteRule(_ context.Context, scope ratelimit.RuleScope, subject string, periodSeconds int64) error {
	for i, existing := range s.rules {
		if existing.Scope == scope && existing.Subject == subject && existing.PeriodSeconds == periodSeconds {
			s.rules = append(s.rules[:i], s.rules[i+1:]...)
			return nil
		}
	}
	return ratelimit.ErrNotFound
}

func (s *adminRateLimitStore) ReplaceConfigRules(ctx context.Context, rules []ratelimit.Rule) error {
	s.rules = nil
	return s.UpsertRules(ctx, rules)
}

func (s *adminRateLimitStore) LoadCounters(context.Context) ([]ratelimit.WindowSnapshot, error) {
	return nil, nil
}
func (s *adminRateLimitStore) SaveCounters(context.Context, []ratelimit.WindowSnapshot) error {
	return nil
}
func (s *adminRateLimitStore) DeleteCounter(context.Context, ratelimit.RuleScope, string, int64) error {
	return nil
}
func (s *adminRateLimitStore) DeleteAllCounters(context.Context) error { return nil }

func (s *adminRateLimitStore) Close() error { return nil }

func newRateLimitHandler(t *testing.T, store *adminRateLimitStore) (*Handler, *ratelimit.Service) {
	t.Helper()
	service, err := ratelimit.NewService(context.Background(), store)
	require.NoError(t, err)

	return NewHandler(nil, nil, WithRateLimits(service), WithQuotaTemplatesEnabled(true)), service
}

func TestRateLimitEndpointsRejectPerChildWithoutEntitlement(t *testing.T) {
	store := &adminRateLimitStore{}
	service, err := ratelimit.NewService(context.Background(), store)
	require.NoError(t, err)

	h := NewHandler(nil, nil, WithRateLimits(service))
	c, rec := echotest.Request(t, http.MethodPut, "/admin/rate-limits", `{"user_path":"/team","per_child":true,"limit_key":{"period":"minute"},"max_requests":10}`)
	err = h.UpsertRateLimit(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, rec.Body.String(), "quota_templates_not_entitled")
	assert.Empty(t, store.rules)
}

func TestRateLimitEndpointsUnavailableWithoutService(t *testing.T) {
	h := NewHandler(nil, nil)
	c, rec := echotest.Get(t, "/admin/rate-limits")
	err := h.ListRateLimits(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

func TestRateLimitEndpointsUpsertListDelete(t *testing.T) {
	store := &adminRateLimitStore{}
	h, service := newRateLimitHandler(t, store)

	c, rec := echotest.Request(t, http.MethodPut, "/admin/rate-limits", `{"user_path":"/team/beta","per_child":true,"limit_key":{"period":"minute"},"max_requests":100,"max_tokens":5000}`)
	err := h.UpsertRateLimit(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	body := echotest.Decode[rateLimitListResponse](t, rec)
	require.Len(t, body.RateLimits, 1)

	item := body.RateLimits[0]
	assert.Equal(t, "/team/beta", item.UserPath)
	assert.Equal(t, int64(60), item.PeriodSeconds)
	assert.Equal(t, "minute", item.PeriodLabel)
	assert.True(t, item.PerChild)
	require.NotNil(t, item.MaxRequests)
	assert.Equal(t, int64(100), *item.MaxRequests)
	require.NotNil(t, item.MaxTokens)
	assert.Equal(t, int64(5000), *item.MaxTokens)
	assert.Equal(t, ratelimit.SourceManual, item.Source)
	assert.Nil(t, item.RequestsRemaining)

	// The service enforces the freshly persisted rule immediately.
	_, err = service.Acquire(ratelimit.Subjects{UserPath: "/team/beta/app"}, time.Now().UTC())
	require.NoError(t, err)

	deleteCtx, deleteRec := echotest.Request(t, http.MethodDelete, "/admin/rate-limits", `{"user_path":"/team/beta","limit_key":{"period":"minute"}}`)
	err = h.DeleteRateLimit(deleteCtx)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, deleteRec.Code, deleteRec.Body.String())

	afterDelete := echotest.Decode[rateLimitListResponse](t, deleteRec)
	assert.Empty(t, afterDelete.RateLimits)
}

func TestRateLimitEndpointsRejectInvalidRequests(t *testing.T) {
	store := &adminRateLimitStore{}
	h, _ := newRateLimitHandler(t, store)

	tests := []struct {
		name string
		body string
	}{
		{"missing limit key", `{"user_path":"/team","max_requests":10}`},
		{"both period forms", `{"user_path":"/team","limit_key":{"period":"minute","period_seconds":60},"max_requests":10}`},
		{"no limits", `{"user_path":"/team","limit_key":{"period":"minute"}}`},
		{"tokens on concurrent", `{"user_path":"/team","limit_key":{"period":"concurrent"},"max_requests":5,"max_tokens":10}`},
		{"unknown period", `{"user_path":"/team","limit_key":{"period":"fortnight"},"max_requests":5}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, rec := echotest.Request(t, http.MethodPut, "/admin/rate-limits", tt.body)
			err := h.UpsertRateLimit(c)
			require.NoError(t, err)
			require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
		})
	}
}

func TestRateLimitEndpointsDeleteMissingRuleReturns404(t *testing.T) {
	store := &adminRateLimitStore{}
	h, _ := newRateLimitHandler(t, store)

	c, rec := echotest.Request(t, http.MethodDelete, "/admin/rate-limits", `{"user_path":"/team","limit_key":{"period":"minute"}}`)
	err := h.DeleteRateLimit(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
}

// rateLimitTestNow is window-aligned so paired Acquire calls in one test can
// never straddle a minute boundary.
var rateLimitTestNow = time.Unix(1_000_000_200, 0).UTC()

func TestRateLimitEndpointsResetCounters(t *testing.T) {
	store := &adminRateLimitStore{}
	h, service := newRateLimitHandler(t, store)
	err := service.UpsertRules(context.Background(), []ratelimit.Rule{{
		Subject:       "/team",
		PeriodSeconds: ratelimit.PeriodMinuteSeconds,
		MaxRequests:   func() *int64 { v := int64(1); return &v }(),
		Source:        ratelimit.SourceManual,
	}})
	require.NoError(t, err)
	_, err = service.Acquire(ratelimit.Subjects{UserPath: "/team"}, rateLimitTestNow)
	require.NoError(t, err)
	_, err = service.Acquire(ratelimit.Subjects{UserPath: "/team"}, rateLimitTestNow)
	require.Error(t, err)

	resetCtx, resetRec := echotest.Request(t, http.MethodPost, "/admin/rate-limits", `{"user_path":"/team","period":"minute"}`)
	err = h.ResetRateLimit(resetCtx)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resetRec.Code, resetRec.Body.String())
	_, err = service.Acquire(ratelimit.Subjects{UserPath: "/team"}, rateLimitTestNow)
	require.NoError(t, err)

	// Reset-all requires confirmation.
	badCtx, badRec := echotest.Request(t, http.MethodPost, "/admin/rate-limits", `{"confirmation":"nope"}`)
	err = h.ResetRateLimits(badCtx)
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, badRec.Code)

	allCtx, allRec := echotest.Request(t, http.MethodPost, "/admin/rate-limits", `{"confirmation":"reset"}`)
	err = h.ResetRateLimits(allCtx)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, allRec.Code, allRec.Body.String())
	_, err = service.Acquire(ratelimit.Subjects{UserPath: "/team"}, rateLimitTestNow)
	require.NoError(t, err)
}

func TestRateLimitEndpointsProviderAndModelScopes(t *testing.T) {
	store := &adminRateLimitStore{}
	h, service := newRateLimitHandler(t, store)

	c, rec := echotest.Request(t, http.MethodPut, "/admin/rate-limits", `{"scope":"provider","subject":"OpenAI","limit_key":{"period":"minute"},"max_requests":500}`)
	err := h.UpsertRateLimit(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	body := echotest.Decode[rateLimitListResponse](t, rec)
	require.Len(t, body.RateLimits, 1)

	item := body.RateLimits[0]
	assert.Equal(t, "provider", item.Scope)
	assert.Equal(t, "openai", item.Subject, "subject must be normalized to lowercase")
	assert.Empty(t, item.UserPath)

	// The rule gates routes immediately.
	_, err = service.Acquire(ratelimit.Subjects{UserPath: "/", Provider: "openai", Model: "openai/gpt-4o"}, rateLimitTestNow)
	require.NoError(t, err)

	// A model rule for the same period coexists; mixed-case subjects are
	// stored lowercase and match lowercase live routes.
	modelCtx, modelRec := echotest.Request(t, http.MethodPut, "/admin/rate-limits", `{"scope":"model","subject":"OpenAI/GPT-4o","limit_key":{"period":"minute"},"max_tokens":90000}`)
	err = h.UpsertRateLimit(modelCtx)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, modelRec.Code, modelRec.Body.String())

	modelBody := echotest.Decode[rateLimitListResponse](t, modelRec)

	var modelSubjects []string
	for _, item := range modelBody.RateLimits {
		if item.Scope == "model" {
			modelSubjects = append(modelSubjects, item.Subject)
		}
	}
	assert.Equal(t, []string{"openai/gpt-4o"}, modelSubjects)

	service.RecordTokens(ratelimit.Subjects{UserPath: "/", Provider: "openai", Model: "gpt-4o"}, 90000, rateLimitTestNow)
	_, err = service.Acquire(ratelimit.Subjects{UserPath: "/", Provider: "openai", Model: "gpt-4o"}, rateLimitTestNow)
	require.Error(t, err)

	// Conflicting subject + user_path on a provider rule is rejected, not
	// silently resolved.
	conflictCtx, conflictRec := echotest.Request(t, http.MethodPut, "/admin/rate-limits", `{"scope":"provider","subject":"openai","user_path":"/team","limit_key":{"period":"minute"},"max_requests":5}`)
	err = h.UpsertRateLimit(conflictCtx)
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, conflictRec.Code, conflictRec.Body.String())

	// A provider/model rule without a subject is rejected, and user_path must
	// not double as the subject.
	badCtx, badRec := echotest.Request(t, http.MethodPut, "/admin/rate-limits", `{"scope":"provider","user_path":"/team","limit_key":{"period":"minute"},"max_requests":5}`)
	err = h.UpsertRateLimit(badCtx)
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, badRec.Code, badRec.Body.String())

	// Delete by scope+subject.
	deleteCtx, deleteRec := echotest.Request(t, http.MethodDelete, "/admin/rate-limits", `{"scope":"provider","subject":"openai","limit_key":{"period":"minute"}}`)
	err = h.DeleteRateLimit(deleteCtx)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, deleteRec.Code, deleteRec.Body.String())

	afterDelete := echotest.Decode[rateLimitListResponse](t, deleteRec)
	require.Len(t, afterDelete.RateLimits, 1)
	assert.Equal(t, "model", afterDelete.RateLimits[0].Scope)
}
