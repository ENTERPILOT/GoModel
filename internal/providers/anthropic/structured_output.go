package anthropic

import (
	"bytes"
	"log/slog"
	"regexp/syntax"
	"slices"
	"strings"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
)

// jsonObjectInstruction emulates response_format {"type":"json_object"}, which
// Anthropic has no native equivalent for: output_config.format only accepts
// json_schema. Assistant prefill is rejected by every model from the 4.6
// generation onward, so the instruction goes into the system prompt.
// The backtick clause is load-bearing: without it Claude Haiku 4.5 wraps the
// object in a ```json fence, which no OpenAI client expects to have to strip.
const jsonObjectInstruction = "Your entire response must be a single valid JSON object. " +
	`The first character of your response must be "{" and the last must be "}". ` +
	"Do not use backticks or a markdown code block anywhere in the response."

// anthropicOutputFormat is Anthropic's native structured output declaration.
// The object accepts only type and schema; name, description and strict (which
// OpenAI carries on response_format.json_schema) are rejected as extra inputs.
type anthropicOutputFormat struct {
	Type   string         `json:"type"`
	Schema map[string]any `json:"schema"`
}

// openAIResponseFormat is the OpenAI-compatible chat response_format field.
type openAIResponseFormat struct {
	Type       string `json:"type"`
	JSONSchema *struct {
		Name   string         `json:"name"`
		Strict *bool          `json:"strict"`
		Schema map[string]any `json:"schema"`
	} `json:"json_schema"`
}

// applyAnthropicResponseFormat maps the OpenAI-compatible response_format onto
// Anthropic's request. json_schema becomes native structured output
// (output_config.format); json_object becomes a system-prompt instruction.
func applyAnthropicResponseFormat(req *anthropicRequest, extraFields core.UnknownJSONFields) error {
	raw := bytes.TrimSpace(extraFields.Lookup("response_format"))
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil
	}

	var responseFormat openAIResponseFormat
	if err := json.Unmarshal(raw, &responseFormat); err != nil {
		return core.NewInvalidRequestError("response_format must be an object", err)
	}

	switch strings.TrimSpace(responseFormat.Type) {
	case "", "text":
		return nil
	case "json_object":
		req.System = appendAnthropicSystemContent(req.System, jsonObjectInstruction)
		return nil
	case "json_schema":
		if responseFormat.JSONSchema == nil {
			return core.NewInvalidRequestError("response_format.json_schema is required for json_schema", nil)
		}
		schema := responseFormat.JSONSchema.Schema
		if len(schema) == 0 {
			// An absent or empty schema constrains nothing, and Anthropic
			// rejects it; fall back to the json_object instruction so the
			// caller still gets JSON back.
			req.System = appendAnthropicSystemContent(req.System, jsonObjectInstruction)
			return nil
		}
		if req.OutputConfig == nil {
			req.OutputConfig = &anthropicOutputConfig{}
		}
		req.OutputConfig.Format = &anthropicOutputFormat{
			Type:   "json_schema",
			Schema: sanitizeAnthropicSchema(schema),
		}
		return nil
	default:
		return core.NewInvalidRequestError("unsupported response_format type: "+responseFormat.Type, nil)
	}
}

// dropUnsupportedVerbosity discards the OpenAI verbosity hint. Anthropic has no
// equivalent knob, and failing the request would break every client that sets
// it, so the intent is logged and dropped.
func dropUnsupportedVerbosity(extraFields core.UnknownJSONFields) {
	raw := bytes.TrimSpace(extraFields.Lookup("verbosity"))
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return
	}
	verbosity := string(raw)
	var value string
	if err := json.Unmarshal(raw, &value); err == nil {
		verbosity = value
	}
	slog.Warn("dropping verbosity; Anthropic has no equivalent parameter", "verbosity", verbosity)
}

// unsupportedSchemaKeywords are the JSON Schema keywords Anthropic's structured
// output compiler does not honor: most are rejected outright, and the string
// length bounds are documented as unsupported and silently ignored. They are
// validation-only constraints, so dropping them keeps the schema's shape
// intact. "pattern" is absent on purpose — Anthropic does enforce it, and so is
// "minItems", which Anthropic accepts for the values 0 and 1.
var unsupportedSchemaKeywords = map[string]struct{}{
	"$schema":               {},
	"contains":              {},
	"dependencies":          {},
	"dependentRequired":     {},
	"dependentSchemas":      {},
	"else":                  {},
	"exclusiveMaximum":      {},
	"exclusiveMinimum":      {},
	"if":                    {},
	"maxContains":           {},
	"maxItems":              {},
	"maxLength":             {},
	"maxProperties":         {},
	"maximum":               {},
	"minContains":           {},
	"minLength":             {},
	"minProperties":         {},
	"minimum":               {},
	"multipleOf":            {},
	"not":                   {},
	"patternProperties":     {},
	"propertyNames":         {},
	"then":                  {},
	"unevaluatedItems":      {},
	"unevaluatedProperties": {},
	"uniqueItems":           {},
}

