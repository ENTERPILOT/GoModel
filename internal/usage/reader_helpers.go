package usage

import (
	"github.com/enterpilot/gomodel/internal/storage/sqlutil"
)

// usageGroupedProviderNameSQL returns a SQL expression that collapses blank
// provider_name values to the canonical provider before grouping.
func usageGroupedProviderNameSQL(providerNameColumn, providerColumn string) string {
	return "COALESCE(NULLIF(TRIM(" + providerNameColumn + "), ''), " + providerColumn + ")"
}

// usageGroupedUserPathSQL returns a SQL expression that collapses blank
// user_path values to the tracked root path before grouping.
func usageGroupedUserPathSQL(userPathColumn string) string {
	return "COALESCE(NULLIF(TRIM(" + userPathColumn + "), ''), '/')"
}

// clampLimitOffset applies the usage reader pagination policy:
// limit defaults to 50 and is capped at 200; offset floors at 0.
func clampLimitOffset(limit, offset int) (int, int) {
	return sqlutil.ClampLimitOffset(limit, offset, 50, 200)
}

// providerSessionCostSQL sums recorded provider spend while excluding local
// response-cache rows, whose stored cost represents avoided cost. A session
// served entirely from the local cache has known zero provider spend; sessions
// with provider requests but no pricing retain a NULL cost.
func providerSessionCostSQL(column string) string {
	providerRow := "(cache_type IS NULL OR cache_type = '')"
	return "CASE WHEN COUNT(CASE WHEN " + providerRow + " THEN 1 END) = 0 THEN 0 " +
		"ELSE SUM(CASE WHEN " + providerRow + " THEN " + column + " END) END"
}
