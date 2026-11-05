package core

import (
	"fmt"
	"maps"

	"github.com/goccy/go-json"
)

// ResponsesTextFormat maps a Chat Completions response_format onto the
// Responses API's text.format, which carries the json_schema members flat
// instead of nested under json_schema.
func ResponsesTextFormat(raw json.RawMessage) (map[string]any, error) {
	var format struct {
		Type       string         `json:"type"`
		JSONSchema map[string]any `json:"json_schema"`
	}
	if err := json.Unmarshal(raw, &format); err != nil {
		return nil, fmt.Errorf("response_format: %w", err)
	}
	switch format.Type {
	case "text", "json_object":
		return map[string]any{"type": format.Type}, nil
	case "json_schema":
		// The discriminator goes last, so a member of the schema object
		// named type cannot replace it.
		out := maps.Clone(format.JSONSchema)
		if out == nil {
			out = map[string]any{}
		}
		out["type"] = "json_schema"
		return out, nil
	default:
		return nil, fmt.Errorf("response_format: unsupported type %q", format.Type)
	}
}