// supportedStringFormats are the "format" values Anthropic accepts; any other
// value is rejected, so unknown formats are dropped.
var supportedStringFormats = map[string]struct{}{
	"date":      {},
	"date-time": {},
	"duration":  {},
	"email":     {},
	"hostname":  {},
	"ipv4":      {},
	"ipv6":      {},
	"time":      {},
	"uri":       {},
	"uuid":      {},
}

// schemaSubschemaLists are the keywords whose value is an array of schemas.
var schemaSubschemaLists = []string{"anyOf", "allOf", "prefixItems"}

// schemaSubschemaMaps are the keywords whose value is a map of name to schema.
var schemaSubschemaMaps = []string{"properties", "$defs", "definitions"}

// sanitizeAnthropicSchema adapts a caller's JSON Schema to what Anthropic's
// structured outputs accept: unsupported validation keywords are dropped,
// oneOf is relaxed to anyOf, and every object schema gets the mandatory
// "additionalProperties": false. The input is never mutated, and the walk is
// keyword-aware so a property literally named "minimum" survives.
func sanitizeAnthropicSchema(schema map[string]any) map[string]any {
	out := make(map[string]any, len(schema))
	for key, value := range schema {
		if _, unsupported := unsupportedSchemaKeywords[key]; unsupported {
			continue
		}
		switch key {
		case "oneOf":
			// Anthropic rejects oneOf; anyOf expresses the same set of
			// acceptable shapes for generation. When the caller already sent
			// anyOf there is nowhere to put the branches: Anthropic rejects an
			// allOf sibling of anyOf ("For 'anyOf', 'allOf' is not supported"),
			// so oneOf is dropped and the loss is logged.
			if _, hasAnyOf := schema["anyOf"]; hasAnyOf {
				slog.Warn("dropping response_format oneOf; Anthropic cannot combine it with a sibling anyOf")
				continue
			}
			out["anyOf"] = sanitizeSchemaList(value)
		case "minItems":
			// Anthropic accepts minItems only as 0 or 1; any other value is a
			// 400, so it is dropped like the other array bounds.
			if n, ok := schemaNumber(value); ok && (n == 0 || n == 1) {
				out[key] = value
			}
		case "format":
			if name, ok := value.(string); ok {
				if _, supported := supportedStringFormats[name]; !supported {
					continue
				}
			}
			out[key] = value
		case "pattern":
			if expr, ok := value.(string); ok && !anthropicSupportsPattern(expr) {
				slog.Warn("dropping response_format pattern; Anthropic's regex engine rejects it")
				continue
			}
			out[key] = value
		case "items", "additionalItems":
			out[key] = sanitizeSchemaValue(value)
		default:
			out[key] = value
		}
	}

	for _, key := range schemaSubschemaLists {
		if value, ok := out[key]; ok {
			out[key] = sanitizeSchemaList(value)
		}
	}
	for _, key := range schemaSubschemaMaps {
		if value, ok := out[key].(map[string]any); ok {
			sanitized := make(map[string]any, len(value))
			for name, sub := range value {
				sanitized[name] = sanitizeSchemaValue(sub)
			}
			out[key] = sanitized
		}
	}

	if isObjectSchema(out) {
		out["additionalProperties"] = false
	}
	return out
}

// anthropicSupportsPattern reports whether Anthropic's regex engine can compile
// a "pattern". It rejects backreferences and lookarounds — exactly what RE2
// refuses to parse — and word boundaries, which RE2 does accept, so those are
// checked separately. A pattern it would reject is a 400 before the model is
// even called, so the sanitizer drops it like the other validation-only
// constraints rather than failing the request.
func anthropicSupportsPattern(expr string) bool {
	parsed, err := syntax.Parse(expr, syntax.Perl)
	if err != nil {
		return false
	}
	return !usesWordBoundary(parsed)
}

// usesWordBoundary reports whether the parsed expression asserts \b or \B.
func usesWordBoundary(re *syntax.Regexp) bool {
	if re.Op == syntax.OpWordBoundary || re.Op == syntax.OpNoWordBoundary {
		return true
	}
	return slices.ContainsFunc(re.Sub, usesWordBoundary)
}

// schemaNumber reads a JSON Schema numeric keyword, whichever numeric type the
// decoder produced.
func schemaNumber(value any) (float64, bool) {
	switch n := value.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case json.Number:
		parsed, err := n.Float64()
		return parsed, err == nil
	}
	return 0, false
}

func sanitizeSchemaValue(value any) any {
	if sub, ok := value.(map[string]any); ok {
		return sanitizeAnthropicSchema(sub)
	}
	return value
}

func sanitizeSchemaList(value any) any {
	list, ok := value.([]any)
	if !ok {
		return value
	}
	sanitized := make([]any, 0, len(list))
	for _, item := range list {
		sanitized = append(sanitized, sanitizeSchemaValue(item))
	}
	return sanitized
}

// isObjectSchema reports whether a schema describes an object, either by an
// explicit type (possibly a nullable union) or by carrying properties.
func isObjectSchema(schema map[string]any) bool {
	switch schemaType := schema["type"].(type) {
	case string:
		return schemaType == "object"
	case []any:
		for _, item := range schemaType {
			if name, ok := item.(string); ok && name == "object" {
				return true
			}
		}
		return false
	}
	_, hasProperties := schema["properties"]
	return hasProperties
}
