package config

import (
	"fmt"
	"os"
	"strings"

	"gomodel/internal/core"
)

// applyUserPathLimitEnv merges SET_<prefix>* env entries into per-user-path
// config entries. The env var suffix becomes the user path (see
// userPathEnvSuffixPath); an env entry replaces the whole existing entry with
// the same canonical path, even when YAML uses non-canonical paths like
// "alice" or "/alice/".
func applyUserPathLimitEnv[Entry any, Limit any](
	entries []Entry,
	prefix string,
	entryPath func(Entry) string,
	parseLimits func(string) ([]Limit, error),
	newEntry func(path string, limits []Limit) Entry,
) ([]Entry, error) {
	for _, item := range os.Environ() {
		key, value, ok := strings.Cut(item, "=")
		if !ok || !strings.HasPrefix(key, prefix) || strings.TrimSpace(value) == "" {
			continue
		}
		path, err := core.NormalizeUserPath(userPathEnvSuffixPath(key[len(prefix):]))
		if err != nil {
			return nil, fmt.Errorf("invalid value for %s: %w", key, err)
		}
		limits, err := parseLimits(value)
		if err != nil {
			return nil, fmt.Errorf("invalid value for %s: %w", key, err)
		}
		if len(limits) == 0 {
			continue
		}
		replaced := entries[:0]
		for _, existing := range entries {
			existingNorm, normErr := core.NormalizeUserPath(entryPath(existing))
			if normErr != nil || existingNorm != path {
				replaced = append(replaced, existing)
			}
		}
		entries = append(replaced, newEntry(path, limits))
	}
	return entries, nil
}

// userPathEnvSuffixPath converts an env var suffix into a user path: double
// underscores separate path segments, single underscores stay literal.
func userPathEnvSuffixPath(suffix string) string {
	suffix = strings.ToLower(strings.TrimSpace(suffix))
	if suffix == "" {
		return "/"
	}
	segments := make([]string, 0)
	for _, part := range strings.Split(suffix, "__") {
		part = strings.TrimSpace(part)
		if part != "" {
			segments = append(segments, part)
		}
	}
	if len(segments) == 0 {
		return "/"
	}
	return "/" + strings.Join(segments, "/")
}
