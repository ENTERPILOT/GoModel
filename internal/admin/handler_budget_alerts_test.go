package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/ext"
	"github.com/enterpilot/gomodel/internal/budget"
	"github.com/enterpilot/gomodel/internal/echotest"
)

// alertingBudgetStore is adminBudgetStore with the alert storage an Alerter
// needs.
type alertingBudgetStore struct {
	adminBudgetStore
}

func (s *alertingBudgetStore) ClaimAlert(context.Context, budget.AlertClaim) (bool, error) {
	return true, nil
}

func (s *alertingBudgetStore) PruneAlerts(context.Context, time.Time) error { return nil }

func (s *alertingBudgetStore) SumSpendByUserPath(context.Context, budget.SpendWindow, int) ([]budget.PathSpend, error) {
	return nil, nil
}

func TestBudgetEndpointsAlertThresholds(t *testing.T) {
	store := &adminBudgetStore{}
	service, err := budget.NewService(context.Background(), store, budget.WithAlertThresholds([]int{70, 95}))
	require.NoError(t, err)
	h := NewHandler(nil, nil, WithBudgets(service))

	c, rec := echotest.Request(t, http.MethodPut, "/admin/budgets",
		`{"user_path":"/team","budget_key":{"period":"daily"},"amount":10,"alert_thresholds":[90,50,90]}`)
	require.NoError(t, h.UpsertBudget(c))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body := echotest.Decode[budgetListResponse](t, rec)
	require.Len(t, body.Budgets, 1)
	assert.Equal(t, []int{50, 90}, body.Budgets[0].AlertThresholds, "sorted, without duplicates")
	assert.Equal(t, budgetAlertsResponse{Enabled: false, DefaultThresholds: []int{70, 95}}, body.Alerts,
		"no destination, so alerts are reported off")

	// A save without thresholds returns the budget to the defaults.
	c, rec = echotest.Request(t, http.MethodPut, "/admin/budgets", `{"user_path":"/team","budget_key":{"period":"daily"},"amount":12}`)
	require.NoError(t, h.UpsertBudget(c))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body = echotest.Decode[budgetListResponse](t, rec)
	require.Len(t, body.Budgets, 1)
	assert.Empty(t, body.Budgets[0].AlertThresholds)
	assert.NotContains(t, rec.Body.String(), `"alert_thresholds"`)

	for _, thresholds := range []string{`[0]`, `[101]`, `[50,-1]`} {
		c, rec = echotest.Request(t, http.MethodPut, "/admin/budgets",
			`{"user_path":"/team","budget_key":{"period":"daily"},"amount":10,"alert_thresholds":`+thresholds+`}`)
		require.NoError(t, h.UpsertBudget(c))
		require.Equal(t, http.StatusBadRequest, rec.Code, thresholds)
		assert.Contains(t, rec.Body.String(), "between 1 and 100")
	}
}

func TestBudgetEndpointsReportAlertsEnabledWithADestination(t *testing.T) {
	store := &alertingBudgetStore{}
	service, err := budget.NewService(context.Background(), store)
	require.NoError(t, err)
	alerter, err := budget.NewAlerter(service, time.Minute, func(ext.AlertEvent) {})
	require.NoError(t, err)
	require.NotNil(t, alerter)
	h := NewHandler(nil, nil, WithBudgets(service))

	c, rec := echotest.Get(t, "/admin/budgets")
	require.NoError(t, h.ListBudgets(c))
	body := echotest.Decode[budgetListResponse](t, rec)
	assert.Equal(t, budgetAlertsResponse{Enabled: true, DefaultThresholds: []int{80, 100}}, body.Alerts)
}

func TestBudgetStatusResponsesProjection(t *testing.T) {
	start := time.Date(2026, time.April, 25, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 0, 1)
	daily := budget.Budget{Scope: budget.ScopeUserPath, Subject: "/team", PeriodSeconds: budget.PeriodDailySeconds, Amount: 100}
	template := daily
	template.PerChild = true
	exhaustion := start.Add(15 * time.Hour)

	tests := []struct {
		name           string
		status         budget.CheckResult
		now            time.Time
		wantSpend      *float64
		wantExhaustion *time.Time
	}{
		{
			name:   "too early in the period",
			status: budget.CheckResult{Budget: daily, PeriodStart: start, PeriodEnd: end, Spent: 5, HasUsage: true},
			now:    start.Add(time.Hour), // 4.2% elapsed, below the 5% floor
		},
		{
			name:      "within the budget",
			status:    budget.CheckResult{Budget: daily, PeriodStart: start, PeriodEnd: end, Spent: 30, HasUsage: true},
			now:       start.Add(12 * time.Hour),
			wantSpend: new(60.0),
		},
		{
			name:           "running out",
			status:         budget.CheckResult{Budget: daily, PeriodStart: start, PeriodEnd: end, Spent: 80, HasUsage: true},
			now:            start.Add(12 * time.Hour),
			wantSpend:      new(160.0),
			wantExhaustion: &exhaustion,
		},
		{
			name:   "per-child template",
			status: budget.CheckResult{Budget: template, PeriodStart: start, PeriodEnd: end},
			now:    start.Add(12 * time.Hour),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			responses := budgetStatusResponses([]budget.CheckResult{tt.status}, tt.now)
			require.Len(t, responses, 1)
			assert.Equal(t, tt.wantSpend, responses[0].ProjectedSpend)
			assert.Equal(t, tt.wantExhaustion, responses[0].ProjectedExhaustionAt)

			raw, err := json.Marshal(responses[0])
			require.NoError(t, err)
			var fields map[string]any
			require.NoError(t, json.Unmarshal(raw, &fields))
			assert.Equal(t, tt.wantSpend != nil, fields["projected_spend"] != nil)
			assert.Equal(t, tt.wantExhaustion != nil, fields["projected_exhaustion_at"] != nil)
		})
	}
}
