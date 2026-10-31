package app

import (
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/budget"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/guardrails"
	"github.com/enterpilot/gomodel/internal/mcpgateway"
	"github.com/enterpilot/gomodel/internal/plugins"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/ratelimit"
	"github.com/enterpilot/gomodel/internal/tagging"
	"github.com/enterpilot/gomodel/internal/users"
	"github.com/enterpilot/gomodel/internal/virtualmodels"
)

// storedObjects are the objects saved in the dashboard that a configuration
// export adds to the loaded configuration.
type storedObjects struct {
	credentials   []providers.ManagedProviderCredential
	virtualModels []virtualmodels.VirtualModel
	users         []users.User
	mcpServers    []mcpgateway.ManagedServer
	mcpVirtual    []mcpgateway.ManagedVirtualServer
	tagging       []tagging.Rule
	budgets       []budget.Budget
	rateLimits    []ratelimit.Rule
}

// The export* functions below add stored objects to the declared
// configuration, as the gateway merges them: a declared object shadows a
// stored one of the same name. They return new values and leave the loaded
// configuration unmodified.

// exportProviders adds the stored provider credentials to the declared
// providers. A disabled credential has no config.yaml form and is left out.
func exportProviders(declared providers.ExportedProviders, stored []providers.ManagedProviderCredential) (map[string]config.RawProviderConfig, []string) {
	result := maps.Clone(declared.Providers)
	if result == nil {
		result = make(map[string]config.RawProviderConfig, len(stored))
	}
	references := slices.Clone(declared.References)
	for _, row := range stored {
		if !row.Enabled || slices.Contains(declared.Declared, row.Name) {
			continue
		}
		raw, rowReferences := row.ExportConfig()
		result[row.Name] = raw
		references = append(references, rowReferences...)
	}
	return result, references
}

func exportVirtualModels(declared []config.VirtualModelConfig, stored []virtualmodels.VirtualModel) []config.VirtualModelConfig {
	result := slices.Clone(declared)
	for _, model := range stored {
		if slices.ContainsFunc(declared, func(d config.VirtualModelConfig) bool {
			return strings.TrimSpace(d.Source) == strings.TrimSpace(model.Source)
		}) {
			continue
		}
		targets := model.Targets
		if len(targets) == 0 && model.Model != "" {
			targets = []virtualmodels.Target{{Provider: model.ProviderName, Model: model.Model}}
		}
		entry := config.VirtualModelConfig{
			Source:          model.Source,
			Strategy:        model.Strategy,
			StrategyPlugin:  model.StrategyPlugin,
			StrategyConfig:  model.StrategyConfig,
			SessionAffinity: model.SessionAffinity,
			Failover:        model.Failover,
			UserPaths:       model.UserPaths,
			Description:     model.Description,
			Slowdown:        model.Slowdown,
			ReasoningEffort: model.ReasoningEffort,
		}
		for _, target := range targets {
			entry.Targets = append(entry.Targets, config.VirtualModelTargetConfig{Provider: target.Provider, Model: target.Model, Weight: target.Weight})
		}
		if !model.Enabled {
			entry.Enabled = new(bool)
		}
		result = append(result, entry)
	}
	return result
}

func exportUsers(declared []config.UserConfig, stored []users.User) []config.UserConfig {
	declaredPaths := make(map[string]bool, len(declared))
	for _, user := range declared {
		path, err := core.NormalizeUserPath(user.Path)
		if err != nil {
			path = user.Path
		}
		declaredPaths[path] = true
	}
	result := slices.Clone(declared)
	for _, user := range stored {
		if declaredPaths[user.UserPath] {
			continue
		}
		result = append(result, config.UserConfig{Path: user.UserPath, AllowedModels: user.AllowedModels, Description: user.Description})
	}
	return result
}

