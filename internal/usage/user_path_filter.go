package usage

import (
	"github.com/enterpilot/gomodel/internal/storage/sqlutil"

	"fmt"
	"regexp"

	"github.com/enterpilot/gomodel/internal/core"
)

func normalizeUsageUserPathFilter(raw string) (string, error) {
	userPath, err := core.NormalizeUserPath(raw)
	if err != nil {
		return "", fmt.Errorf("normalize usage user path filter: %w", err)
	}
	return userPath, nil
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
