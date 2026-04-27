//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"

	"gomodel/internal/admin"
	"gomodel/internal/budget"
	"gomodel/internal/core"
	"gomodel/internal/usage"
)

const sqliteBudgetAmount = 0.01

type budgetE2EFixture struct {
	db            *sql.DB
	usageLogger   *usage.Logger
	budgetService *budget.Service
	closeOnce     sync.Once
	closeErr      error
}

type staticBudgetPricingResolver struct{}

func (staticBudgetPricingResolver) ResolvePricing(_, _ string) *core.ModelPricing {
	inputPerMtok := 1000.0
	outputPerMtok := 1000.0
	return &core.ModelPricing{
		Currency:      "USD",
		InputPerMtok:  &inputPerMtok,
		OutputPerMtok: &outputPerMtok,
	}
}

func TestBudgetEnforcementSQLite_E2E(t *testing.T) {
	mockServer.ResetRequests()
	fixture := setupBudgetE2EFixture(t, []budget.Budget{
		{UserPath: "/team/sqlite", PeriodSeconds: budget.PeriodDailySeconds, Amount: sqliteBudgetAmount},
	})

	ts := httptest.NewServer(setupE2EServer(t, e2eServerOptions{
		usageLogger:     fixture.usageLogger,
		budgetChecker:   fixture.budgetService,
		pricingResolver: staticBudgetPricingResolver{},
	}))
	defer ts.Close()

	firstResp := sendBudgetChatRequest(t, ts.URL, "budget sqlite first", "budget-sqlite-first", "/team/sqlite/app")
	require.Equal(t, http.StatusOK, firstResp.StatusCode)
	closeBody(firstResp)

	waitForBudgetSpent(t, fixture.budgetService, "/team/sqlite", sqliteBudgetAmount)

	secondResp := sendBudgetChatRequest(t, ts.URL, "budget sqlite second", "budget-sqlite-second", "/team/sqlite/app")
	defer closeBody(secondResp)
	require.Equal(t, http.StatusTooManyRequests, secondResp.StatusCode)
	require.NotEmpty(t, secondResp.Header.Get("Retry-After"))

	var envelope core.OpenAIErrorEnvelope
	require.NoError(t, json.NewDecoder(secondResp.Body).Decode(&envelope))
	require.Equal(t, core.ErrorTypeRateLimit, envelope.Error.Type)
	require.NotNil(t, envelope.Error.Code)
	require.Equal(t, "budget_exceeded", *envelope.Error.Code)
	require.Len(t, mockServer.Requests(), 1, "blocked request must not reach upstream provider")
}

func TestBudgetAdminEndpointsSQLite_E2E(t *testing.T) {
	fixture := setupBudgetE2EFixture(t, nil)
	ts := httptest.NewServer(setupE2EServer(t, e2eServerOptions{
		adminEndpointsEnabled: true,
		adminOptions:          []admin.Option{admin.WithBudgets(fixture.budgetService)},
		usageLogger:           fixture.usageLogger,
		budgetChecker:         fixture.budgetService,
		pricingResolver:       staticBudgetPricingResolver{},
	}))
	defer ts.Close()

	putResp := sendBudgetJSONRequest(t, http.MethodPut, ts.URL+"/admin/api/v1/budgets/%2Fteam%2Fadmin/daily", map[string]any{
		"amount": 12.5,
	})
	require.Equal(t, http.StatusOK, putResp.StatusCode)
	closeBody(putResp)

	listResp := sendBudgetJSONRequest(t, http.MethodGet, ts.URL+"/admin/api/v1/budgets", nil)
	require.Equal(t, http.StatusOK, listResp.StatusCode)
	var listBody struct {
		Budgets []struct {
			UserPath      string  `json:"user_path"`
			PeriodSeconds int64   `json:"period_seconds"`
			Amount        float64 `json:"amount"`
		} `json:"budgets"`
	}
	require.NoError(t, json.NewDecoder(listResp.Body).Decode(&listBody))
	closeBody(listResp)
	require.Len(t, listBody.Budgets, 1)
	require.Equal(t, "/team/admin", listBody.Budgets[0].UserPath)
	require.Equal(t, budget.PeriodDailySeconds, listBody.Budgets[0].PeriodSeconds)
	require.Equal(t, 12.5, listBody.Budgets[0].Amount)

	resetResp := sendBudgetJSONRequest(t, http.MethodPost, ts.URL+"/admin/api/v1/budgets/reset-one", map[string]any{
		"user_path":      "/team/admin",
		"period_seconds": budget.PeriodDailySeconds,
	})
	require.Equal(t, http.StatusOK, resetResp.StatusCode)
	closeBody(resetResp)

	statuses, err := fixture.budgetService.Statuses(context.Background(), time.Now().UTC())
	require.NoError(t, err)
	require.Len(t, statuses, 1)
	require.NotNil(t, statuses[0].Budget.LastResetAt)

	deleteResp := sendBudgetJSONRequest(t, http.MethodDelete, ts.URL+"/admin/api/v1/budgets/%2Fteam%2Fadmin/daily", nil)
	require.Equal(t, http.StatusOK, deleteResp.StatusCode)
	closeBody(deleteResp)

	statuses, err = fixture.budgetService.Statuses(context.Background(), time.Now().UTC())
	require.NoError(t, err)
	require.Empty(t, statuses)
}