// exportMCP adds the stored MCP servers and virtual servers. Headers are
// written as stored, secret references unresolved, and returned as
// references.
func exportMCP(declared config.MCPConfig, servers []mcpgateway.ManagedServer, virtual []mcpgateway.ManagedVirtualServer) (config.MCPConfig, []string) {
	result := declared
	result.Servers = maps.Clone(declared.Servers)
	var references []string
	for _, row := range servers {
		if _, ok := declared.Servers[row.Name]; ok {
			continue
		}
		if result.Servers == nil {
			result.Servers = make(map[string]config.MCPServerConfig)
		}
		spec := row.Spec()
		enabled := spec.Enabled
		result.Servers[row.Name] = config.MCPServerConfig{
			URL:                 spec.URL,
			Transport:           spec.Transport,
			Headers:             maps.Clone(row.Headers),
			Description:         spec.Description,
			Enabled:             &enabled,
			AllowedTools:        spec.AllowedTools,
			DisallowedTools:     spec.DisallowedTools,
			UserPaths:           spec.UserPaths,
			DisallowedUserPaths: spec.DisallowedUserPaths,
			ToolTimeout:         spec.ToolTimeout,
		}
		for name := range row.Headers {
			references = append(references, "mcp.servers."+row.Name+".headers."+name)
		}
	}
	result.VirtualServers = maps.Clone(declared.VirtualServers)
	for _, row := range virtual {
		if _, ok := declared.VirtualServers[row.Name]; ok {
			continue
		}
		if result.VirtualServers == nil {
			result.VirtualServers = make(map[string]config.MCPVirtualServerConfig)
		}
		result.VirtualServers[row.Name] = config.MCPVirtualServerConfig{
			Description:   row.Description,
			Servers:       row.Servers,
			ToolDiscovery: row.ToolDiscovery,
		}
	}
	slices.Sort(references)
	return result, references
}

func exportTaggingHeaders(declared []config.TaggingHeaderConfig, stored []tagging.Rule) []config.TaggingHeaderConfig {
	result := slices.Clone(declared)
	for _, rule := range stored {
		if slices.ContainsFunc(declared, func(d config.TaggingHeaderConfig) bool { return strings.EqualFold(d.Header, rule.Header) }) {
			continue
		}
		result = append(result, config.TaggingHeaderConfig{Header: rule.Header, Prefix: rule.Prefix, DoNotPass: rule.DoNotPass, Delimiter: rule.Delimiter})
	}
	return result
}

// exportBudgets lists the declared budgets and the stored ones saved in the
// dashboard. A budget saved in the dashboard wins over a declared one of the
// same scope, subject, and period, as it does when the gateway starts.
func exportBudgets(declared config.BudgetsConfig, stored []budget.Budget) (config.BudgetsConfig, error) {
	rows, err := budget.ConfigBudgets(declared)
	if err != nil {
		return config.BudgetsConfig{}, err
	}
	type budgetKey struct {
		scope   budget.Scope
		subject string
		period  int64
	}
	saved := make(map[budgetKey]bool)
	for _, row := range stored {
		if row.Source != budget.SourceConfig {
			saved[budgetKey{row.Scope, row.Subject, row.PeriodSeconds}] = true
		}
	}
	rows = slices.DeleteFunc(rows, func(row budget.Budget) bool {
		return saved[budgetKey{row.Scope, row.Subject, row.PeriodSeconds}]
	})
	for _, row := range stored {
		if row.Source != budget.SourceConfig {
			rows = append(rows, row)
		}
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Scope != rows[j].Scope {
			return rows[i].Scope < rows[j].Scope
		}
		if rows[i].Subject != rows[j].Subject {
			return rows[i].Subject < rows[j].Subject
		}
		return rows[i].PeriodSeconds < rows[j].PeriodSeconds
	})
	result := declared
	result.UserPaths, result.Labels, result.Providers = nil, nil, nil
	for _, row := range rows {
		limit := config.BudgetLimitConfig{PeriodSeconds: row.PeriodSeconds, Amount: row.Amount, CostBasis: string(row.CostBasis)}
		if label := budget.PeriodLabel(row.PeriodSeconds); isBudgetPeriodName(label) {
			limit.Period = label
		}
		switch row.Scope {
		case budget.ScopeUserPath:
			i := slices.IndexFunc(result.UserPaths, func(e config.BudgetUserPathConfig) bool {
				return e.Path == row.Subject && e.PerChild == row.PerChild
			})
			if i < 0 {
				result.UserPaths = append(result.UserPaths, config.BudgetUserPathConfig{Path: row.Subject, PerChild: row.PerChild})
				i = len(result.UserPaths) - 1
			}
			result.UserPaths[i].Limits = append(result.UserPaths[i].Limits, limit)
		case budget.ScopeLabel:
			i := slices.IndexFunc(result.Labels, func(e config.BudgetLabelConfig) bool { return e.Label == row.Subject })
			if i < 0 {
				result.Labels = append(result.Labels, config.BudgetLabelConfig{Label: row.Subject})
				i = len(result.Labels) - 1
			}
			result.Labels[i].Limits = append(result.Labels[i].Limits, limit)
		case budget.ScopeProvider:
			i := slices.IndexFunc(result.Providers, func(e config.BudgetProviderConfig) bool { return e.Name == row.Subject })
			if i < 0 {
				result.Providers = append(result.Providers, config.BudgetProviderConfig{Name: row.Subject})
				i = len(result.Providers) - 1
			}
			result.Providers[i].Limits = append(result.Providers[i].Limits, limit)
		}
	}
	return result, nil
}

