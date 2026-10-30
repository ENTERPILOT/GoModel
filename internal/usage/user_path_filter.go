package usage

import (
	"github.com/enterpilot/gomodel/internal/storage/sqlutil"

	"fmt"
	"regexp"
	"strings"

	"github.com/enterpilot/gomodel/internal/core"
)

func normalizeUsageUserPathFilter(raw string) (string, error) {
	userPath, err := core.NormalizeUserPath(raw)
	if err != nil {
		return "", fmt.Errorf("normalize usage user path filter: %w", err)
	}
	return userPath, nil
}

// usageUserPathFilters normalizes the user-path scope (UserPath) and the
// any-of subtree list (UserPaths). Blank and duplicate paths are dropped.
func usageUserPathFilters(params UsageQueryParams) (scope string, paths []string, err error) {
	scope, err = normalizeUsageUserPathFilter(params.UserPath)
	if err != nil {
		return "", nil, err
	}
	seen := map[string]bool{}
	for _, raw := range params.UserPaths {
		path, err := normalizeUsageUserPathFilter(raw)
		if err != nil {
			return "", nil, err
		}
		if path == "" || seen[path] {
			continue
		}
		seen[path] = true
		paths = append(paths, path)
	}
	return scope, paths, nil
}

// withoutUserPathFilters clears both user-path filters, for queries that
// apply them to a derived (canonical) user-path expression instead.
func withoutUserPathFilters(params UsageQueryParams) UsageQueryParams {
	params.UserPath = ""
	params.UserPaths = nil
	return params
}

// userPathConditions returns the SQL conditions for the user-path scope and
// the any-of subtree list, matched against column.
func userPathConditions(column string, params UsageQueryParams) ([]string, []any, error) {
	scope, paths, err := usageUserPathFilters(params)
	if err != nil {
		return nil, nil, err
	}
	var conditions []string
	var args []any
	if scope != "" {
		condition, conditionArgs := sqlutil.UserPathSubtreeCondition(column, scope)
		conditions = append(conditions, condition)
		args = append(args, conditionArgs...)
	}
	if len(paths) > 0 {
		parts := make([]string, 0, len(paths))
		for _, path := range paths {
			condition, conditionArgs := sqlutil.UserPathSubtreeCondition(column, path)
			parts = append(parts, condition)
			args = append(args, conditionArgs...)
		}
		conditions = append(conditions, "("+strings.Join(parts, " OR ")+")")
	}
	return conditions, args, nil
}

func usageUserPathSubtreePattern(userPath string) string {
	if userPath == "/" {
		return "/%"
	}
	return sqlutil.EscapeLikeWildcards(userPath) + "/%"
}

func usageUserPathSubtreeRegex(userPath string) string {
	if userPath == "/" {
		return "^/"
	}
	return "^" + regexp.QuoteMeta(userPath) + "(?:/|$)"
}
