package auditlog

import (
	"fmt"
	"regexp"

	"github.com/enterpilot/gomodel/internal/core"
)

func normalizeAuditUserPathFilter(raw string) (string, error) {
	userPath, err := core.NormalizeUserPath(raw)
	if err != nil {
		return "", fmt.Errorf("normalize audit user path filter: %w", err)
	}
	return userPath, nil
}

// auditUserPathSubtreeBounds returns the half-open range [userPath+"/",
// userPath+"0") that holds exactly the descendants of userPath under byte-wise
// ordering: '0' is the byte after '/', so every value starting with
// userPath+"/" sorts inside the range and nothing else does. Comparing against
// bounds instead of a LIKE pattern lets a btree index serve the subtree filter
// and needs no wildcard escaping.
func auditUserPathSubtreeBounds(userPath string) (lower, upper string) {
	if userPath == "/" {
		return "/", "0"
	}
	return userPath + "/", userPath + "0"
}

// auditUserPathSQLPredicate matches userPath itself and its subtree through
// column, which must compare bytes (BINARY on SQLite, COLLATE "C" on
// PostgreSQL) for the bounds from auditUserPathSubtreeBounds to mean "prefix".
// Bind userPath, then the lower and upper bound. Root also admits the legacy
// NULL rows written before user paths existed.
func auditUserPathSQLPredicate(userPath, column string) string {
	predicate := "(" + column + " = ? OR (" + column + " >= ? AND " + column + " < ?)"
	if userPath == "/" {
		predicate += " OR user_path IS NULL"
	}
	return predicate + ")"
}

func auditExactUserPathSQLPredicate(userPath, column string) string {
	if userPath == "/" {
		return "(" + column + " = ? OR user_path = '' OR user_path IS NULL)"
	}
	return column + " = ?"
}

func auditUserPathSubtreeRegex(userPath string) string {
	if userPath == "/" {
		return "^/"
	}
	return "^" + regexp.QuoteMeta(userPath) + "(?:/|$)"
}
