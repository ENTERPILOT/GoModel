package admin

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"sort"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/budget"
	"github.com/enterpilot/gomodel/internal/ratelimit"
	"github.com/enterpilot/gomodel/internal/virtualmodels"
)

// The export* functions below add the rows managed in the dashboard to the
// declared configuration they are given, returning new values: the loaded
// configuration is shared and never modified.

// exportProviders adds the provider credentials stored from the dashboard.
// A disabled credential has no config.yaml form, so it is left out and named.
func (h *Handler) exportProviders(ctx context.Context, declared map[string]config.RawProviderConfig) (map[string]config.RawProviderConfig, []string, error) {
	providers := maps.Clone(declared)
	if h.providerCredentials == nil {
		return providers, nil, nil
	}
	rows, err := h.providerCredentials.List(ctx)
	if err != nil {
		return nil, nil, providerCredentialWriteError(err)
	}
	if providers == nil {
		providers = make(map[string]config.RawProviderConfig, len(rows))
	}
	var disabled []string
	for _, row := range rows {
		if h.providerCredentials.IsManaged(row.Name) {
			continue
		}
		if !row.Enabled {
			disabled = append(disabled, row.Name)
			continue
		}
		providers[row.Name] = row.ExportConfig()
	}
	slices.Sort(disabled)
	return providers, disabled, nil
}

func (h *Handler) exportVirtualModels(declared []config.VirtualModelConfig) []config.VirtualModelConfig {
	if h.virtualModels == nil {
		return declared
	}
	result := slices.Clone(declared)
	for _, model := range h.virtualModels.List() {
		if model.Managed {
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

func (h *Handler) exportUsers(declared []config.UserConfig) []config.UserConfig {
	if h.users == nil {
		return declared
	}
	result := slices.Clone(declared)
	for _, user := range h.users.List() {
		if user.Managed {
			continue
		}
		result = append(result, config.UserConfig{Path: user.UserPath, AllowedModels: user.AllowedModels, Description: user.Description})
	}
	return result
}

// exportMCP adds the MCP servers and virtual servers stored from the
// dashboard. A header is written as stored: the secret reference it holds,
// or the value, for the export to redact.
func (h *Handler) exportMCP(declared config.MCPConfig) config.MCPConfig {
	if h.mcpServers == nil {
		return declared
	}
	result := declared
	result.Servers = maps.Clone(declared.Servers)
	for _, view := range h.mcpServers.Views() {
		spec := view.Spec
		if spec.Managed {
			continue
		}
		if result.Servers == nil {
			result.Servers = make(map[string]config.MCPServerConfig)
		}
		headers := make(map[string]string, len(spec.Headers))
		for name, value := range spec.Headers {
			if stored, ok := spec.HeaderReferences[name]; ok {
				value = stored
			}
			headers[name] = value
		}
		enabled := spec.Enabled
		result.Servers[spec.Name] = config.MCPServerConfig{
			URL:                 spec.URL,
			Transport:           spec.Transport,
			Headers:             headers,
			Description:         spec.Description,
			Enabled:             &enabled,
			AllowedTools:        spec.AllowedTools,
			DisallowedTools:     spec.DisallowedTools,
			UserPaths:           spec.UserPaths,
			DisallowedUserPaths: spec.DisallowedUserPaths,
			ToolTimeout:         spec.ToolTimeout,
		}
	}
	result.VirtualServers = maps.Clone(declared.VirtualServers)
	for _, view := range h.mcpServers.VirtualViews() {
		spec := view.Spec
		if spec.Managed {
			continue
		}
		if result.VirtualServers == nil {
			result.VirtualServers = make(map[string]config.MCPVirtualServerConfig)
		}
		result.VirtualServers[spec.Name] = config.MCPVirtualServerConfig{
			Description:   spec.Description,
			Servers:       spec.Servers,
			ToolDiscovery: spec.ToolDiscovery,
		}
	}
	return result
}

func (h *Handler) exportTaggingHeaders(declared []config.TaggingHeaderConfig) []config.TaggingHeaderConfig {
	if h.tagging == nil {
		return declared
	}
	result := slices.Clone(declared)
	for _, rule := range h.tagging.Rules() {
		if rule.Managed {
			continue
		}
		result = append(result, config.TaggingHeaderConfig{Header: rule.Header, Prefix: rule.Prefix, DoNotPass: rule.DoNotPass, Delimiter: rule.Delimiter})
	}
	return result
}

// exportBudgets lists the stored budgets, which hold the declared ones too,
// as edited in the dashboard since.
func (h *Handler) exportBudgets(declared config.BudgetsConfig) config.BudgetsConfig {
	if h.budgets == nil {
		return declared
	}
	rows := h.budgets.Budgets()
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Scope != rows[j].Scope {
			return rows[i].Scope < rows[j].Scope
		}
		if rows[i].Subject != rows[j].Subject {
			return rows[i].Subject < rows[j].Subject
		}
		return rows[i].PeriodSeconds < rows[j].PeriodSeconds
	})
	result := config.BudgetsConfig{Enabled: declared.Enabled}
	for _, row := range rows {
		limit := config.BudgetLimitConfig{PeriodSeconds: row.PeriodSeconds, Amount: row.Amount}
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
	return result
}

// exportRateLimits lists the stored rate limit rules, which hold the declared
// ones too, as edited in the dashboard since.
func (h *Handler) exportRateLimits(declared config.RateLimitsConfig) config.RateLimitsConfig {
	if h.rateLimits == nil {
		return declared
	}
	rows := h.rateLimits.Rules()
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Scope != rows[j].Scope {
			return rows[i].Scope < rows[j].Scope
		}
		if rows[i].Subject != rows[j].Subject {
			return rows[i].Subject < rows[j].Subject
		}
		return rows[i].PeriodSeconds < rows[j].PeriodSeconds
	})
	result := config.RateLimitsConfig{Enabled: declared.Enabled, FlushInterval: declared.FlushInterval}
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
	return result
}

// exportGuardrails returns the paths of the secret settings of the declared
// guardrail rules, and the names of the guardrails created in the dashboard.
// Those are left out: config.yaml adds every rule to the default workflow,
// while a dashboard guardrail runs only in the workflows that name it.
func (h *Handler) exportGuardrails(declared []config.GuardrailRuleConfig) ([]string, []string) {
	if h.guardrailDefs == nil {
		return nil, nil
	}
	var secretFields []string
	declaredNames := make(map[string]bool, len(declared))
	for i, rule := range declared {
		declaredNames[rule.Name] = true
		for key := range h.guardrailDefs.SecretConfigKeys(rule.Type) {
			secretFields = append(secretFields, fmt.Sprintf("guardrails.rules[%d].config.%s", i, key))
		}
	}
	var created []string
	for _, def := range h.guardrailDefs.List() {
		if !declaredNames[def.Name] {
			created = append(created, def.Name)
		}
	}
	slices.Sort(created)
	return secretFields, created
}

func isBudgetPeriodName(label string) bool {
	_, ok := budget.PeriodSeconds(label)
	return ok
}

func isRateLimitPeriodName(label string) bool {
	_, ok := ratelimit.PeriodSecondsFromName(label)
	return ok
}
