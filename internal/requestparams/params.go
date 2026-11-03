// Package requestparams applies operator-configured request parameters to
// translated inference requests. A virtual model or model policy may carry two
// parameter sets: defaults, filled in only when the client omitted the key, and
// forced params, which replace the client's value. Keys use Chat Completions
// spelling; provider adapters translate them exactly as they translate
// client-sent values.
package requestparams

import (
	"bytes"
	"fmt"
	"maps"
	"sort"
	"strings"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/validation"
)

// Set maps body-level parameter names to their JSON values. A nil value in a
// forced set removes the key from the request.
type Set map[string]json.RawMessage

// Limits bounding a configured set, so a row cannot grow a request body
// without bound or stall validation.
const (
	MaxKeys    = 64
	MaxKeyLen  = 64
	MaxBytes   = 16 << 10
	nullLexeme = "null"
)

// frozenKeys cannot be configured: they carry the conversation, select the
// route, or shape the transport, and changing them per alias would break the
// gateway's own handling of the request. Reasoning effort has its own typed
// override with provider translation, so it is refused here too.
var frozenKeys = map[string]struct{}{
	"model":                {},
	"provider":             {},
	"messages":             {},
	"input":                {},
	"instructions":         {},
	"tools":                {},
	"stream":               {},
	"stream_options":       {},
	"reasoning":            {},
	"reasoning_effort":     {},
	"previous_response_id": {},
	"conversation":         {},
	"prompt":               {},
}

// Overrides is everything the operator forces onto one provider attempt: the
// reasoning effort and both parameter sets, already merged along the route
// the request took.
type Overrides struct {
	ReasoningEffort string
	Defaults        Set
	Params          Set
}

// IsZero reports whether nothing is configured.
func (o Overrides) IsZero() bool {
	return o.ReasoningEffort == "" && len(o.Defaults) == 0 && len(o.Params) == 0
}

// Layer returns o with the deeper over applied on top: a non-empty effort
// replaces, parameter sets merge per key with over winning.
func (o Overrides) Layer(over Overrides) Overrides {
	if over.ReasoningEffort != "" {
		o.ReasoningEffort = over.ReasoningEffort
	}
	o.Defaults = Merge(o.Defaults, over.Defaults)
	o.Params = Merge(o.Params, over.Params)
	return o
}

// Merge returns base with over's keys added, over winning on conflict. Sets
// are treated as immutable: the result shares no map with either input when
// both are non-empty, and is the non-empty input otherwise.
func Merge(base, over Set) Set {
	switch {
	case len(over) == 0:
		return base
	case len(base) == 0:
		return over
	}
	merged := make(Set, len(base)+len(over))
	maps.Copy(merged, base)
	maps.Copy(merged, over)
	return merged
}

// Clone deep-copies a set so stored rows are never shared with callers.
func Clone(set Set) Set {
	if set == nil {
		return nil
	}
	cloned := make(Set, len(set))
	for key, value := range set {
		if value == nil {
			cloned[key] = nil
			continue
		}
		cloned[key] = bytes.Clone(value)
	}
	return cloned
}

// FromAny encodes a decoded YAML or JSON object into a set.
func FromAny(values map[string]any) (Set, error) {
	if len(values) == 0 {
		return nil, nil
	}
	set := make(Set, len(values))
	for key, value := range values {
		if value == nil {
			set[key] = nil
			continue
		}
		raw, err := json.Marshal(value)
		if err != nil {
			return nil, validation.NewError(fmt.Sprintf("parameter %q is not JSON-serializable", key), err)
		}
		set[key] = raw
	}
	return set, nil
}

// ToAny decodes a set into plain values for storage drivers and views that
// do not carry raw JSON.
func ToAny(set Set) map[string]any {
	if len(set) == 0 {
		return nil
	}
	values := make(map[string]any, len(set))
	for key, raw := range set {
		var value any
		if len(raw) > 0 {
			_ = json.Unmarshal(raw, &value)
		}
		values[key] = value
	}
	return values
}

