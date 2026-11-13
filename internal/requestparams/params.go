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
	"unicode"

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

// Overrides is everything the operator configures for one provider attempt:
// the forced reasoning effort and both parameter sets, already merged along
// the route the request took, and the ceilings that bound the result.
type Overrides struct {
	ReasoningEffort string
	Defaults        Set
	Params          Set
	// Ceilings apply last, to the request as the fields above left it, so
	// they bound a forced or defaulted value as they bound the client's.
	Ceilings Ceilings
}

// IsZero reports whether nothing is configured.
func (o Overrides) IsZero() bool {
	return !o.Forces() && o.Ceilings.IsZero()
}

// Forces reports whether a reasoning effort or a parameter set is
// configured, which rewrites every request; ceilings alone rewrite only a
// request that exceeds them or sets no output token limit.
func (o Overrides) Forces() bool {
	return o.ReasoningEffort != "" || len(o.Defaults) > 0 || len(o.Params) > 0
}

// Layer returns o with the deeper over applied on top: a non-empty effort
// replaces, and parameter sets merge per key with over winning. Precedence
// holds across the two sets: a key over configures as a default stops being
// forced by a shallower row, and one it forces stops being a shallower
// row's default. Ceilings have no winner: the tightest of both stays.
func (o Overrides) Layer(over Overrides) Overrides {
	if over.ReasoningEffort != "" {
		o.ReasoningEffort = over.ReasoningEffort
	}
	o.Ceilings = o.Ceilings.Tighten(over.Ceilings)
	o.Defaults = Merge(without(o.Defaults, over.Params), over.Defaults)
	o.Params = Merge(without(o.Params, over.Defaults), over.Params)
	return o
}

// without returns set minus the keys of other, sharing set when nothing is
// removed.
func without(set, other Set) Set {
	if len(set) == 0 || len(other) == 0 {
		return set
	}
	drop := canonicalKeys(other)
	covered := func(key string) bool {
		_, ok := drop[canonicalKey(key)]
		return ok
	}
	shared := false
	for key := range set {
		if covered(key) {
			shared = true
			break
		}
	}
	if !shared {
		return set
	}
	kept := make(Set, len(set))
	for key, value := range set {
		if !covered(key) {
			kept[key] = value
		}
	}
	if len(kept) == 0 {
		return nil
	}
	return kept
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
	// A key over sets in another spelling of the same parameter replaces
	// the base's too.
	merged := make(Set, len(base)+len(over))
	maps.Copy(merged, without(base, over))
	maps.Copy(merged, over)
	return merged
}

// tokenLimitKeys are the spellings of one parameter, the output token limit:
// Chat Completions' max_tokens and max_completion_tokens, and the Responses
// API's max_output_tokens. Sets match and layer them as one key.
var tokenLimitKeys = map[string]struct{}{
	"max_tokens": {}, "max_completion_tokens": {}, "max_output_tokens": {},
}

// canonicalKey names the parameter a key sets, folding the token-limit
// spellings into one.
func canonicalKey(key string) string {
	if _, ok := tokenLimitKeys[key]; ok {
		return "max_tokens"
	}
	return key
}

