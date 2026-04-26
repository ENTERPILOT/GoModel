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

	"gomodel/internal/budget"
)

type adminBudgetStore struct {
	budgets  []budget.Budget
	sum      float64
	settings budget.Settings

	resetUserPath      string
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
			if existing.UserPath == normalized.UserPath && existing.PeriodSeconds == normalized.PeriodSeconds {
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

func (s *adminBudgetStore) DeleteBudget(_ context.Context, userPath string, periodSeconds int64) error {
	if s.deleteErr != nil {
		return s.deleteErr
	}
	normalizedPath, err := budget.NormalizeUserPath(userPath)
	if err != nil {
		return err
	}
	for i, existing := range s.budgets {
		if existing.UserPath == normalizedPath && existing.PeriodSeconds == periodSeconds {
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

func (s *adminBudgetStore) ResetBudget(_ context.Context, userPath string, periodSeconds int64, at time.Time) error {
	if s.resetErr != nil {
		return s.resetErr
	}
	s.resetUserPath = userPath
	s.resetPeriodSeconds = periodSeconds
	for i := range s.budgets {
		if s.budgets[i].UserPath == userPath && s.budgets[i].PeriodSeconds == periodSeconds {
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

func (s *adminBudgetStore) SumUsageCost(context.Context, string, time.Time, time.Time) (float64, bool, error) {
	return s.sum, s.sum > 0, nil
}

func (s *adminBudgetStore) Close() error {
	return nil
}

func newBudgetHandler(t *testing.T, store *adminBudgetStore) *Handler {
	t.Helper()
	service, err := budget.NewService(context.Background(), store)
	if err != nil {
		t.Fatalf("NewService() failed: %v", err)
	}
	return NewHandler(nil, nil, WithBudgets(service))
}

func TestBudgetEndpointsListStatuses(t *testing.T) {
	store := &adminBudgetStore{
		budgets: []budget.Budget{
			{UserPath: "/team", PeriodSeconds: budget.PeriodDailySeconds, Amount: 10},
		},
		sum: 4,
	}
	h := newBudgetHandler(t, store)
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/admin/api/v1/budgets", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	if err := h.ListBudgets(c); err != nil {
		t.Fatalf("ListBudgets() failed: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var body budgetListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(body.Budgets) != 1 {
		t.Fatalf("expected 1 budget, got %d", len(body.Budgets))
	}
	if got := body.Budgets[0].UsageRatio; got != 0.4 {
		t.Fatalf("usage_ratio = %v, want 0.4", got)
	}
}

func TestBudgetEndpointsUpsertAndResetOneBudget(t *testing.T) {
	store := &adminBudgetStore{}
	h := newBudgetHandler(t, store)
	e := echo.New()

	upsertReq := httptest.NewRequest(
		http.MethodPut,
		"/admin/api/v1/budgets/%2Fteam%2Fbeta/weekly",
		strings.NewReader(`{"amount":12.5}`),
	)
	upsertReq.Header.Set("Content-Type", "application/json")
	upsertRec := httptest.NewRecorder()
	upsertCtx := e.NewContext(upsertReq, upsertRec)
	upsertCtx.SetPathValues(echo.PathValues{
		{Name: "user_path", Value: "%2Fteam%2Fbeta"},
		{Name: "period", Value: "weekly"},
	})
	if err := h.UpsertBudget(upsertCtx); err != nil {
		t.Fatalf("UpsertBudget() failed: %v", err)
	}
	if upsertRec.Code != http.StatusOK {
		t.Fatalf("upsert status = %d, want %d body=%s", upsertRec.Code, http.StatusOK, upsertRec.Body.String())
	}
	if len(store.budgets) != 1 || store.budgets[0].UserPath != "/team/beta" || store.budgets[0].PeriodSeconds != budget.PeriodWeeklySeconds {
		t.Fatalf("stored budgets = %+v", store.budgets)
	}

	resetReq := httptest.NewRequest(
		http.MethodPost,
		"/admin/api/v1/budgets/reset-one",
		strings.NewReader(`{"user_path":"/team/beta","period_seconds":604800}`),
	)
	resetReq.Header.Set("Content-Type", "application/json")
	resetRec := httptest.NewRecorder()
	resetCtx := e.NewContext(resetReq, resetRec)
	if err := h.ResetBudget(resetCtx); err != nil {
		t.Fatalf("ResetBudget() failed: %v", err)
	}
	if resetRec.Code != http.StatusOK {
		t.Fatalf("reset status = %d, want %d body=%s", resetRec.Code, http.StatusOK, resetRec.Body.String())
	}
	if store.resetUserPath != "/team/beta" || store.resetPeriodSeconds != budget.PeriodWeeklySeconds {
		t.Fatalf("reset key = %s/%d", store.resetUserPath, store.resetPeriodSeconds)
	}
}

func TestBudgetEndpointsUpsertMarksConfigBudgetManual(t *testing.T) {
	store := &adminBudgetStore{
		budgets: []budget.Budget{
			{UserPath: "/team", PeriodSeconds: budget.PeriodDailySeconds, Amount: 10, Source: budget.SourceConfig},
		},
	}
	h := newBudgetHandler(t, store)
	e := echo.New()
	req := httptest.NewRequest(
		http.MethodPut,
		"/admin/api/v1/budgets/%2Fteam/daily",
		strings.NewReader(`{"amount":12.5}`),
	)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPathValues(echo.PathValues{
		{Name: "user_path", Value: "%2Fteam"},
		{Name: "period", Value: "daily"},
	})

	if err := h.UpsertBudget(c); err != nil {
		t.Fatalf("UpsertBudget() failed: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if len(store.budgets) != 1 {
		t.Fatalf("stored budgets = %+v, want one budget", store.budgets)
	}
	if got := store.budgets[0].Source; got != budget.SourceManual {
		t.Fatalf("budget source = %q, want %q", got, budget.SourceManual)
	}
}

func TestBudgetEndpointsDeleteBudget(t *testing.T) {
	store := &adminBudgetStore{
		budgets: []budget.Budget{
			{UserPath: "/team/beta", PeriodSeconds: budget.PeriodWeeklySeconds, Amount: 12.5},
			{UserPath: "/team/beta", PeriodSeconds: budget.PeriodDailySeconds, Amount: 4},
		},
	}
	h := newBudgetHandler(t, store)
	e := echo.New()
	req := httptest.NewRequest(
		http.MethodDelete,
		"/admin/api/v1/budgets/%2Fteam%2Fbeta/604800",
		nil,
	)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPathValues(echo.PathValues{
		{Name: "user_path", Value: "%2Fteam%2Fbeta"},
		{Name: "period", Value: "604800"},
	})

	if err := h.DeleteBudget(c); err != nil {
		t.Fatalf("DeleteBudget() failed: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("delete status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if len(store.budgets) != 1 || store.budgets[0].PeriodSeconds != budget.PeriodDailySeconds {
		t.Fatalf("stored budgets after delete = %+v", store.budgets)
	}
}

func TestBudgetEndpointsMissingMutationsReturnNotFound(t *testing.T) {
	tests := []struct {
		name  string
		run   func(*Handler, *echo.Echo) *httptest.ResponseRecorder
		setup func(*adminBudgetStore)
	}{
		{
			name: "delete missing budget",
			setup: func(store *adminBudgetStore) {
				store.deleteErr = budget.ErrNotFound
			},
			run: func(h *Handler, e *echo.Echo) *httptest.ResponseRecorder {
				req := httptest.NewRequest(http.MethodDelete, "/admin/api/v1/budgets/%2Fteam/86400", nil)
				rec := httptest.NewRecorder()
				c := e.NewContext(req, rec)
				c.SetPathValues(echo.PathValues{
					{Name: "user_path", Value: "%2Fteam"},
					{Name: "period", Value: "86400"},
				})
				if err := h.DeleteBudget(c); err != nil {
					t.Fatalf("DeleteBudget() returned handler error: %v", err)
				}
				return rec
			},
		},
		{
			name: "reset missing budget",
			setup: func(store *adminBudgetStore) {
				store.resetErr = budget.ErrNotFound
			},
			run: func(h *Handler, e *echo.Echo) *httptest.ResponseRecorder {
				req := httptest.NewRequest(
					http.MethodPost,
					"/admin/api/v1/budgets/reset-one",
					strings.NewReader(`{"user_path":"/team","period_seconds":86400}`),
				)
				req.Header.Set("Content-Type", "application/json")
				rec := httptest.NewRecorder()
				c := e.NewContext(req, rec)
				if err := h.ResetBudget(c); err != nil {
					t.Fatalf("ResetBudget() returned handler error: %v", err)
				}
				return rec
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &adminBudgetStore{}
			tt.setup(store)
			h := newBudgetHandler(t, store)
			e := echo.New()

			rec := tt.run(h, e)

			if rec.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusNotFound, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), `"code":"budget_not_found"`) {
				t.Fatalf("body = %s, want budget_not_found code", rec.Body.String())
			}
		})
	}
}

func TestBudgetSettingsEndpoints(t *testing.T) {
	store := &adminBudgetStore{}
	h := newBudgetHandler(t, store)
	e := echo.New()

	getReq := httptest.NewRequest(http.MethodGet, "/admin/api/v1/budgets/settings", nil)
	getRec := httptest.NewRecorder()
	getCtx := e.NewContext(getReq, getRec)
	if err := h.BudgetSettings(getCtx); err != nil {
		t.Fatalf("BudgetSettings() failed: %v", err)
	}
	if getRec.Code != http.StatusOK {
		t.Fatalf("settings status = %d, want %d body=%s", getRec.Code, http.StatusOK, getRec.Body.String())
	}
	var defaults budget.Settings
	if err := json.Unmarshal(getRec.Body.Bytes(), &defaults); err != nil {
		t.Fatalf("decode settings: %v", err)
	}
	if defaults.MonthlyResetDay != 1 || defaults.WeeklyResetWeekday != int(time.Monday) {
		t.Fatalf("default settings = %+v", defaults)
	}

	updateReq := httptest.NewRequest(
		http.MethodPut,
		"/admin/api/v1/budgets/settings",
		strings.NewReader(`{"daily_reset_hour":6,"daily_reset_minute":30,"weekly_reset_weekday":2,"weekly_reset_hour":9,"weekly_reset_minute":15,"monthly_reset_day":31,"monthly_reset_hour":2,"monthly_reset_minute":45}`),
	)
	updateReq.Header.Set("Content-Type", "application/json")
	updateRec := httptest.NewRecorder()
	updateCtx := e.NewContext(updateReq, updateRec)
	if err := h.UpdateBudgetSettings(updateCtx); err != nil {
		t.Fatalf("UpdateBudgetSettings() failed: %v", err)
	}
	if updateRec.Code != http.StatusOK {
		t.Fatalf("update settings status = %d, want %d body=%s", updateRec.Code, http.StatusOK, updateRec.Body.String())
	}
	if store.settings.DailyResetHour != 6 || store.settings.MonthlyResetDay != 31 {
		t.Fatalf("stored settings = %+v", store.settings)
	}

	invalidReq := httptest.NewRequest(
		http.MethodPut,
		"/admin/api/v1/budgets/settings",
		strings.NewReader(`{"daily_reset_hour":24,"daily_reset_minute":0,"weekly_reset_weekday":1,"weekly_reset_hour":0,"weekly_reset_minute":0,"monthly_reset_day":1,"monthly_reset_hour":0,"monthly_reset_minute":0}`),
	)
	invalidReq.Header.Set("Content-Type", "application/json")
	invalidRec := httptest.NewRecorder()
	invalidCtx := e.NewContext(invalidReq, invalidRec)
	if err := h.UpdateBudgetSettings(invalidCtx); err != nil {
		t.Fatalf("invalid UpdateBudgetSettings() returned handler error: %v", err)
	}
	if invalidRec.Code != http.StatusBadRequest {
		t.Fatalf("invalid settings status = %d, want %d body=%s", invalidRec.Code, http.StatusBadRequest, invalidRec.Body.String())
	}

	malformedReq := httptest.NewRequest(
		http.MethodPut,
		"/admin/api/v1/budgets/settings",
		strings.NewReader(`{"daily_reset_hour":`),
	)
	malformedReq.Header.Set("Content-Type", "application/json")
	malformedRec := httptest.NewRecorder()
	malformedCtx := e.NewContext(malformedReq, malformedRec)
	if err := h.UpdateBudgetSettings(malformedCtx); err != nil {
		t.Fatalf("malformed UpdateBudgetSettings() returned handler error: %v", err)
	}
	if malformedRec.Code != http.StatusBadRequest {
		t.Fatalf("malformed settings status = %d, want %d body=%s", malformedRec.Code, http.StatusBadRequest, malformedRec.Body.String())
	}
}

func TestResetBudgetsEndpoint(t *testing.T) {
	store := &adminBudgetStore{
		budgets: []budget.Budget{
			{UserPath: "/team", PeriodSeconds: budget.PeriodDailySeconds, Amount: 10},
		},
	}
	h := newBudgetHandler(t, store)
	e := echo.New()

	badReq := httptest.NewRequest(http.MethodPost, "/admin/api/v1/budgets/reset", strings.NewReader(`{"confirm":"no"}`))
	badReq.Header.Set("Content-Type", "application/json")
	badRec := httptest.NewRecorder()
	badCtx := e.NewContext(badReq, badRec)
	if err := h.ResetBudgets(badCtx); err != nil {
		t.Fatalf("invalid ResetBudgets() returned handler error: %v", err)
	}
	if badRec.Code != http.StatusBadRequest {
		t.Fatalf("invalid reset status = %d, want %d body=%s", badRec.Code, http.StatusBadRequest, badRec.Body.String())
	}
	if !store.resetAllAt.IsZero() {
		t.Fatalf("resetAllAt = %s, want zero", store.resetAllAt)
	}

	req := httptest.NewRequest(http.MethodPost, "/admin/api/v1/budgets/reset", strings.NewReader(`{"confirm":"reset"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	if err := h.ResetBudgets(c); err != nil {
		t.Fatalf("ResetBudgets() failed: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("reset all status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if store.resetAllAt.IsZero() {
		t.Fatal("ResetAllBudgets was not called")
	}
	if store.budgets[0].LastResetAt == nil || !store.budgets[0].LastResetAt.Equal(store.resetAllAt) {
		t.Fatalf("budget reset time = %v, want %s", store.budgets[0].LastResetAt, store.resetAllAt)
	}
	var body resetBudgetsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode reset response: %v", err)
	}
	if body.Status != "ok" {
		t.Fatalf("reset status body = %q, want ok", body.Status)
	}
}