func setupBudgetE2EFixture(t *testing.T, budgets []budget.Budget) *budgetE2EFixture {
	t.Helper()

	db, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	db.SetMaxOpenConns(1)

	usageStore, err := usage.NewSQLiteStore(db, 0)
	require.NoError(t, err)

	budgetStore, err := budget.NewSQLiteStore(db)
	require.NoError(t, err)

	service, err := budget.NewService(context.Background(), budgetStore)
	require.NoError(t, err)
	if len(budgets) > 0 {
		require.NoError(t, service.UpsertBudgets(context.Background(), budgets))
	}

	cfg := usage.DefaultConfig()
	cfg.Enabled = true
	cfg.BufferSize = 100
	cfg.FlushInterval = 10 * time.Millisecond

	fixture := &budgetE2EFixture{
		db:            db,
		usageLogger:   usage.NewLogger(usageStore, cfg),
		budgetService: service,
	}
	t.Cleanup(func() {
		fixture.close(t)
	})
	return fixture
}

func (f *budgetE2EFixture) close(t *testing.T) {
	t.Helper()

	f.closeOnce.Do(func() {
		f.closeErr = f.usageLogger.Close()
		if err := f.db.Close(); err != nil && f.closeErr == nil {
			f.closeErr = err
		}
	})
	require.NoError(t, f.closeErr)
}

func waitForBudgetSpent(t *testing.T, service *budget.Service, userPath string, amount float64) {
	t.Helper()

	require.Eventually(t, func() bool {
		statuses, err := service.Statuses(context.Background(), time.Now().UTC())
		if err != nil || len(statuses) != 1 {
			return false
		}
		return statuses[0].Budget.UserPath == userPath && statuses[0].HasUsage && statuses[0].Spent > amount
	}, 2*time.Second, 20*time.Millisecond)
}

func sendBudgetChatRequest(t *testing.T, serverURL, message, requestID, userPath string) *http.Response {
	t.Helper()

	return sendBudgetJSONRequestWithHeaders(t, http.MethodPost, serverURL+chatCompletionsPath, defaultChatReq(message), map[string]string{
		"X-Request-ID":        requestID,
		"X-GoModel-User-Path": userPath,
	})
}

func sendBudgetJSONRequest(t *testing.T, method, url string, payload any) *http.Response {
	t.Helper()
	return sendBudgetJSONRequestWithHeaders(t, method, url, payload, nil)
}

func sendBudgetJSONRequestWithHeaders(t *testing.T, method, url string, payload any, headers map[string]string) *http.Response {
	t.Helper()

	var body *bytes.Reader
	if payload == nil {
		body = bytes.NewReader(nil)
	} else {
		raw, err := json.Marshal(payload)
		require.NoError(t, err)
		body = bytes.NewReader(raw)
	}

	req, err := http.NewRequest(method, url, body)
	require.NoError(t, err)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	return resp
}
