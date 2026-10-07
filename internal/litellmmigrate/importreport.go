package litellmmigrate

import (
	"fmt"
	"slices"
	"strings"
)

func (plan *ImportPlan) writeMarkdown(b *strings.Builder) {
	fmt.Fprintf(b, "\n## LiteLLM database\n\n%d keys to import, %d blocked or expired, %d model policies, %d budgets, %d rate limits.\n",
		len(plan.Keys)-plan.DisabledKeys, plan.DisabledKeys, len(plan.Policies), len(plan.Budgets), len(plan.RateLimits))
	type row struct{ models, budgets, limits []string }
	rows := map[string]*row{}
	at := func(path string) *row {
		if rows[path] == nil {
			rows[path] = &row{}
		}
		return rows[path]
	}
	for _, policy := range plan.Policies {
		at(policy.UserPath).models = policy.AllowedModels
	}
	for _, budget := range plan.Budgets {
		period := budget.Period
		if period == "" {
			period = "every " + formatSeconds(budget.PeriodSeconds)
		}
		at(budget.UserPath).budgets = append(at(budget.UserPath).budgets, fmt.Sprintf("$%g %s (LiteLLM spent $%.2f)", budget.Amount, period, budget.Spend))
	}
	for _, limit := range plan.RateLimits {
		r := at(limit.UserPath)
		switch {
		case limit.Period == "concurrent" && limit.MaxRequests != nil:
			r.limits = append(r.limits, fmt.Sprintf("%d concurrent requests", *limit.MaxRequests))
		case limit.MaxRequests != nil:
			r.limits = append(r.limits, fmt.Sprintf("%d requests/%s", *limit.MaxRequests, limit.Period))
		}
		if limit.MaxTokens != nil {
			r.limits = append(r.limits, fmt.Sprintf("%d tokens/%s", *limit.MaxTokens, limit.Period))
		}
	}
	if len(rows) == 0 {
		return
	}
	paths := make([]string, 0, len(rows))
	for path := range rows {
		paths = append(paths, path)
	}
	slices.Sort(paths)
	b.WriteString("\nBudgets start from zero in GoModel; the LiteLLM spend is shown for reference.\n\n| User path | Allowed models | Budget | Rate limits |\n| --- | --- | --- | --- |\n")
	for _, path := range paths {
		r := rows[path]
		fmt.Fprintf(b, "| `%s` | %s | %s | %s |\n", path, codeList(r.models), orDash(r.budgets), orDash(r.limits))
	}
}

// formatSeconds renders a custom budget window, such as "12h" or "45d".
func formatSeconds(seconds int64) string {
	switch {
	case seconds%86400 == 0:
		return fmt.Sprintf("%dd", seconds/86400)
	case seconds%3600 == 0:
		return fmt.Sprintf("%dh", seconds/3600)
	case seconds%60 == 0:
		return fmt.Sprintf("%dm", seconds/60)
	}
	return fmt.Sprintf("%ds", seconds)
}

func orDash(items []string) string {
	if len(items) == 0 {
		return "-"
	}
	return strings.Join(items, ", ")
}
