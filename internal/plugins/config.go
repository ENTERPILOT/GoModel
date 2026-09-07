package plugins

import (
	"bytes"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/pluginapi"
)

// SecretMask is what a stored secret is rendered as in admin responses. A
// client sending it back unchanged keeps the stored value; sending "" clears it.
const SecretMask = "********"

// ValidateConfig checks raw against the schema fields of the given scope and
// returns the canonical config: defaults applied, values coerced to the
// field's type, keys sorted. Unknown keys are rejected unless the schema has
// no field in that scope at all, in which case the config passes through.
func ValidateConfig(schema []pluginapi.Field, raw json.RawMessage, scope pluginapi.FieldScope) (json.RawMessage, error) {
	values, err := decodeConfigObject(raw)
	if err != nil {
		return nil, err
	}
	fields := scopedFields(schema, scope)
	if len(fields) == 0 {
		return marshalCanonical(values)
	}
	known := make(map[string]struct{}, len(fields))
	for _, field := range fields {
		known[field.Key] = struct{}{}
	}
	for key := range values {
		if _, ok := known[key]; !ok {
			return nil, fmt.Errorf("unknown config key %q", key)
		}
	}
	out := make(map[string]any, len(fields))
	for _, field := range fields {
		value, present := values[field.Key]
		if !present || value == nil || isEmptyString(value) {
			if field.Default == nil {
				if !present {
					continue
				}
			} else {
				value = field.Default
			}
		}
		coerced, err := coerceField(field, value)
		if err != nil {
			return nil, fmt.Errorf("config key %q: %w", field.Key, err)
		}
		if coerced == nil {
			continue
		}
		out[field.Key] = coerced
	}
	for _, field := range fields {
		if !field.Required {
			continue
		}
		if isMissing(out[field.Key]) {
			return nil, fmt.Errorf("config key %q is required", field.Key)
		}
	}
	return marshalCanonical(out)
}

// RedactSecrets replaces every non-empty secret value with SecretMask.
func RedactSecrets(schema []pluginapi.Field, raw json.RawMessage) json.RawMessage {
	values, err := decodeConfigObject(raw)
	if err != nil {
		return raw
	}
	changed := false
	for _, field := range schema {
		if field.Input != pluginapi.InputSecret {
			continue
		}
		if s, ok := values[field.Key].(string); ok && s != "" {
			values[field.Key] = SecretMask
			changed = true
		}
	}
	if !changed {
		return raw
	}
	out, err := marshalCanonical(values)
	if err != nil {
		return raw
	}
	return out
}

// MergeSecrets restores stored secret values where incoming carries the mask.
// An empty incoming secret clears the stored value.
func MergeSecrets(schema []pluginapi.Field, incoming, stored json.RawMessage) json.RawMessage {
	in, err := decodeConfigObject(incoming)
	if err != nil {
		return incoming
	}
	old, err := decodeConfigObject(stored)
	if err != nil {
		return incoming
	}
	changed := false
	for _, field := range schema {
		if field.Input != pluginapi.InputSecret {
			continue
		}
		if s, ok := in[field.Key].(string); ok && s == SecretMask {
			if prev, ok := old[field.Key]; ok {
				in[field.Key] = prev
			} else {
				delete(in, field.Key)
			}
			changed = true
		}
	}
	if !changed {
		return incoming
	}
	out, err := marshalCanonical(in)
	if err != nil {
		return incoming
	}
	return out
}

// SchemaDefaults renders the default config object of the instance-scoped
// fields: each field's Default, or the empty value of its input kind.
func SchemaDefaults(schema []pluginapi.Field) json.RawMessage {
	out := map[string]any{}
	for _, field := range scopedFields(schema, pluginapi.ScopeInstance) {
		if field.Default != nil {
			out[field.Key] = field.Default
			continue
		}
		switch field.Input {
		case pluginapi.InputCheckboxes:
			out[field.Key] = []string{}
		case pluginapi.InputNumber, pluginapi.InputSelect:
		default:
			out[field.Key] = ""
		}
	}
	raw, err := marshalCanonical(out)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return raw
}

// ConfigHash returns a short stable digest of a config for chain hashing.
func ConfigHash(raw json.RawMessage) string {
	canonical := raw
	if values, err := decodeConfigObject(raw); err == nil {
		if encoded, err := marshalCanonical(values); err == nil {
			canonical = encoded
		}
	}
	return fmt.Sprintf("%016x", xxhashString(string(canonical)))
}

func scopedFields(schema []pluginapi.Field, scope pluginapi.FieldScope) []pluginapi.Field {
	var fields []pluginapi.Field
	for _, field := range schema {
		if field.Scope == scope {
			fields = append(fields, field)
		}
	}
	return fields
}

