// Package sqlutil provides small SQL query-building helpers shared by the
// SQL-backed readers (usage, audit log).
package sqlutil

import "strings"

// EscapeLikeWildcards escapes SQL LIKE/ILIKE wildcard characters in user input
// to prevent wildcard injection. Escapes \, %, and _.
func EscapeLikeWildcards(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `%`, `\%`)
	s = strings.ReplaceAll(s, `_`, `\_`)
	return s
}

// BuildWhereClause joins condition strings into a SQL WHERE clause.
// Returns an empty string when conditions is empty.
func BuildWhereClause(conditions []string) string {
	if len(conditions) == 0 {
		return ""
	}
	return " WHERE " + strings.Join(conditions, " AND ")
}

// ClampLimitOffset normalises pagination parameters: limit defaults to
// defaultLimit when non-positive and is capped at maxLimit; offset floors at 0.
func ClampLimitOffset(limit, offset, defaultLimit, maxLimit int) (int, int) {
	if limit <= 0 {
		limit = defaultLimit
	}
	if limit > maxLimit {
		limit = maxLimit
	}
	if offset < 0 {
		offset = 0
	}
	return limit, offset
}
