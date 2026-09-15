package admin

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/budget"
	"github.com/enterpilot/gomodel/internal/echotest"
)

type adminBudgetStore struct {
	budgets  []budget.Budget
	sum      float64
	settings budget.Settings

	resetScope         budget.Scope
	resetSubject       string
	resetPeriodSeconds int64
	resetAllAt         time.Time
	deleteErr          error
	resetErr           error
}

func (s *adminBudgetStore) ListBudgets(context.Context) ([]budget.Budget, error) {
	return append([]budget.Budget(nil), s.budgets...), nil
}

func (s *adminBudgetStore) UpsertBudgets(_ context.Context, budgets []budget.Budget) error {
	for _, item := range budgets {
		normalized, err := budget.NormalizeBudget(item)
		if err != nil {
			return err
		}
		replaced := false
		for i, existing := range s.budgets {
			if existing.Scope == normalized.Scope && existing.Subject == normalized.Subject &&
				existing.PeriodSeconds == normalized.PeriodSeconds {
				s.budgets[i] = normalized
				replaced = true
				break
			}
		}
		if !replaced {
			s.budgets = append(s.budgets, normalized)
		}
	}
	return nil
}

func (s *adminBudgetStore) DeleteBudget(_ context.Context, scope budget.Scope, subject string, periodSeconds int64) error {
	if s.deleteErr != nil {
		return s.deleteErr
	}
	subject, err := budget.NormalizeSubject(scope, subject)
	if err != nil {
		return err
	}
	for i, existing := range s.budgets {
		if existing.Scope == scope && existing.Subject == subject && existing.PeriodSeconds == periodSeconds {
			s.budgets = append(s.budgets[:i], s.budgets[i+1:]...)
			return nil
		}
	}
	return nil
}

func (s *adminBudgetStore) ReplaceConfigBudgets(ctx context.Context, budgets []budget.Budget) error {
	s.budgets = nil
	return s.UpsertBudgets(ctx, budgets)
}

func (s *adminBudgetStore) GetSettings(context.Context) (budget.Settings, error) {
	if s.settings == (budget.Settings{}) {
		return budget.DefaultSettings(), nil
	}
	return s.settings, nil
}

func (s *adminBudgetStore) SaveSettings(_ context.Context, settings budget.Settings) (budget.Settings, error) {
	s.settings = settings
	return settings, nil
}

func (s *adminBudgetStore) ResetBudget(_ context.Context, scope budget.Scope, subject string, periodSeconds int64, at time.Time) error {
	if s.resetErr != nil {
		return s.resetErr
	}
	s.resetScope = scope
	s.resetSubject = subject
	s.resetPeriodSeconds = periodSeconds
	for i := range s.budgets {
		if s.budgets[i].Scope == scope && s.budgets[i].Subject == subject && s.budgets[i].PeriodSeconds == periodSeconds {
			t := at.UTC()
			s.budgets[i].LastResetAt = &t
		}
	}
	return nil
}

func (s *adminBudgetStore) ResetAllBudgets(_ context.Context, at time.Time) error {
	s.resetAllAt = at.UTC()
	for i := range s.budgets {
		t := at.UTC()
		s.budgets[i].LastResetAt = &t
	}
	return nil
}

func (s *adminBudgetStore) SumSpend(_ context.Context, windows []budget.SpendWindow) ([]budget.Spend, error) {
	spends := make([]budget.Spend, len(windows))
	for i := range windows {
		spends[i] = budget.Spend{Total: s.sum, HasUsage: s.sum > 0}
	}
	return spends, nil
}

func (s *adminBudgetStore) Close() error {
	return nil
}

func newBudgetHandler(t *testing.T, store *adminBudgetStore) *Handler {
	t.Helper()
	service, err := budget.NewService(context.Background(), store)
	require.NoError(t, err)

	return NewHandler(nil, nil, WithBudgets(service), WithQuotaTemplatesEnabled(true))
}

func TestBudgetEndpointsRejectPerChildWithoutEntitlement(t *testing.T) {
	store := &adminBudgetStore{}
	service, err := budget.NewService(context.Background(), store)
	require.NoError(t, err)

	h := NewHandler(nil, nil, WithBudgets(service))
	c, rec := echotest.Request(t, http.MethodPut, "/admin/budgets", `{"user_path":"/team","per_child":true,"budget_key":{"period":"daily"},"amount":10}`)
	err = h.UpsertBudget(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, rec.Body.String(), "quota_templates_not_entitled")
	assert.Empty(t, store.budgets)
}

