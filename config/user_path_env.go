package config

import (
	"fmt"
	"os"
	"strings"

	"gomodel/internal/core"
)

// applyKeyedLimitEnv merges <prefix>* env entries into keyed config entries.
// The env var suffix resolves to the entry key via keyFromSuffix; an env
// entry replaces the whole existing entry with the same canonical key, even
// when YAML spells the key non-canonically.
func applyKeyedLimitEnv[Entry any, Limit any](
	entries []Entry,
	prefix string,
	keyFromSuffix func(string) (string, error),
	canonicalKey func(string) (string, error),
	entryKey func(Entry) string,
	parseLimits func(string) ([]Limit, error),
	newEntry func(key string, limits []Limit) Entry,
) ([]Entry, error) {
	for _, item := range os.Environ() {
		key, value, ok := strings.Cut(item, "=")
		if !ok || !strings.HasPrefix(key, prefix) || strings.TrimSpace(value) == "" {
			continue
		}
		entryKeyValue, err := keyFromSuffix(key[len(prefix):])
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
			existingKey, keyErr := canonicalKey(entryKey(existing))
			if keyErr != nil || existingKey != entryKeyValue {
				replaced = append(replaced, existing)
			}
		}
		entries = append(replaced, newEntry(entryKeyValue, limits))
	}
	return entries, nil
}

// applyUserPathLimitEnv merges SET_<prefix>* env entries into per-user-path
// config entries. The env var suffix becomes the user path (see
// userPathEnvSuffixPath).
func applyUserPathLimitEnv[Entry any, Limit any](
	entries []Entry,
	prefix string,
	entryPath func(Entry) string,
	parseLimits func(string) ([]Limit, error),
	newEntry func(path string, limits []Limit) Entry,
) ([]Entry, error) {
	return applyKeyedLimitEnv(
		entries,
		prefix,
		func(suffix string) (string, error) {
			return core.NormalizeUserPath(userPathEnvSuffixPath(suffix))
		},
		core.NormalizeUserPath,
		entryPath,
		parseLimits,
		newEntry,
	)
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
