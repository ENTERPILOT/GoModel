package exchange

import (
	"fmt"
	"maps"
	"reflect"
	"sort"
	"strings"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
)

// extraParams decodes the raw request body and drops the keys the typed
// Params already expose, giving plugins a read-only view of the rest.
func extraParams(raw json.RawMessage, modelled ...string) map[string]any {
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil
	}
	for _, key := range modelled {
		delete(body, key)
	}
	if len(body) == 0 {
		return nil
	}
	return body
}

// jsonKeys lists the JSON member names of v's struct fields.
func jsonKeys(v any) map[string]bool {
	t := reflect.TypeOf(v)
	keys := make(map[string]bool, t.NumField())
	for field := range t.Fields() {
		if !field.IsExported() {
			continue
		}
		name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		if name == "-" {
			continue
		}
		if name == "" {
			name = field.Name
		}
		keys[name] = true
	}
	return keys
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func paramInt(name string, v any) (int, error) {
	switch n := v.(type) {
	case int:
		return n, nil
	case int64:
		return int(n), nil
	case float64:
		return int(n), nil
	case json.Number:
		i, err := n.Int64()
		return int(i), err
	case *int:
		if n != nil {
			return *n, nil
		}
	}
	return 0, fmt.Errorf("exchange: parameter %q must be an integer, got %T", name, v)
}

func paramFloat(name string, v any) (float64, error) {
	switch f := v.(type) {
	case float64:
		return f, nil
	case float32:
		return float64(f), nil
	case int:
		return float64(f), nil
	case int64:
		return float64(f), nil
	case json.Number:
		return f.Float64()
	case *float64:
		if f != nil {
			return *f, nil
		}
	}
	return 0, fmt.Errorf("exchange: parameter %q must be a number, got %T", name, v)
}

func paramString(name string, v any) (string, error) {
	if s, ok := v.(string); ok {
		return s, nil
	}
	return "", fmt.Errorf("exchange: parameter %q must be a string, got %T", name, v)
}

func paramBool(name string, v any) (bool, error) {
	if b, ok := v.(bool); ok {
		return b, nil
	}
	return false, fmt.Errorf("exchange: parameter %q must be a boolean, got %T", name, v)
}

// mergeJSONParams applies parameters through a JSON round trip: the typed
// value is encoded, the keys are set, and the result is decoded again. It is
// used for typed fields the explicit apply code does not model, and lets a
// plugin set any body-level key the request type knows.
func mergeJSONParams(target any, params map[string]json.RawMessage) error {
	if len(params) == 0 {
		return nil
	}
	encoded, err := json.Marshal(target)
	if err != nil {
		return err
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &body); err != nil {
		return err
	}
	maps.Copy(body, params)
	merged, err := json.Marshal(body)
	if err != nil {
		return err
	}
	return json.Unmarshal(merged, target)
}

// paramApplier splits SetParam keys into the ones applied through typed
// fields, the ones merged through JSON, and the ones stored as unknown
// extra fields.
type paramApplier struct {
	typedKeys map[string]bool
	frozen    map[string]bool
	jsonMerge map[string]json.RawMessage
	extras    map[string]json.RawMessage
}

func newParamApplier(typed any, frozen ...string) *paramApplier {
	a := &paramApplier{typedKeys: jsonKeys(typed), frozen: map[string]bool{}}
	for _, key := range frozen {
		a.frozen[key] = true
	}
	return a
}

// route stores a key the explicit switch did not handle.
func (a *paramApplier) route(key string, value any) error {
	if a.frozen[key] {
		return fmt.Errorf("exchange: parameter %q cannot be changed by a plugin", key)
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("exchange: parameter %q is not JSON-serializable: %w", key, err)
	}
	if a.typedKeys[key] {
		if a.jsonMerge == nil {
			a.jsonMerge = map[string]json.RawMessage{}
		}
		a.jsonMerge[key] = raw
		return nil
	}
	if a.extras == nil {
		a.extras = map[string]json.RawMessage{}
	}
	a.extras[key] = raw
	return nil
}

func (a *paramApplier) extraFields(base core.UnknownJSONFields) (core.UnknownJSONFields, error) {
	return core.MergeUnknownJSONFields(base, a.extras)
}

// prompt param helpers shared by both request types.
func maxTokensParam(v any) (*int, error) {
	n, err := paramInt("max_tokens", v)
	if err != nil {
		return nil, err
	}
	return &n, nil
}

func floatParam(name string, v any) (*float64, error) {
	f, err := paramFloat(name, v)
	if err != nil {
		return nil, err
	}
	return &f, nil
}
