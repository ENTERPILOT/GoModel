package app

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/ext"
	"github.com/enterpilot/gomodel/internal/budget"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/storage"
	"github.com/enterpilot/gomodel/internal/storage/sqlx"
	"github.com/enterpilot/gomodel/internal/usage"
)

type appAlertSink struct {
	events chan ext.AlertEvent
}

func (s *appAlertSink) Name() string { return "test-sink" }

func (s *appAlertSink) SendAlert(_ context.Context, event ext.AlertEvent) error {
	s.events <- event
	return nil
}

// newBudgetAlertsApp builds a local app; setup can add environment and
// extensions before the configuration loads.
func newBudgetAlertsApp(t *testing.T, extensions *ext.Registry) (*App, error) {
	t.Helper()
	t.Chdir(t.TempDir())
	t.Setenv("STORAGE_TYPE", "sqlite")
	t.Setenv("SQLITE_PATH", filepath.Join(t.TempDir(), "alerts.db"))
	t.Setenv("ADMIN_UI_ENABLED", "false")
	t.Setenv("ADMIN_ENDPOINTS_ENABLED", "false")
	t.Setenv("MCP_ENABLED", "false")

	loaded, err := config.Load()
	require.NoError(t, err)
	application, err := New(context.Background(), Config{
		AppConfig:  loaded,
		Factory:    providers.NewProviderFactory(),
		Extensions: extensions,
	})
	if err != nil {
		return nil, err
	}
	t.Cleanup(func() { assert.NoError(t, application.Shutdown(context.Background())) })
	return application, nil
}

func TestBudgetAlertsAreBuiltOnlyWithADestination(t *testing.T) {
	var typedNil *appAlertSink
	tests := []struct {
		name       string
		webhookURL string
		sink       ext.AlertSink
		want       bool
	}{
		{name: "no destination"},
		{name: "webhook", webhookURL: "https://alerts.example.com/hook", want: true},
		{name: "registered sink", sink: &appAlertSink{events: make(chan ext.AlertEvent, 1)}, want: true},
		{name: "nil sink", sink: typedNil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.webhookURL != "" {
				t.Setenv("BUDGETS_ALERTS_WEBHOOK_URL", tt.webhookURL)
			}
			extensions := &ext.Registry{}
			if tt.sink != nil {
				extensions.RegisterAlertSink(tt.sink)
			}
			application, err := newBudgetAlertsApp(t, extensions)
			require.NoError(t, err)

			assert.Equal(t, tt.want, application.budgetAlerts != nil)
			assert.Equal(t, tt.want, application.budgets.Service.AlertSettings().Enabled)

			// start and Close are safe in any order and any number of times,
			// with or without alerts.
			application.budgetAlerts.start()
			application.budgetAlerts.start()
			require.NoError(t, application.budgetAlerts.Close())
			require.NoError(t, application.budgetAlerts.Close())
			application.budgetAlerts.start()
		})
	}
}

func TestBudgetAlertsNeedBudgets(t *testing.T) {
	t.Setenv("BUDGETS_ENABLED", "false")
	t.Setenv("BUDGETS_ALERTS_WEBHOOK_URL", "https://alerts.example.com/hook")

	application, err := newBudgetAlertsApp(t, nil)
	require.NoError(t, err)
	assert.Nil(t, application.budgetAlerts)
}

func TestBudgetAlertsRejectAnInvalidWebhookWithoutEchoingIt(t *testing.T) {
	t.Setenv("BUDGETS_ALERTS_WEBHOOK_URL", "alerts.example.com/t0ken")

	_, err := newBudgetAlertsApp(t, nil)
	require.ErrorContains(t, err, "budgets.alerts.webhooks[0].url")
	assert.NotContains(t, err.Error(), "t0ken")
}

// The whole path on a built app: a configured budget, recorded usage, one
// evaluation, and the registered sink receives the alert.
func TestBudgetAlertsReachARegisteredSink(t *testing.T) {
	t.Setenv("SET_BUDGET_TEAM", `[{"period":"monthly","amount":10,"alert_thresholds":[50]}]`)
	sink := &appAlertSink{events: make(chan ext.AlertEvent, 4)}
	extensions := &ext.Registry{}
	extensions.RegisterAlertSink(sink)

	application, err := newBudgetAlertsApp(t, extensions)
	require.NoError(t, err)
	require.NotNil(t, application.budgetAlerts)

	ctx := context.Background()
	usageStore, err := storage.ResolveSQLBackend(ctx, application.storage,
		func(db sqlx.DB) (*usage.SQLStore, error) { return usage.NewSQLStore(ctx, db, 0) },
		func(*mongo.Database) (*usage.SQLStore, error) { return nil, errors.New("test runs on SQLite") },
	)
	require.NoError(t, err)
	// SQLite compares usage timestamps by the second, so the entry is dated
	// just before now, without leaving the budget period.
	now := time.Now().UTC()
	at := now.Add(-2 * time.Second)
	if start, _ := budget.PeriodBounds(now, budget.PeriodMonthlySeconds, budget.DefaultSettings()); at.Before(start) {
		at = start
	}
	cost := 6.0
	require.NoError(t, usageStore.WriteBatch(ctx, []*usage.UsageEntry{{
		ID: "0b0e7f6e-6f0e-4d3a-9c59-4f1f6f0a0001", RequestID: "r1", ProviderID: "p1",
		Timestamp: at, Model: "gpt-4", Provider: "test", ProviderName: "test",
		Endpoint: "/v1/chat/completions", UserPath: "/team/app", TotalCost: &cost,
	}}))

	var event ext.AlertEvent
	require.Eventually(t, func() bool {
		if err := application.budgetAlerts.alerter.Evaluate(ctx); err != nil {
			return false
		}
		select {
		case event = <-sink.events:
			return true
		case <-time.After(50 * time.Millisecond):
			return false
		}
	}, 10*time.Second, 100*time.Millisecond, "the sink received no alert")
	assert.Equal(t, ext.AlertTypeBudgetThreshold, event.Type)
	assert.Equal(t, "/team", event.Budget.Subject)
	assert.Equal(t, "monthly", event.Budget.Period)
	assert.Equal(t, 50, event.ThresholdPercent)
	assert.Equal(t, 6.0, event.Spent)

	// A second evaluation fires nothing: the alert is claimed.
	require.NoError(t, application.budgetAlerts.alerter.Evaluate(ctx))
	require.NoError(t, application.budgetAlerts.Close())
	assert.Empty(t, sink.events)
}
