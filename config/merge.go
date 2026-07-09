package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
)

// decodeIaCJSON decodes an infrastructure-as-code env var into target. The env layer
// declares the same structures as the YAML layer and overrides it entry by entry, so
// a typo must fail as loudly here as it does there — a silently ignored key would let
// a malformed env entry win over a correct YAML one. CONFIG_STRICT=false downgrades
// unknown keys to warnings, matching the YAML layer; malformed values stay fatal.
func decodeIaCJSON(source, raw string, target any, strict bool) error {
	err := decodeStrictJSON(raw, target)
	if err == nil || strict || !isUnknownFieldJSONError(err) {
		return err
	}
	slog.Warn("unknown config key ignored; it has no effect", "source", source, "detail", err.Error())
	// The strict decode stopped at the unknown key, so re-decode leniently to fill
	// target from the whole value.
	return json.Unmarshal([]byte(raw), target)
}

func decodeStrictJSON(raw string, target any) error {
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	// Decode stops after the first JSON value and leaves the rest unread, so
	// `[{...}] junk` would pass. json.Unmarshal rejects trailing data and so must we:
	// silently ignoring half an env var is the bug this whole path exists to prevent.
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return fmt.Errorf("unexpected data after the JSON value")
	}
	return nil
}

// isUnknownFieldJSONError reports whether err is encoding/json's unknown-key error,
// the only decode error CONFIG_STRICT=false is allowed to downgrade.
func isUnknownFieldJSONError(err error) bool {
	return strings.HasPrefix(err.Error(), "json: unknown field ")
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
