package anthropicapi

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
)

// applyOutputConfig carries output_config onto the canonical request: its
// effort sets the reasoning effort, taking precedence over the one derived
// from thinking, and its format (or the deprecated top-level output_format)
// becomes a strict json_schema response_format. A format the canonical request
// cannot express is rejected rather than dropped, so the caller never gets
// prose where it asked for JSON.
func applyOutputConfig(req *MessagesRequest, chat *core.ChatRequest) error {
	format := req.OutputFormat
	if req.OutputConfig != nil {
		if effort := strings.TrimSpace(req.OutputConfig.Effort); effort != "" {
			chat.Reasoning = &core.Reasoning{Effort: effort}
		}
		if len(bytes.TrimSpace(req.OutputConfig.Format)) > 0 {
			format = req.OutputConfig.Format
		}
	}
	format = bytes.TrimSpace(format)
	if len(format) == 0 || core.IsJSONNull(format) {
		return nil
	}
	responseFormat, err := responseFormatFromOutputFormat(format)
	if err != nil {
		return core.NewInvalidRequestError(err.Error(), err).WithParam("output_config.format")
	}
	extra, err := core.MergeUnknownJSONFields(chat.ExtraFields, map[string]json.RawMessage{"response_format": responseFormat})
	if err != nil {
		return core.NewInvalidRequestError("output_config.format: "+err.Error(), err)
	}
	chat.ExtraFields = extra
	return nil
}

// responseFormatFromOutputFormat maps an Anthropic output format onto the
// OpenAI-compatible response_format. It is strict when the schema meets
// OpenAI's strict-mode rules, which reject other schemas outright; otherwise
// the schema still guides the output without the guarantee.
func responseFormatFromOutputFormat(raw json.RawMessage) (json.RawMessage, error) {
	var format struct {
		Type   string          `json:"type"`
		Schema json.RawMessage `json:"schema"`
	}
	if err := json.Unmarshal(raw, &format); err != nil {
		return nil, fmt.Errorf("output_config.format must be an object: %v", err)
	}
	schema := bytes.TrimSpace(format.Schema)
	if format.Type != "json_schema" || len(schema) == 0 || schema[0] != '{' {
		return nil, fmt.Errorf(`output_config.format must be {"type":"json_schema","schema":{...}}`)
	}
	var decoded any
	if err := json.Unmarshal(schema, &decoded); err != nil {
		return nil, fmt.Errorf("output_config.format.schema: %v", err)
	}
	return json.Marshal(map[string]any{
		"type": "json_schema",
		"json_schema": map[string]any{
			"name":   "output",
			"strict": strictCompatible(decoded),
			"schema": json.RawMessage(schema),
		},
	})
}

// strictCompatible reports whether a JSON schema meets OpenAI's strict-mode
// rules: every object sets additionalProperties to false and lists all of its
// properties as required, throughout nested schemas.
func strictCompatible(schema any) bool {
	switch node := schema.(type) {
	case []any:
		for _, item := range node {
			if !strictCompatible(item) {
				return false
			}
		}
		return true
	case map[string]any:
		properties, hasProperties := node["properties"].(map[string]any)
		if node["type"] == "object" || hasProperties {
			if additional, ok := node["additionalProperties"].(bool); !ok || additional {
				return false
			}
			required, _ := node["required"].([]any)
			listed := make(map[string]bool, len(required))
			for _, name := range required {
				// Only names count; a malformed entry (an object, an array)
				// must not be used as a map key.
				if name, ok := name.(string); ok {
					listed[name] = true
				}
			}
			for name, property := range properties {
				if !listed[name] || !strictCompatible(property) {
					return false
				}
			}
		}
		for _, key := range []string{"items", "anyOf", "oneOf", "allOf", "$defs", "definitions"} {
			child, ok := node[key]
			if !ok {
				continue
			}
			if defs, isMap := child.(map[string]any); isMap && (key == "$defs" || key == "definitions") {
				for _, def := range defs {
					if !strictCompatible(def) {
						return false
					}
				}
				continue
			}
			if !strictCompatible(child) {
				return false
			}
		}
		return true
	default:
		return true
	}
}
