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
	budgets []budget.Budget
	sum     float64

	resetUserPath      string
	resetPeriodSeconds int64
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
	return budget.DefaultSettings(), nil
}

func (s *adminBudgetStore) SaveSettings(_ context.Context, settings budget.Settings) (budget.Settings, error) {
	return settings, nil
}

func (s *adminBudgetStore) ResetBudget(_ context.Context, userPath string, periodSeconds int64, at time.Time) error {
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
		"/admin/api/v1/budgets",
		strings.NewReader(`{"user_path":"team/beta","period":"weekly","amount":12.5}`),
	)
	upsertReq.Header.Set("Content-Type", "application/json")
	upsertRec := httptest.NewRecorder()
	upsertCtx := e.NewContext(upsertReq, upsertRec)
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
		"/admin/api/v1/budgets",
		strings.NewReader(`{"user_path":"/team/beta","period_seconds":604800}`),
	)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

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