func TestBudgetEndpointsListStatuses(t *testing.T) {
	store := &adminBudgetStore{
		budgets: []budget.Budget{
			{Scope: budget.ScopeUserPath, Subject: "/team", PeriodSeconds: budget.PeriodDailySeconds, Amount: 10},
		},
		sum: 4,
	}
	h := newBudgetHandler(t, store)
	c, rec := echotest.Get(t, "/admin/budgets")
	err := h.ListBudgets(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	body := echotest.Decode[budgetListResponse](t, rec)
	require.Len(t, body.Budgets, 1)
	assert.InDelta(t, 0.4, body.Budgets[0].UsageRatio, 1e-9)
}

func TestBudgetEndpointsUpsertAndResetOneBudget(t *testing.T) {
	store := &adminBudgetStore{}
	h := newBudgetHandler(t, store)

	upsertCtx, upsertRec := echotest.Request(t, http.MethodPut, "/admin/budgets", `{"user_path":"/team/beta","per_child":true,"budget_key":{"period":"weekly"},"amount":12.5}`)
	err := h.UpsertBudget(upsertCtx)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, upsertRec.Code, upsertRec.Body.String())
	require.Len(t, store.budgets, 1)
	assert.Equal(t, "/team/beta", store.budgets[0].Subject)
	assert.True(t, store.budgets[0].PerChild)
	assert.Equal(t, budget.PeriodWeeklySeconds, store.budgets[0].PeriodSeconds)

	resetCtx, resetRec := echotest.Request(t, http.MethodPost, "/admin/budgets/reset-one", `{"user_path":"/team/beta","period_seconds":604800}`)
	err = h.ResetBudget(resetCtx)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resetRec.Code, resetRec.Body.String())
	assert.Equal(t, budget.ScopeUserPath, store.resetScope)
	assert.Equal(t, "/team/beta", store.resetSubject)
	assert.Equal(t, budget.PeriodWeeklySeconds, store.resetPeriodSeconds)
}