// Normalize validates a configured set and returns it in canonical form:
// trimmed keys, compact values, and nil for an empty set. allowNull admits
// null values, which a forced set uses to strip a key. field names the
// setting in messages.
func Normalize(field string, set Set, allowNull bool) (Set, error) {
	if len(set) == 0 {
		return nil, nil
	}
	if len(set) > MaxKeys {
		return nil, validation.NewError(fmt.Sprintf("%s: at most %d parameters are allowed", field, MaxKeys), nil)
	}
	normalized := make(Set, len(set))
	size := 0
	for key, raw := range set {
		if err := validateKey(field, key); err != nil {
			return nil, err
		}
		if _, dup := normalized[key]; dup {
			return nil, validation.NewError(fmt.Sprintf("%s: duplicate parameter %q", field, key), nil)
		}
		value := bytes.TrimSpace(raw)
		if len(value) == 0 || bytes.Equal(value, []byte(nullLexeme)) {
			if !allowNull {
				return nil, validation.NewError(fmt.Sprintf("%s: parameter %q must have a value", field, key), nil)
			}
			normalized[key] = nil
			continue
		}
		if !json.Valid(value) {
			return nil, validation.NewError(fmt.Sprintf("%s: parameter %q is not valid JSON", field, key), nil)
		}
		compact, err := compactJSON(value)
		if err != nil {
			return nil, validation.NewError(fmt.Sprintf("%s: parameter %q is not valid JSON", field, key), err)
		}
		if err := validateTyped(field, key, compact); err != nil {
			return nil, err
		}
		size += len(key) + len(compact)
		if size > MaxBytes {
			return nil, validation.NewError(fmt.Sprintf("%s: parameters exceed %d bytes", field, MaxBytes), nil)
		}
		normalized[key] = compact
	}
	return normalized, nil
}

// Disjoint rejects a key configured in both sets of one row, where the
// forced value would silently make the default dead.
func Disjoint(defaults, params Set) error {
	for key := range params {
		if _, both := defaults[key]; both {
			return validation.NewError(fmt.Sprintf("parameter %q cannot be in both params and default_params", key), nil)
		}
	}
	return nil
}

func validateKey(field, key string) error {
	if key == "" || strings.TrimSpace(key) != key {
		return validation.NewError(fmt.Sprintf("%s: parameter names cannot be empty or padded", field), nil)
	}
	if len(key) > MaxKeyLen {
		return validation.NewError(fmt.Sprintf("%s: parameter name %q is longer than %d characters", field, key, MaxKeyLen), nil)
	}
	for _, r := range key {
		if r < 0x20 || r == 0x7f {
			return validation.NewError(fmt.Sprintf("%s: parameter name %q contains control characters", field, key), nil)
		}
	}
	if _, frozen := frozenKeys[key]; frozen {
		return validation.NewError(fmt.Sprintf("%s: parameter %q cannot be configured on a virtual model", field, key), nil)
	}
	return nil
}

// validateTyped type-checks the keys the gateway models as typed fields, so a
// misconfigured row fails at save time rather than on the first request.
func validateTyped(field, key string, raw json.RawMessage) error {
	var err error
	switch key {
	case "temperature", "top_p", "presence_penalty", "frequency_penalty":
		var v float64
		err = json.Unmarshal(raw, &v)
	case "max_tokens", "max_output_tokens", "max_completion_tokens", "top_logprobs":
		var v int
		if err = json.Unmarshal(raw, &v); err == nil && v < 0 {
			err = fmt.Errorf("must not be negative")
		}
	case "parallel_tool_calls", "store", "logprobs":
		var v bool
		err = json.Unmarshal(raw, &v)
	case "user", "service_tier", "truncation", "safety_identifier", "prompt_cache_retention":
		var v string
		err = json.Unmarshal(raw, &v)
	case "tool_choice":
		if raw[0] != '"' && raw[0] != '{' {
			err = fmt.Errorf("must be a string or an object")
		}
	case "metadata":
		var v map[string]string
		err = json.Unmarshal(raw, &v)
	default:
		return nil
	}
	if err != nil {
		return validation.NewError(fmt.Sprintf("%s: parameter %q has the wrong type: %v", field, key, err), nil)
	}
	return nil
}

func compactJSON(raw []byte) (json.RawMessage, error) {
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return nil, err
	}
	return json.RawMessage(bytes.Clone(buf.Bytes())), nil
}

// sortedKeys yields a set's keys in a stable order so application is
// deterministic and tests are reproducible.
func sortedKeys(set Set) []string {
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
