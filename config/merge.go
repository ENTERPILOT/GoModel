package config

import "strings"

// mergeByKey overlays override entries onto base, replacing matching base
// entries in place and appending new entries.
func mergeByKey[T any](base, override []T, key func(T) string) []T {
	if len(override) == 0 {
		return base
	}
	merged := make([]T, len(base))
	copy(merged, base)
	index := make(map[string]int, len(merged))
	for i, item := range merged {
		index[key(item)] = i
	}
	for _, item := range override {
		itemKey := key(item)
		if pos, ok := index[itemKey]; ok {
			merged[pos] = item
			continue
		}
		index[itemKey] = len(merged)
		merged = append(merged, item)
	}
	return merged
}

func canonicalTextKey(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}