func decodeConfigObject(raw json.RawMessage) (map[string]any, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return map[string]any{}, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var values map[string]any
	if err := decoder.Decode(&values); err != nil {
		return nil, fmt.Errorf("config must be a JSON object: %w", err)
	}
	if decoder.More() {
		return nil, fmt.Errorf("config has trailing data")
	}
	if values == nil {
		values = map[string]any{}
	}
	return values, nil
}

func marshalCanonical(values map[string]any) (json.RawMessage, error) {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, key := range keys {
		if i > 0 {
			buf.WriteByte(',')
		}
		encodedKey, err := json.Marshal(key)
		if err != nil {
			return nil, err
		}
		buf.Write(encodedKey)
		buf.WriteByte(':')
		encoded, err := json.Marshal(values[key])
		if err != nil {
			return nil, fmt.Errorf("encode config key %q: %w", key, err)
		}
		buf.Write(encoded)
	}
	buf.WriteByte('}')
	return json.RawMessage(buf.Bytes()), nil
}

func coerceField(field pluginapi.Field, value any) (any, error) {
	switch field.Input {
	case pluginapi.InputNumber:
		return coerceNumber(value)
	case pluginapi.InputCheckboxes:
		return coerceCheckboxes(field, value)
	case pluginapi.InputSelect:
		s, err := coerceString(value, true)
		if err != nil {
			return nil, err
		}
		if s == "" {
			return nil, nil
		}
		if len(field.Options) > 0 && !hasOption(field.Options, s) {
			return nil, fmt.Errorf("value %q is not one of the allowed options", s)
		}
		return s, nil
	case pluginapi.InputSecret:
		return coerceString(value, false)
	case pluginapi.InputTextarea:
		return coerceTextarea(value)
	default: // text, model, or unknown
		return coerceString(value, true)
	}
}

// coerceTextarea accepts a string or, for YAML convenience, a list of
// strings that becomes one line per entry.
func coerceTextarea(value any) (string, error) {
	items, ok := value.([]any)
	if !ok {
		return coerceString(value, true)
	}
	lines := make([]string, 0, len(items))
	for i, item := range items {
		line, err := coerceString(item, true)
		if err != nil {
			return "", fmt.Errorf("item %d: %w", i, err)
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n"), nil
}

func coerceString(value any, trim bool) (string, error) {
	var s string
	switch v := value.(type) {
	case string:
		s = v
	case json.Number:
		s = v.String()
	case bool:
		s = strconv.FormatBool(v)
	case float64:
		s = strconv.FormatFloat(v, 'f', -1, 64)
	case int:
		s = strconv.Itoa(v)
	case int64:
		s = strconv.FormatInt(v, 10)
	default:
		return "", fmt.Errorf("expected a string, got %T", value)
	}
	if trim {
		s = strings.TrimSpace(s)
	}
	return s, nil
}

func coerceNumber(value any) (any, error) {
	var text string
	switch v := value.(type) {
	case json.Number:
		text = v.String()
	case string:
		text = strings.TrimSpace(v)
		if text == "" {
			return nil, nil
		}
	case float64:
		return normalizeFloat(v), nil
	case float32:
		return normalizeFloat(float64(v)), nil
	case int:
		return int64(v), nil
	case int64:
		return v, nil
	default:
		return nil, fmt.Errorf("expected a number, got %T", value)
	}
	if i, err := strconv.ParseInt(text, 10, 64); err == nil {
		return i, nil
	}
	f, err := strconv.ParseFloat(text, 64)
	if err != nil {
		return nil, fmt.Errorf("expected a number, got %q", text)
	}
	return normalizeFloat(f), nil
}

func normalizeFloat(f float64) any {
	if f == float64(int64(f)) {
		return int64(f)
	}
	return f
}

func coerceCheckboxes(field pluginapi.Field, value any) ([]string, error) {
	var items []any
	switch v := value.(type) {
	case []any:
		items = v
	case []string:
		for _, s := range v {
			items = append(items, s)
		}
	case string:
		if strings.TrimSpace(v) == "" {
			return []string{}, nil
		}
		items = []any{v}
	default:
		return nil, fmt.Errorf("expected a list of options, got %T", value)
	}
	out := make([]string, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		s, err := coerceString(item, true)
		if err != nil {
			return nil, err
		}
		if s == "" {
			continue
		}
		if len(field.Options) > 0 && !hasOption(field.Options, s) {
			return nil, fmt.Errorf("value %q is not one of the allowed options", s)
		}
		if _, dup := seen[s]; dup {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out, nil
}

func hasOption(options []pluginapi.Option, value string) bool {
	for _, option := range options {
		if option.Value == value {
			return true
		}
	}
	return false
}

func isEmptyString(value any) bool {
	s, ok := value.(string)
	return ok && strings.TrimSpace(s) == ""
}

func isMissing(value any) bool {
	switch v := value.(type) {
	case nil:
		return true
	case string:
		return strings.TrimSpace(v) == ""
	case []string:
		return len(v) == 0
	case []any:
		return len(v) == 0
	}
	return false
}