func TestBudgetEndpointsUpsertMarksConfigBudgetManual(t *testing.T) {
	store := &adminBudgetStore{
		budgets: []budget.Budget{
			{Scope: budget.ScopeUserPath, Subject: "/team", PeriodSeconds: budget.PeriodDailySeconds, Amount: 10, Source: budget.SourceConfig},
		},
	}
	h := newBudgetHandler(t, store)
	c, rec := echotest.Request(t, http.MethodPut, "/admin/budgets", `{"user_path":"/team","budget_key":{"period":"daily"},"amount":12.5}`)
	err := h.UpsertBudget(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Len(t, store.budgets, 1)
	assert.Equal(t, budget.SourceManual, store.budgets[0].Source)
}

func TestBudgetEndpointsUpsertAcceptsNumericPeriodString(t *testing.T) {
	store := &adminBudgetStore{}
	h := newBudgetHandler(t, store)
	c, rec := echotest.Request(t, http.MethodPut, "/admin/budgets", `{"user_path":"/team","budget_key":{"period":"604800"},"amount":12.5}`)
	err := h.UpsertBudget(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Len(t, store.budgets, 1)
	assert.Equal(t, budget.PeriodWeeklySeconds, store.budgets[0].PeriodSeconds)
}

func TestBudgetEndpointsRejectInvalidBudgetKey(t *testing.T) {
	tests := []struct {
		name string
		body string
		run  func(*Handler, *echo.Context) error
	}{
		{
			name: "missing budget key",
			body: `{"user_path":"/team","amount":12.5}`,
			run:  (*Handler).UpsertBudget,
		},
		{
			name: "empty budget key",
			body: `{"user_path":"/team","budget_key":{},"amount":12.5}`,
			run:  (*Handler).UpsertBudget,
		},
		{
			name: "ambiguous budget key",
			body: `{"user_path":"/team","budget_key":{"period":"daily","period_seconds":86400},"amount":12.5}`,
			run:  (*Handler).UpsertBudget,
		},
		{
			name: "delete missing budget key",
			body: `{"user_path":"/team"}`,
			run:  (*Handler).DeleteBudget,
		},
		{
			name: "unknown scope",
			body: `{"scope":"provider","subject":"openai","budget_key":{"period":"daily"},"amount":12.5}`,
			run:  (*Handler).UpsertBudget,
		},
		{
			name: "label budget without a subject",
			body: `{"scope":"label","budget_key":{"period":"daily"},"amount":12.5}`,
			run:  (*Handler).UpsertBudget,
		},
		{
			name: "label budget naming a user path",
			body: `{"scope":"label","user_path":"/team","budget_key":{"period":"daily"},"amount":12.5}`,
			run:  (*Handler).UpsertBudget,
		},
		{
			name: "label budget with both subject and user path",
			body: `{"scope":"label","subject":"prod","user_path":"/team","budget_key":{"period":"daily"},"amount":12.5}`,
			run:  (*Handler).UpsertBudget,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &adminBudgetStore{}
			h := newBudgetHandler(t, store)
			c, rec := echotest.Request(t, http.MethodPut, "/admin/budgets", tt.body)
			err := tt.run(h, c)
			require.NoError(t, err)
			require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
		})
	}
}

func TestBudgetEndpointsDeleteBudget(t *testing.T) {
	store := &adminBudgetStore{
		budgets: []budget.Budget{
			{Scope: budget.ScopeUserPath, Subject: "/team/beta", PeriodSeconds: budget.PeriodWeeklySeconds, Amount: 12.5},
			{Scope: budget.ScopeUserPath, Subject: "/team/beta", PeriodSeconds: budget.PeriodDailySeconds, Amount: 4},
		},
	}
	h := newBudgetHandler(t, store)
	c, rec := echotest.Request(t, http.MethodDelete, "/admin/budgets", `{"user_path":"/team/beta","budget_key":{"period_seconds":604800}}`)
	err := h.DeleteBudget(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Len(t, store.budgets, 1)
	assert.Equal(t, budget.PeriodDailySeconds, store.budgets[0].PeriodSeconds)
}

func TestBudgetEndpointsMissingMutationsReturnNotFound(t *testing.T) {
	tests := []struct {
		name   string
		store  adminBudgetStore
		method string
		target string
		body   string
		run    func(*Handler, *echo.Context) error
	}{
		{
			name:   "delete missing budget",
			store:  adminBudgetStore{deleteErr: budget.ErrNotFound},
			method: http.MethodDelete,
			target: "/admin/budgets",
			body:   `{"user_path":"/team","budget_key":{"period_seconds":86400}}`,
			run:    (*Handler).DeleteBudget,
		},
		{
			name:   "reset missing budget",
			store:  adminBudgetStore{resetErr: budget.ErrNotFound},
			method: http.MethodPost,
			target: "/admin/budgets/reset-one",
			body:   `{"user_path":"/team","period_seconds":86400}`,
			run:    (*Handler).ResetBudget,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newBudgetHandler(t, &tt.store)
			c, rec := echotest.Request(t, tt.method, tt.target, tt.body)
			err := tt.run(h, c)
			require.NoError(t, err)
			require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
			assert.Contains(t, rec.Body.String(), `"code":"budget_not_found"`)
		})
	}
}

func TestBudgetSettingsEndpoints(t *testing.T) {
	store := &adminBudgetStore{}
	h := newBudgetHandler(t, store)

	getCtx, getRec := echotest.Get(t, "/admin/budgets/settings")
	err := h.BudgetSettings(getCtx)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, getRec.Code, getRec.Body.String())

	defaults := echotest.Decode[budget.Settings](t, getRec)
	assert.Equal(t, 1, defaults.MonthlyResetDay)
	assert.Equal(t, int(time.Monday), defaults.WeeklyResetWeekday)

	updateCtx, updateRec := echotest.Request(t, http.MethodPut, "/admin/budgets/settings", `{"daily_reset_hour":6,"daily_reset_minute":30,"weekly_reset_weekday":2,"weekly_reset_hour":9,"weekly_reset_minute":15,"monthly_reset_day":31,"monthly_reset_hour":2,"monthly_reset_minute":45}`)
	err = h.UpdateBudgetSettings(updateCtx)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, updateRec.Code, updateRec.Body.String())
	assert.Equal(t, 6, store.settings.DailyResetHour)
	assert.Equal(t, 31, store.settings.MonthlyResetDay)

	partialCtx, partialRec := echotest.Request(t, http.MethodPut, "/admin/budgets/settings", `{"daily_reset_hour":8}`)
	err = h.UpdateBudgetSettings(partialCtx)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, partialRec.Code, partialRec.Body.String())
	assert.Equal(t, 8, store.settings.DailyResetHour)
	assert.Equal(t, 30, store.settings.DailyResetMinute, "partial update must preserve existing values")
	assert.Equal(t, 31, store.settings.MonthlyResetDay, "partial update must preserve existing values")

	invalidCtx, invalidRec := echotest.Request(t, http.MethodPut, "/admin/budgets/settings", `{"daily_reset_hour":24,"daily_reset_minute":0,"weekly_reset_weekday":1,"weekly_reset_hour":0,"weekly_reset_minute":0,"monthly_reset_day":1,"monthly_reset_hour":0,"monthly_reset_minute":0}`)
	err = h.UpdateBudgetSettings(invalidCtx)
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, invalidRec.Code, invalidRec.Body.String())

	malformedCtx, malformedRec := echotest.Request(t, http.MethodPut, "/admin/budgets/settings", `{"daily_reset_hour":`)
	err = h.UpdateBudgetSettings(malformedCtx)
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, malformedRec.Code, malformedRec.Body.String())
}

