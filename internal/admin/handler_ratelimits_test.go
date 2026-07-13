package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v5"

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

func (s *adminRateLimitStore) Close() error { return nil }

func newRateLimitHandler(t *testing.T, store *adminRateLimitStore) (*Handler, *ratelimit.Service) {
	t.Helper()
	service, err := ratelimit.NewService(context.Background(), store)
	if err != nil {
		t.Fatalf("NewService() failed: %v", err)
	}
	return NewHandler(nil, nil, WithRateLimits(service)), service
}

func adminRateLimitRequest(method, body string) (*echo.Context, *httptest.ResponseRecorder) {
	e := echo.New()
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, "/admin/rate-limits", nil)
	} else {
		req = httptest.NewRequest(method, "/admin/rate-limits", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	return e.NewContext(req, rec), rec
}

func TestRateLimitEndpointsUnavailableWithoutService(t *testing.T) {
	h := NewHandler(nil, nil)
	c, rec := adminRateLimitRequest(http.MethodGet, "")
	if err := h.ListRateLimits(c); err != nil {
		t.Fatalf("ListRateLimits() failed: %v", err)
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}

func TestRateLimitEndpointsUpsertListDelete(t *testing.T) {
	store := &adminRateLimitStore{}
	h, service := newRateLimitHandler(t, store)

	c, rec := adminRateLimitRequest(
		http.MethodPut,
		`{"user_path":"/team/beta","limit_key":{"period":"minute"},"max_requests":100,"max_tokens":5000}`,
	)
	if err := h.UpsertRateLimit(c); err != nil {
		t.Fatalf("UpsertRateLimit() failed: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 body=%s", rec.Code, rec.Body.String())
	}

	var body rateLimitListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(body.RateLimits) != 1 {
		t.Fatalf("rate limits = %d, want 1", len(body.RateLimits))
	}
	item := body.RateLimits[0]
	if item.UserPath != "/team/beta" || item.PeriodSeconds != 60 || item.PeriodLabel != "minute" {
		t.Fatalf("item = %+v, want /team/beta minute", item)
	}
	if item.MaxRequests == nil || *item.MaxRequests != 100 || item.MaxTokens == nil || *item.MaxTokens != 5000 {
		t.Fatalf("item limits = %+v, want 100/5000", item)
	}
	if item.Source != ratelimit.SourceManual {
		t.Fatalf("source = %q, want manual", item.Source)
	}
	if item.RequestsRemaining == nil || *item.RequestsRemaining != 100 {
		t.Fatalf("requests remaining = %v, want 100", item.RequestsRemaining)
	}

	// The service enforces the freshly persisted rule immediately.
	if _, err := service.Acquire(ratelimit.Subjects{UserPath: "/team/beta/app"}, time.Now().UTC()); err != nil {
		t.Fatalf("Acquire() failed: %v", err)
	}

	deleteCtx, deleteRec := adminRateLimitRequest(
		http.MethodDelete,
		`{"user_path":"/team/beta","limit_key":{"period":"minute"}}`,
	)
	if err := h.DeleteRateLimit(deleteCtx); err != nil {
		t.Fatalf("DeleteRateLimit() failed: %v", err)
	}
	if deleteRec.Code != http.StatusOK {
		t.Fatalf("delete status = %d, want 200 body=%s", deleteRec.Code, deleteRec.Body.String())
	}
	var afterDelete rateLimitListResponse
	if err := json.Unmarshal(deleteRec.Body.Bytes(), &afterDelete); err != nil {
		t.Fatalf("decode delete response: %v", err)
	}
	if len(afterDelete.RateLimits) != 0 {
		t.Fatalf("rate limits after delete = %d, want 0", len(afterDelete.RateLimits))
	}
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
			c, rec := adminRateLimitRequest(http.MethodPut, tt.body)
			if err := h.UpsertRateLimit(c); err != nil {
				t.Fatalf("UpsertRateLimit() failed: %v", err)
			}
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestRateLimitEndpointsDeleteMissingRuleReturns404(t *testing.T) {
	store := &adminRateLimitStore{}
	h, _ := newRateLimitHandler(t, store)

	c, rec := adminRateLimitRequest(http.MethodDelete, `{"user_path":"/team","limit_key":{"period":"minute"}}`)
	if err := h.DeleteRateLimit(c); err != nil {
		t.Fatalf("DeleteRateLimit() failed: %v", err)
	}
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 body=%s", rec.Code, rec.Body.String())
	}
}

// rateLimitTestNow is window-aligned so paired Acquire calls in one test can
// never straddle a minute boundary.
var rateLimitTestNow = time.Unix(1_000_000_200, 0).UTC()

func TestRateLimitEndpointsResetCounters(t *testing.T) {
	store := &adminRateLimitStore{}
	h, service := newRateLimitHandler(t, store)
	if err := service.UpsertRules(context.Background(), []ratelimit.Rule{{
		Subject:       "/team",
		PeriodSeconds: ratelimit.PeriodMinuteSeconds,
		MaxRequests:   func() *int64 { v := int64(1); return &v }(),
		Source:        ratelimit.SourceManual,
	}}); err != nil {
		t.Fatalf("UpsertRules() failed: %v", err)
	}

	if _, err := service.Acquire(ratelimit.Subjects{UserPath: "/team"}, rateLimitTestNow); err != nil {
		t.Fatalf("Acquire() failed: %v", err)
	}
	if _, err := service.Acquire(ratelimit.Subjects{UserPath: "/team"}, rateLimitTestNow); err == nil {
		t.Fatal("Acquire() over limit succeeded")
	}

	resetCtx, resetRec := adminRateLimitRequest(http.MethodPost, `{"user_path":"/team","period":"minute"}`)
	if err := h.ResetRateLimit(resetCtx); err != nil {
		t.Fatalf("ResetRateLimit() failed: %v", err)
	}
	if resetRec.Code != http.StatusOK {
		t.Fatalf("reset status = %d, want 200 body=%s", resetRec.Code, resetRec.Body.String())
	}
	if _, err := service.Acquire(ratelimit.Subjects{UserPath: "/team"}, rateLimitTestNow); err != nil {
		t.Fatalf("Acquire() after reset failed: %v", err)
	}

	// Reset-all requires confirmation.
	badCtx, badRec := adminRateLimitRequest(http.MethodPost, `{"confirmation":"nope"}`)
	if err := h.ResetRateLimits(badCtx); err != nil {
		t.Fatalf("ResetRateLimits() failed: %v", err)
	}
	if badRec.Code != http.StatusBadRequest {
		t.Fatalf("unconfirmed reset status = %d, want 400", badRec.Code)
	}

	allCtx, allRec := adminRateLimitRequest(http.MethodPost, `{"confirmation":"reset"}`)
	if err := h.ResetRateLimits(allCtx); err != nil {
		t.Fatalf("ResetRateLimits() failed: %v", err)
	}
	if allRec.Code != http.StatusOK {
		t.Fatalf("reset-all status = %d, want 200 body=%s", allRec.Code, allRec.Body.String())
	}
	if _, err := service.Acquire(ratelimit.Subjects{UserPath: "/team"}, rateLimitTestNow); err != nil {
		t.Fatalf("Acquire() after reset-all failed: %v", err)
	}
}

func TestRateLimitEndpointsProviderAndModelScopes(t *testing.T) {
	store := &adminRateLimitStore{}
	h, service := newRateLimitHandler(t, store)

	c, rec := adminRateLimitRequest(
		http.MethodPut,
		`{"scope":"provider","subject":"OpenAI","limit_key":{"period":"minute"},"max_requests":500}`,
	)
	if err := h.UpsertRateLimit(c); err != nil {
		t.Fatalf("UpsertRateLimit() failed: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 body=%s", rec.Code, rec.Body.String())
	}
	var body rateLimitListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(body.RateLimits) != 1 {
		t.Fatalf("rate limits = %d, want 1", len(body.RateLimits))
	}
	item := body.RateLimits[0]
	if item.Scope != "provider" || item.Subject != "openai" {
		t.Fatalf("item = %+v, want provider openai (normalized lowercase)", item)
	}
	if item.UserPath != "" {
		t.Fatalf("user_path = %q, want empty for provider rules", item.UserPath)
	}

	// The rule gates routes immediately.
	if _, err := service.Acquire(ratelimit.Subjects{UserPath: "/", Provider: "openai", Model: "openai/gpt-4o"}, rateLimitTestNow); err != nil {
		t.Fatalf("Acquire() failed: %v", err)
	}

	// A model rule for the same period coexists; mixed-case subjects are
	// stored lowercase and match lowercase live routes.
	modelCtx, modelRec := adminRateLimitRequest(
		http.MethodPut,
		`{"scope":"model","subject":"OpenAI/GPT-4o","limit_key":{"period":"minute"},"max_tokens":90000}`,
	)
	if err := h.UpsertRateLimit(modelCtx); err != nil {
		t.Fatalf("UpsertRateLimit() model failed: %v", err)
	}
	if modelRec.Code != http.StatusOK {
		t.Fatalf("model status = %d, want 200 body=%s", modelRec.Code, modelRec.Body.String())
	}
	var modelBody rateLimitListResponse
	if err := json.Unmarshal(modelRec.Body.Bytes(), &modelBody); err != nil {
		t.Fatalf("decode model response: %v", err)
	}
	foundModel := false
	for _, item := range modelBody.RateLimits {
		if item.Scope == "model" {
			foundModel = true
			if item.Subject != "openai/gpt-4o" {
				t.Fatalf("model subject = %q, want lowercase openai/gpt-4o", item.Subject)
			}
		}
	}
	if !foundModel {
		t.Fatal("model rule missing from response")
	}
	service.RecordTokens(ratelimit.Subjects{UserPath: "/", Provider: "openai", Model: "gpt-4o"}, 90000, rateLimitTestNow)
	if _, err := service.Acquire(ratelimit.Subjects{UserPath: "/", Provider: "openai", Model: "gpt-4o"}, rateLimitTestNow); err == nil {
		t.Fatal("Acquire() succeeded, want mixed-case rule to limit the lowercase route")
	}

	// Conflicting subject + user_path on a provider rule is rejected, not
	// silently resolved.
	conflictCtx, conflictRec := adminRateLimitRequest(
		http.MethodPut,
		`{"scope":"provider","subject":"openai","user_path":"/team","limit_key":{"period":"minute"},"max_requests":5}`,
	)
	if err := h.UpsertRateLimit(conflictCtx); err != nil {
		t.Fatalf("UpsertRateLimit() failed: %v", err)
	}
	if conflictRec.Code != http.StatusBadRequest {
		t.Fatalf("conflicting subject+user_path status = %d, want 400 body=%s", conflictRec.Code, conflictRec.Body.String())
	}

	// A provider/model rule without a subject is rejected, and user_path must
	// not double as the subject.
	badCtx, badRec := adminRateLimitRequest(
		http.MethodPut,
		`{"scope":"provider","user_path":"/team","limit_key":{"period":"minute"},"max_requests":5}`,
	)
	if err := h.UpsertRateLimit(badCtx); err != nil {
		t.Fatalf("UpsertRateLimit() failed: %v", err)
	}
	if badRec.Code != http.StatusBadRequest {
		t.Fatalf("subjectless provider rule status = %d, want 400 body=%s", badRec.Code, badRec.Body.String())
	}

	// Delete by scope+subject.
	deleteCtx, deleteRec := adminRateLimitRequest(
		http.MethodDelete,
		`{"scope":"provider","subject":"openai","limit_key":{"period":"minute"}}`,
	)
	if err := h.DeleteRateLimit(deleteCtx); err != nil {
		t.Fatalf("DeleteRateLimit() failed: %v", err)
	}
	if deleteRec.Code != http.StatusOK {
		t.Fatalf("delete status = %d, want 200 body=%s", deleteRec.Code, deleteRec.Body.String())
	}
	var afterDelete rateLimitListResponse
	if err := json.Unmarshal(deleteRec.Body.Bytes(), &afterDelete); err != nil {
		t.Fatalf("decode delete response: %v", err)
	}
	if len(afterDelete.RateLimits) != 1 || afterDelete.RateLimits[0].Scope != "model" {
		t.Fatalf("after delete = %+v, want only the model rule", afterDelete.RateLimits)
	}
}