func canonicalKeys(set Set) map[string]struct{} {
	keys := make(map[string]struct{}, len(set))
	for key := range set {
		keys[canonicalKey(key)] = struct{}{}
	}
	return keys
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
	spelledAs := make(map[string]string, len(set))
	for _, key := range sortedKeys(set) {
		raw := set[key]
		if err := validateKey(field, key); err != nil {
			return nil, err
		}
		if _, dup := normalized[key]; dup {
			return nil, validation.NewError(fmt.Sprintf("%s: duplicate parameter %q", field, key), nil)
		}
		if other, dup := spelledAs[canonicalKey(key)]; dup {
			return nil, validation.NewError(fmt.Sprintf("%s: %q and %q set the same token limit; keep one", field, other, key), nil)
		}
		spelledAs[canonicalKey(key)] = key
		value := bytes.TrimSpace(raw)
		if len(value) == 0 || bytes.Equal(value, []byte(nullLexeme)) {
			if !allowNull {
				return nil, validation.NewError(fmt.Sprintf("%s: parameter %q must have a value", field, key), nil)
			}
			size += len(key) + len(nullLexeme)
			if size > MaxBytes {
				return nil, validation.NewError(fmt.Sprintf("%s: parameters exceed %d bytes", field, MaxBytes), nil)
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
	forced := canonicalKeys(params)
	for _, key := range sortedKeys(defaults) {
		if _, both := forced[canonicalKey(key)]; both {
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
		if unicode.IsSpace(r) {
			return validation.NewError(fmt.Sprintf("%s: parameter name %q contains whitespace", field, key), nil)
		}
	}
	// Matched case-insensitively: "Model" is no upstream's parameter, and
	// some upstreams read keys case-insensitively.
	if _, frozen := frozenKeys[strings.ToLower(key)]; frozen {
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
	case "top_logprobs":
		var v int
		if err = json.Unmarshal(raw, &v); err == nil && v < 0 {
			err = fmt.Errorf("must not be negative")
		}
	case "max_tokens", "max_output_tokens", "max_completion_tokens", "n":
		// No upstream accepts a zero token limit or zero choices.
		var v int
		if err = json.Unmarshal(raw, &v); err == nil && v < 1 {
			err = fmt.Errorf("must be at least 1")
		}
	case "seed":
		var v int64
		err = json.Unmarshal(raw, &v)
	case "stop":
		err = stringOrStrings(raw)
	case "verbosity":
		var v string
		err = json.Unmarshal(raw, &v)
	case "response_format", "text":
		// Both must be objects. response_format applies on chat only (the
		// Responses API skips it unless forced null) and needs a type; text
		// applies on the Responses API as one key, merged one level deep.
		var v map[string]json.RawMessage
		if err = json.Unmarshal(raw, &v); err == nil && key == "response_format" {
			err = validateResponseFormat(v)
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
		var v map[string]json.RawMessage
		if err = json.Unmarshal(raw, &v); err == nil {
			for name, value := range v {
				if len(value) == 0 || value[0] != '"' {
					err = fmt.Errorf("member %q must be a string", name)
					break
				}
			}
		}
	case "include":
		var v []json.RawMessage
		if err = json.Unmarshal(raw, &v); err == nil {
			for i, value := range v {
				if len(value) == 0 || value[0] != '"' {
					err = fmt.Errorf("element %d must be a string", i)
					break
				}
			}
		}
	default:
		return nil
	}
	if err != nil {
		return validation.NewError(fmt.Sprintf("%s: parameter %q has the wrong type: %v", field, key, err), nil)
	}
	return nil
}

// validateResponseFormat checks a response_format object: a string type,
// and for json_schema a json_schema object, so a forced schema cannot be
// skipped at request time for being malformed.
func validateResponseFormat(format map[string]json.RawMessage) error {
	var formatType string
	if json.Unmarshal(format["type"], &formatType) != nil || formatType == "" {
		return fmt.Errorf("must be an object with a string type")
	}
	if formatType != "json_schema" {
		return nil
	}
	var schema map[string]json.RawMessage
	if json.Unmarshal(format["json_schema"], &schema) != nil || schema == nil {
		return fmt.Errorf("json_schema must be an object")
	}
	return nil
}

// stringOrStrings accepts a JSON string or an array of JSON strings. A null
// element would decode into a Go string without complaint, so each element's
// lexeme is checked.
func stringOrStrings(raw json.RawMessage) error {
	if raw[0] == '"' {
		var v string
		return json.Unmarshal(raw, &v)
	}
	var elements []json.RawMessage
	if err := json.Unmarshal(raw, &elements); err != nil {
		return fmt.Errorf("must be a string or an array of strings")
	}
	for i, element := range elements {
		if len(element) == 0 || element[0] != '"' {
			return fmt.Errorf("element %d must be a string", i)
		}
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
