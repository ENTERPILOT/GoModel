package tagging

import "context"

// Store persists the operator-managed tagging rules (the dashboard-editable
// set). Declarative config/env rules are never stored.
type Store interface {
	// GetRules returns the persisted operator rules, empty when none were saved.
	GetRules(ctx context.Context) ([]Rule, error)

	// SaveRules replaces the persisted operator rule set.
	SaveRules(ctx context.Context, rules []Rule) error

	// Close releases store resources.
	Close() error
}

// rulesSettingKey is the single settings key the rule set is stored under.
const rulesSettingKey = "headers"
