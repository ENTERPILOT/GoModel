package config

import (
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
)

// BudgetAlertsConfig holds soft budget alerts: thresholds evaluated in the
// background and delivered to a generic JSON webhook, and to any alert sink a
// distribution registers. Nothing is evaluated until a destination exists.
type BudgetAlertsConfig struct {
	// Thresholds lists the default alert thresholds as percentages of a
	// budget amount, each 1-100. A budget can override them. Default: 80, 100.
	// Env: BUDGETS_ALERTS_THRESHOLDS, a comma-separated list.
	Thresholds []int `yaml:"thresholds"`

	// Interval is how often budgets are evaluated against their thresholds,
	// in seconds. Default: 60.
	Interval int `yaml:"interval" env:"BUDGETS_ALERTS_INTERVAL"`

	// Webhooks lists the endpoints every alert is POSTed to as JSON.
	// BUDGETS_ALERTS_WEBHOOK_URL (with the optional
	// BUDGETS_ALERTS_WEBHOOK_SECRET) declares one and replaces this list.
	Webhooks []BudgetAlertWebhookConfig `yaml:"webhooks"`

	// WebhookTimeout bounds one delivery attempt, in seconds. Default: 10.
	WebhookTimeout int `yaml:"webhook_timeout" env:"BUDGETS_ALERTS_WEBHOOK_TIMEOUT"`

	// WebhookMaxRetries is how often a failed delivery is retried, with
	// exponential backoff. Default: 3. 0 disables retries.
	WebhookMaxRetries int `yaml:"webhook_max_retries" env:"BUDGETS_ALERTS_WEBHOOK_MAX_RETRIES"`
}

// BudgetAlertWebhookConfig declares one alert webhook. Both fields accept
// secret references such as ${env:NAME} or ${file:/path}.
type BudgetAlertWebhookConfig struct {
	// URL is the http(s) endpoint. It is treated as a secret, since webhook
	// URLs commonly carry a token in their path.
	URL string `yaml:"url" export:"secret"`

	// Secret, when set, signs every delivery: the X-GoModel-Signature header
	// carries "sha256=" and the hex HMAC-SHA256 of the request body.
	Secret string `yaml:"secret" export:"secret"`
}

// DefaultBudgetAlertThresholds are the thresholds used when none are
// configured: a warning at 80% and a notice when the budget is spent.
func DefaultBudgetAlertThresholds() []int {
	return []int{80, 100}
}

func defaultBudgetAlertsConfig() BudgetAlertsConfig {
	return BudgetAlertsConfig{
		Thresholds:        DefaultBudgetAlertThresholds(),
		Interval:          60,
		WebhookTimeout:    10,
		WebhookMaxRetries: 3,
	}
}

// NormalizeBudgetAlertThresholds validates alert thresholds and returns them
// sorted without duplicates. Each must be a percentage from 1 to 100.
func NormalizeBudgetAlertThresholds(thresholds []int) ([]int, error) {
	if len(thresholds) == 0 {
		return nil, nil
	}
	normalized := slices.Clone(thresholds)
	slices.Sort(normalized)
	normalized = slices.Compact(normalized)
	if normalized[0] < 1 || normalized[len(normalized)-1] > 100 {
		return nil, fmt.Errorf("alert thresholds must be percentages between 1 and 100")
	}
	return normalized, nil
}

func applyBudgetAlertsEnv(cfg *BudgetsConfig) error {
	if raw := strings.TrimSpace(os.Getenv("BUDGETS_ALERTS_THRESHOLDS")); raw != "" {
		var thresholds []int
		for item := range strings.SplitSeq(raw, ",") {
			item = strings.TrimSpace(item)
			if item == "" {
				continue
			}
			value, err := strconv.Atoi(item)
			if err != nil {
				return fmt.Errorf("invalid BUDGETS_ALERTS_THRESHOLDS: %q is not a whole number", item)
			}
			thresholds = append(thresholds, value)
		}
		cfg.Alerts.Thresholds = thresholds
	}
	url := strings.TrimSpace(os.Getenv("BUDGETS_ALERTS_WEBHOOK_URL"))
	secret := os.Getenv("BUDGETS_ALERTS_WEBHOOK_SECRET")
	if url != "" {
		cfg.Alerts.Webhooks = []BudgetAlertWebhookConfig{{URL: url, Secret: secret}}
	} else if strings.TrimSpace(secret) != "" {
		return fmt.Errorf("BUDGETS_ALERTS_WEBHOOK_SECRET is set without BUDGETS_ALERTS_WEBHOOK_URL")
	}
	return nil
}

func validateBudgetAlertsConfig(cfg *BudgetAlertsConfig) error {
	thresholds, err := NormalizeBudgetAlertThresholds(cfg.Thresholds)
	if err != nil {
		return fmt.Errorf("budgets.alerts.thresholds: %w", err)
	}
	if len(thresholds) == 0 {
		thresholds = DefaultBudgetAlertThresholds()
	}
	cfg.Thresholds = thresholds
	if cfg.Interval < 1 {
		return fmt.Errorf("budgets.alerts.interval must be at least 1 second")
	}
	if cfg.WebhookTimeout < 1 {
		return fmt.Errorf("budgets.alerts.webhook_timeout must be at least 1 second")
	}
	if cfg.WebhookMaxRetries < 0 || cfg.WebhookMaxRetries > 10 {
		return fmt.Errorf("budgets.alerts.webhook_max_retries must be between 0 and 10")
	}
	for i := range cfg.Webhooks {
		cfg.Webhooks[i].URL = strings.TrimSpace(cfg.Webhooks[i].URL)
		if cfg.Webhooks[i].URL == "" {
			return fmt.Errorf("budgets.alerts.webhooks[%d].url is required", i)
		}
	}
	return nil
}