func TestResetBudgetsEndpoint(t *testing.T) {
	store := &adminBudgetStore{
		budgets: []budget.Budget{
			{Scope: budget.ScopeUserPath, Subject: "/team", PeriodSeconds: budget.PeriodDailySeconds, Amount: 10},
		},
	}
	h := newBudgetHandler(t, store)

	badCtx, badRec := echotest.Request(t, http.MethodPost, "/admin/budgets/reset", `{"confirm":"no"}`)
	err := h.ResetBudgets(badCtx)
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, badRec.Code, badRec.Body.String())
	assert.True(t, store.resetAllAt.IsZero(), "unconfirmed reset must not touch the store")

	c, rec := echotest.Request(t, http.MethodPost, "/admin/budgets/reset", `{"confirm":"reset"}`)
	err = h.ResetBudgets(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.False(t, store.resetAllAt.IsZero())
	require.NotNil(t, store.budgets[0].LastResetAt)
	assert.True(t, store.budgets[0].LastResetAt.Equal(store.resetAllAt), "budget reset time = %v, want %s", store.budgets[0].LastResetAt, store.resetAllAt)

	body := echotest.Decode[resetBudgetsResponse](t, rec)
	assert.Equal(t, "ok", body.Status)
}

// The API accepts a label budget and keeps its subject verbatim; the response
// omits user_path, which only names user-path budgets.
func TestBudgetEndpointsUpsertLabelBudget(t *testing.T) {
	store := &adminBudgetStore{}
	h := newBudgetHandler(t, store)
	c, rec := echotest.Request(t, http.MethodPut, "/admin/budgets", `{"scope":"label","subject":"Mobile-App-iOS","budget_key":{"period":"monthly"},"amount":500}`)
	err := h.UpsertBudget(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Len(t, store.budgets, 1)

	stored := store.budgets[0]
	assert.Equal(t, budget.ScopeLabel, stored.Scope)
	assert.Equal(t, "Mobile-App-iOS", stored.Subject)

	body := echotest.Decode[budgetListResponse](t, rec)
	require.Len(t, body.Budgets, 1)
	got := body.Budgets[0]
	assert.Equal(t, "label", got.Scope)
	assert.Equal(t, "Mobile-App-iOS", got.Subject)
	assert.Empty(t, got.UserPath)
}

// A user path and a label can spell the same subject; deleting one must leave
// the other in place.
func TestBudgetEndpointsDeleteDistinguishesScopes(t *testing.T) {
	store := &adminBudgetStore{
		budgets: []budget.Budget{
			{Scope: budget.ScopeUserPath, Subject: "/prod", PeriodSeconds: budget.PeriodDailySeconds, Amount: 10},
			{Scope: budget.ScopeLabel, Subject: "/prod", PeriodSeconds: budget.PeriodDailySeconds, Amount: 20},
		},
	}
	h := newBudgetHandler(t, store)
	c, rec := echotest.Request(t, http.MethodDelete, "/admin/budgets", `{"scope":"label","subject":"/prod","budget_key":{"period":"daily"}}`)
	err := h.DeleteBudget(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Len(t, store.budgets, 1)
	assert.Equal(t, budget.ScopeUserPath, store.budgets[0].Scope)
}
