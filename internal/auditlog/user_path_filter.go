package auditlog

import (
	"fmt"
	"regexp"

	"gomodel/internal/core"
)

func normalizeAuditUserPathFilter(raw string) (string, error) {
	userPath, err := core.NormalizeUserPath(raw)
	if err != nil {
		return "", fmt.Errorf("normalize audit user path filter: %w", err)
	}
	return userPath, nil
}

func auditUserPathSubtreePattern(userPath string) string {
	if userPath == "/" {
		return "/%"
	}
	return escapeLikeWildcards(userPath) + "/%"
}

func auditUserPathSQLPredicate(userPath, exactExpr, subtreeExpr string) string {
	predicate := "(" + exactExpr + " OR " + subtreeExpr
	if userPath == "/" {
		predicate += " OR user_path IS NULL"
	}
	return predicate + ")"
}

func auditUserPathSubtreeRegex(userPath string) string {
	if userPath == "/" {
		return "^/"
	}
	return "^" + regexp.QuoteMeta(userPath) + "(?:/|$)"
}
