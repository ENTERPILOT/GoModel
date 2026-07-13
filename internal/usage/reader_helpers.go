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
