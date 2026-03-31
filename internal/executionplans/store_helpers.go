package executionplans

import (
	"fmt"
	"strings"
)

type versionRowScanner interface {
	Scan(dest ...any) error
}

type versionRowIterator interface {
	versionRowScanner
	Next() bool
	Err() error
}

func collectVersions(rows versionRowIterator, scan func(versionRowScanner) (Version, error)) ([]Version, error) {
	versions := make([]Version, 0)
	for rows.Next() {
		version, err := scan(rows)
		if err != nil {
			return nil, err
		}
		versions = append(versions, version)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate execution plans: %w", err)
	}
	return versions, nil
}

func storedScopeUserPath(scopeKey, userPath string) string {
	userPath = strings.TrimSpace(userPath)
	if userPath != "" {
		return userPath
	}

	switch {
	case strings.HasPrefix(scopeKey, "path:"):
		path := strings.TrimPrefix(scopeKey, "path:")
		if path == "" {
			return "/"
		}
		return path
	case strings.HasPrefix(scopeKey, "provider_path:"):
		parts := strings.SplitN(scopeKey, ":", 3)
		if len(parts) == 3 {
			if strings.TrimSpace(parts[2]) == "" {
				return "/"
			}
			return parts[2]
		}
	case strings.HasPrefix(scopeKey, "provider_model_path:"):
		parts := strings.SplitN(scopeKey, ":", 4)
		if len(parts) == 4 {
			if strings.TrimSpace(parts[3]) == "" {
				return "/"
			}
			return parts[3]
		}
	}

	return ""
}
