package usage

import (
	"strings"
)

// escapeLikeWildcards escapes SQL LIKE/ILIKE wildcard characters in user input
// to prevent wildcard injection. Escapes \, %, and _.
func escapeLikeWildcards(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `%`, `\%`)
	s = strings.ReplaceAll(s, `_`, `\_`)
	return s
}

// buildWhereClause joins condition strings into a SQL WHERE clause.
// Returns an empty string when conditions is empty.
func buildWhereClause(conditions []string) string {
	if len(conditions) == 0 {
		return ""
	}
	return " WHERE " + strings.Join(conditions, " AND ")
}

// usageGroupedProviderNameSQL returns a SQL expression that collapses blank
// provider_name values to the canonical provider before grouping.
func usageGroupedProviderNameSQL(providerNameColumn, providerColumn string) string {
	return "COALESCE(NULLIF(TRIM(" + providerNameColumn + "), ''), " + providerColumn + ")"
}

// clampLimitOffset normalises pagination parameters:
//   - limit defaults to 50 and is capped at 200
//   - offset floors at 0
func clampLimitOffset(limit, offset int) (int, int) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	if offset < 0 {
		offset = 0
	}
	return limit, offset
}
