package plugins

import (
	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/pluginapi"
)

// SecretValues returns the non-empty value of every secret field of raw, by
// config key.
func SecretValues(schema []pluginapi.Field, raw json.RawMessage) map[string]string {
	values, err := decodeConfigObject(raw)
	if err != nil {
		return nil
	}
	secrets := make(map[string]string)
	for _, field := range schema {
		if field.Input != pluginapi.InputSecret {
			continue
		}
		if s, ok := values[field.Key].(string); ok && s != "" {
			secrets[field.Key] = s
		}
	}
	return secrets
}

// MapSecrets replaces every non-empty secret value of raw with what fn
// returns for it, stopping at the first error. raw is returned unchanged when
// fn changes nothing.
func MapSecrets(schema []pluginapi.Field, raw json.RawMessage, fn func(key, value string) (string, error)) (json.RawMessage, error) {
	values, err := decodeConfigObject(raw)
	if err != nil {
		return nil, err
	}
	changed := false
	for _, field := range schema {
		if field.Input != pluginapi.InputSecret {
			continue
		}
		s, ok := values[field.Key].(string)
		if !ok || s == "" {
			continue
		}
		mapped, err := fn(field.Key, s)
		if err != nil {
			return nil, err
		}
		if mapped != s {
			values[field.Key] = mapped
			changed = true
		}
	}
	if !changed {
		return raw, nil
	}
	return marshalCanonical(values)
}
