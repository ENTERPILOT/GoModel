package config

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBudgetAlertsDefaults(t *testing.T) {
	clearAllConfigEnvVars(t)

	withTempDir(t, func(string) {
		result, err := Load()
		require.NoError(t, err)

		alerts := result.Config.Budgets.Alerts
		assert.Equal(t, []int{80, 100}, alerts.Thresholds)
		assert.Equal(t, 60, alerts.Interval)
		assert.Equal(t, 10, alerts.WebhookTimeout)
		assert.Equal(t, 3, alerts.WebhookMaxRetries)
		assert.Empty(t, alerts.Webhooks, "no destination by default, so nothing is evaluated")
	})
}

func TestBudgetAlertsFromYAML(t *testing.T) {
	clearAllConfigEnvVars(t)
	t.Setenv("ALERT_HOOK_SECRET", "s3cret")

	withTempDir(t, func(dir string) {
		result := loadConfigYAML(t, dir, `
budgets:
  alerts:
    thresholds: [100, 50, 90, 50]
    interval: 30
    webhook_timeout: 5
    webhook_max_retries: 0
    webhooks:
      - url: "https://alerts.example.com/hook"
        secret: "${env:ALERT_HOOK_SECRET}"
  user_paths:
    - path: "/team"
      limits:
        - period: "daily"
          amount: 10
          alert_thresholds: [90, 75]
`)
		require.NoError(t, result.ResolveSecrets(context.Background()))

		alerts := result.Config.Budgets.Alerts
		assert.Equal(t, []int{50, 90, 100}, alerts.Thresholds, "sorted, without duplicates")
		assert.Equal(t, 30, alerts.Interval)
		assert.Equal(t, 5, alerts.WebhookTimeout)
		assert.Equal(t, 0, alerts.WebhookMaxRetries)
		assert.Equal(t, []BudgetAlertWebhookConfig{{URL: "https://alerts.example.com/hook", Secret: "s3cret"}}, alerts.Webhooks)
		assert.Equal(t, []int{75, 90}, result.Config.Budgets.UserPaths[0].Limits[0].AlertThresholds)
	})
}

func TestBudgetAlertsFromEnv(t *testing.T) {
	clearAllConfigEnvVars(t)
	t.Setenv("BUDGETS_ALERTS_THRESHOLDS", "90, 50")
	t.Setenv("BUDGETS_ALERTS_INTERVAL", "120")
	t.Setenv("BUDGETS_ALERTS_WEBHOOK_URL", "https://env.example.com/hook")
	t.Setenv("BUDGETS_ALERTS_WEBHOOK_SECRET", "env-secret")
	t.Setenv("BUDGETS_ALERTS_WEBHOOK_TIMEOUT", "3")
	t.Setenv("BUDGETS_ALERTS_WEBHOOK_MAX_RETRIES", "1")
	t.Setenv("SET_BUDGET_TEAM", `[{"period":"daily","amount":5,"alert_thresholds":[60]}]`)

	withTempDir(t, func(dir string) {
		result := loadConfigYAML(t, dir, `
budgets:
  alerts:
    webhooks:
      - url: "https://yaml.example.com/hook"
`)
		alerts := result.Config.Budgets.Alerts
		assert.Equal(t, []int{50, 90}, alerts.Thresholds)
		assert.Equal(t, 120, alerts.Interval)
		assert.Equal(t, 3, alerts.WebhookTimeout)
		assert.Equal(t, 1, alerts.WebhookMaxRetries)
		assert.Equal(t, []BudgetAlertWebhookConfig{{URL: "https://env.example.com/hook", Secret: "env-secret"}}, alerts.Webhooks,
			"the env webhook replaces the YAML list")
		require.Len(t, result.Config.Budgets.UserPaths, 1)
		assert.Equal(t, []int{60}, result.Config.Budgets.UserPaths[0].Limits[0].AlertThresholds)
	})
}

func TestBudgetAlertsRejectInvalidConfig(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		env  map[string]string
		want string
	}{
		{name: "threshold above 100", yaml: "budgets:\n  alerts:\n    thresholds: [80, 120]\n", want: "between 1 and 100"},
		{name: "threshold zero", yaml: "budgets:\n  alerts:\n    thresholds: [0]\n", want: "between 1 and 100"},
		{name: "interval zero", yaml: "budgets:\n  alerts:\n    interval: 0\n", want: "budgets.alerts.interval"},
		{name: "timeout zero", yaml: "budgets:\n  alerts:\n    webhook_timeout: 0\n", want: "budgets.alerts.webhook_timeout"},
		{name: "negative retries", yaml: "budgets:\n  alerts:\n    webhook_max_retries: -1\n", want: "budgets.alerts.webhook_max_retries"},
		{name: "webhook without url", yaml: "budgets:\n  alerts:\n    webhooks:\n      - secret: x\n", want: "budgets.alerts.webhooks[0].url is required"},
		{
			name: "per-budget threshold out of range",
			yaml: "budgets:\n  user_paths:\n    - path: /team\n      limits:\n        - period: daily\n          amount: 1\n          alert_thresholds: [101]\n",
			want: "budgets.user_paths[0].limits[0].alert_thresholds",
		},
		{name: "env threshold not a number", env: map[string]string{"BUDGETS_ALERTS_THRESHOLDS": "80,high"}, want: "BUDGETS_ALERTS_THRESHOLDS"},
		{name: "env secret without url", env: map[string]string{"BUDGETS_ALERTS_WEBHOOK_SECRET": "x"}, want: "BUDGETS_ALERTS_WEBHOOK_URL"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearAllConfigEnvVars(t)
			for key, value := range tt.env {
				t.Setenv(key, value)
			}
			withTempDir(t, func(dir string) {
				writeConfigYAML(t, dir, tt.yaml)
				_, err := Load()
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.want)
			})
		})
	}
}

func TestBudgetAlertsEmptyThresholdsKeepDefaults(t *testing.T) {
	clearAllConfigEnvVars(t)

	withTempDir(t, func(dir string) {
		result := loadConfigYAML(t, dir, "budgets:\n  alerts:\n    thresholds: []\n")
		assert.Equal(t, DefaultBudgetAlertThresholds(), result.Config.Budgets.Alerts.Thresholds)
	})
}
