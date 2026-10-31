package app

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/budget"
	"github.com/enterpilot/gomodel/internal/plugins"
	"github.com/enterpilot/gomodel/pluginapi"
)

func TestExportBudgets(t *testing.T) {
	declared := config.BudgetsConfig{
		Enabled:                 true,
		ProviderBilledCacheHits: true,
		UserPaths: []config.BudgetUserPathConfig{
			{Path: "/b", Limits: []config.BudgetLimitConfig{{Period: "daily", Amount: 1}}},
			{Path: "/a", Limits: []config.BudgetLimitConfig{{Period: "daily", Amount: 2}}},
		},
	}
	stored := []budget.Budget{
		{Scope: budget.ScopeLabel, Subject: "mobile", PeriodSeconds: 7200, Amount: 3, CostBasis: budget.CostBasisBilled, Source: budget.SourceManual},
		{Scope: budget.ScopeUserPath, Subject: "/a", PeriodSeconds: budget.PeriodDailySeconds, Amount: 20, CostBasis: budget.CostBasisProvider, Source: budget.SourceManual},
	}

	for _, billed := range []bool{false, true} {
		got, err := exportBudgets(declared, stored, billed)
		require.NoError(t, err)

		assert.True(t, got.ProviderBilledCacheHits)
		require.Len(t, got.UserPaths, 2)
		assert.Equal(t, "/b", got.UserPaths[0].Path, "declared budgets keep their positions")
		require.Len(t, got.UserPaths[1].Limits, 1)
		assert.Equal(t, 20.0, got.UserPaths[1].Limits[0].Amount, "the dashboard edit wins in place")
		require.Len(t, got.Labels, 1)
		require.Len(t, got.Labels[0].Limits, 1)
		want := "provider"
		if billed {
			want = "billed"
		}
		assert.Equal(t, want, got.Labels[0].Limits[0].CostBasis, "billed=%v", billed)
	}
}

func TestExportBudgetsWritesDisabledSectionAsDeclared(t *testing.T) {
	// The gateway does not validate a disabled section, so neither does the
	// export.
	declared := config.BudgetsConfig{UserPaths: []config.BudgetUserPathConfig{{Path: "/a", Limits: []config.BudgetLimitConfig{{Period: "fortnightly", Amount: 1}}}}}

	got, err := exportBudgets(declared, nil, false)
	require.NoError(t, err)
	assert.Equal(t, declared, got)

	rules, err := exportRateLimits(config.RateLimitsConfig{UserPaths: []config.RateLimitUserPathConfig{{Path: "/a", Limits: []config.RateLimitRuleConfig{{Period: "fortnightly"}}}}}, nil)
	require.NoError(t, err)
	assert.Len(t, rules.UserPaths, 1)
}

// secretRoutePlugin is a test plugin with a secret setting.
type secretRoutePlugin struct{}

func (secretRoutePlugin) Manifest() pluginapi.Manifest {
	return pluginapi.Manifest{
		Name:  "signed_route",
		Kinds: []pluginapi.Kind{pluginapi.KindResponse},
		ConfigSchema: []pluginapi.Field{
			{Key: "hmac_material", Input: pluginapi.InputSecret},
			{Key: "weight", Input: pluginapi.InputNumber},
		},
	}
}

func (secretRoutePlugin) Init(context.Context, json.RawMessage, pluginapi.Host) error { return nil }
func (secretRoutePlugin) Close(context.Context) error                                 { return nil }
func (secretRoutePlugin) OnResponse(context.Context, *pluginapi.Exchange) (pluginapi.Decision, error) {
	return pluginapi.Allow(), nil
}

func TestRouteSecretFields(t *testing.T) {
	catalog := plugins.NewCatalog()
	require.NoError(t, catalog.Register(func() pluginapi.Plugin { return secretRoutePlugin{} }, plugins.SourceRegistered))
	models := []config.VirtualModelConfig{
		{Source: "plain"},
		{Source: "signed", StrategyPlugin: "signed_route", StrategyConfig: map[string]any{"hmac_material": "x", "weight": 1}},
		{Source: "missing", StrategyPlugin: "not_loaded"},
	}

	assert.Equal(t, []string{"virtual_models[1].strategy_config.hmac_material"}, routeSecretFields(models, catalog))
}
