package providers

import (
	"context"
	"strconv"
	"strings"

	"github.com/enterpilot/gomodel/config"
)

// sourcedKey is one API key together with every place the operator wrote it.
//
// A source is the YAML path of a config.yaml value ("providers.openai.api_key",
// "providers.openai.api_keys[1]") or the name of the environment variable that
// supplied it ("OPENAI_API_KEY_2"). Secret references are resolved, and their
// errors reported, under Sources[0]. Sources has more than one entry when
// identical keys were collapsed into one.
type sourcedKey struct {
	Value   string
	Sources []string
}

// yamlAPIKeys lists a config.yaml provider's keys under their YAML paths:
// api_key first, then api_keys in order.
func yamlAPIKeys(name string, p config.RawProviderConfig) []sourcedKey {
	prefix := "providers." + name
	keys := make([]sourcedKey, 0, len(p.APIKeys)+1)
	keys = append(keys, sourcedKey{Value: p.APIKey, Sources: []string{prefix + ".api_key"}})
	for i, key := range p.APIKeys {
		keys = append(keys, sourcedKey{Value: key, Sources: []string{prefix + ".api_keys[" + strconv.Itoa(i) + "]"}})
	}
	return keys
}

// dedupeKeys trims each key, drops the ones usable rejects, and collapses
// repeats into the first occurrence, keeping every source. Order is preserved.
func dedupeKeys(keys []sourcedKey, usable func(string) bool) []sourcedKey {
	result := make([]sourcedKey, 0, len(keys))
	index := make(map[string]int, len(keys))
	for _, key := range keys {
		value := strings.TrimSpace(key.Value)
		if !usable(value) {
			continue
		}
		if i, dup := index[value]; dup {
			result[i].Sources = append(result[i].Sources, key.Sources...)
			continue
		}
		index[value] = len(result)
		result = append(result, sourcedKey{Value: value, Sources: append([]string(nil), key.Sources...)})
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

// resolveSecretKeys resolves the secret references in keys, each once and
// under its first source, then drops empty results and collapses keys that
// resolved to the same value.
func resolveSecretKeys(ctx context.Context, secrets *config.Secrets, keys []sourcedKey) ([]sourcedKey, error) {
	resolved := make([]sourcedKey, len(keys))
	for i, key := range keys {
		value := key.Value
		if err := secrets.ResolveFields(ctx, key.Sources[0], &value); err != nil {
			return nil, err
		}
		resolved[i] = sourcedKey{Value: value, Sources: key.Sources}
	}
	return dedupeKeys(resolved, resolvedValueSet), nil
}

// keyValues returns the primary key and the full ordered key set.
func keyValues(keys []sourcedKey) (string, []string) {
	if len(keys) == 0 {
		return "", nil
	}
	values := make([]string, len(keys))
	for i, key := range keys {
		values[i] = key.Value
	}
	return values[0], values
}

// keySources returns the sources of each key, aligned with keyValues.
func keySources(keys []sourcedKey) [][]string {
	if len(keys) == 0 {
		return nil
	}
	sources := make([][]string, len(keys))
	for i, key := range keys {
		sources[i] = key.Sources
	}
	return sources
}
