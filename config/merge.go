package config

import (
	"encoding/json"
	"strings"
)

// decodeStrictJSON decodes an infrastructure-as-code env var into target, rejecting
// unknown keys. The env layer declares the same structures as the YAML layer and
// overrides it entry by entry, so a typo must fail loudly here too — a silently
// ignored key would let a malformed env entry win over a correct YAML one.
func decodeStrictJSON(raw string, target any) error {
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
}

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