// exportRateLimits lists the declared rate limit rules and the stored ones
// saved in the dashboard. A rule saved in the dashboard wins over a declared
// one of the same scope, subject, and period, as it does when the gateway
// starts.
func exportRateLimits(declared config.RateLimitsConfig, stored []ratelimit.Rule) (config.RateLimitsConfig, error) {
	rows, err := ratelimit.ConfigRules(declared)
	if err != nil {
		return config.RateLimitsConfig{}, err
	}
	type ruleKey struct {
		scope   ratelimit.RuleScope
		subject string
		period  int64
	}
	saved := make(map[ruleKey]bool)
	for _, row := range stored {
		if row.Source != ratelimit.SourceConfig {
			saved[ruleKey{row.Scope, row.Subject, row.PeriodSeconds}] = true
		}
	}
	rows = slices.DeleteFunc(rows, func(row ratelimit.Rule) bool {
		return saved[ruleKey{row.Scope, row.Subject, row.PeriodSeconds}]
	})
	for _, row := range stored {
		if row.Source != ratelimit.SourceConfig {
			rows = append(rows, row)
		}
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Scope != rows[j].Scope {
			return rows[i].Scope < rows[j].Scope
		}
		if rows[i].Subject != rows[j].Subject {
			return rows[i].Subject < rows[j].Subject
		}
		return rows[i].PeriodSeconds < rows[j].PeriodSeconds
	})
	result := declared
	result.UserPaths, result.Providers, result.Models = nil, nil, nil
	for _, row := range rows {
		limit := config.RateLimitRuleConfig{MaxRequests: row.MaxRequests, MaxTokens: row.MaxTokens}
		if label := ratelimit.PeriodLabel(row.PeriodSeconds); isRateLimitPeriodName(label) {
			limit.Period = label
		} else {
			seconds := row.PeriodSeconds
			limit.PeriodSeconds = &seconds
		}
		subject := row.Subject
		if row.SubjectDisplay != "" {
			subject = row.SubjectDisplay
		}
		switch row.Scope {
		case ratelimit.ScopeUserPath:
			i := slices.IndexFunc(result.UserPaths, func(e config.RateLimitUserPathConfig) bool {
				return e.Path == subject && e.PerChild == row.PerChild
			})
			if i < 0 {
				result.UserPaths = append(result.UserPaths, config.RateLimitUserPathConfig{Path: subject, PerChild: row.PerChild})
				i = len(result.UserPaths) - 1
			}
			result.UserPaths[i].Limits = append(result.UserPaths[i].Limits, limit)
		case ratelimit.ScopeProvider:
			i := slices.IndexFunc(result.Providers, func(e config.RateLimitProviderConfig) bool { return e.Name == subject })
			if i < 0 {
				result.Providers = append(result.Providers, config.RateLimitProviderConfig{Name: subject})
				i = len(result.Providers) - 1
			}
			result.Providers[i].Limits = append(result.Providers[i].Limits, limit)
		case ratelimit.ScopeModel:
			i := slices.IndexFunc(result.Models, func(e config.RateLimitModelConfig) bool { return e.Model == subject })
			if i < 0 {
				result.Models = append(result.Models, config.RateLimitModelConfig{Model: subject})
				i = len(result.Models) - 1
			}
			result.Models[i].Limits = append(result.Models[i].Limits, limit)
		}
	}
	return result, nil
}

// guardrailSecretFields returns the paths of the secret settings of the
// declared guardrail rules, by their plugin's schema.
func guardrailSecretFields(declared []config.GuardrailRuleConfig, catalog *plugins.Catalog) []string {
	var fields []string
	for i, rule := range declared {
		for key := range guardrails.SecretConfigKeys(catalog, rule.Type) {
			fields = append(fields, fmt.Sprintf("guardrails.rules[%d].config.%s", i, key))
		}
	}
	slices.Sort(fields)
	return fields
}

func isBudgetPeriodName(label string) bool {
	_, ok := budget.PeriodSeconds(label)
	return ok
}

func isRateLimitPeriodName(label string) bool {
	_, ok := ratelimit.PeriodSecondsFromName(label)
	return ok
}
