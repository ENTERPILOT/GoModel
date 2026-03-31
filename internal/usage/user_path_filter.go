package usage

import (
	"fmt"
	"regexp"

	"gomodel/internal/core"
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
	return escapeLikeWildcards(userPath) + "/%"
}

func usageUserPathSubtreeRegex(userPath string) string {
	if userPath == "/" {
		return "^/"
	}
	return "^" + regexp.QuoteMeta(userPath) + "(?:/|$)"
}
