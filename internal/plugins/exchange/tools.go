package exchange

import (
	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/pluginapi"
)

// toolsFromMaps maps request tools (chat nests the definition under
// "function"; Responses keeps it flat) to unified tools.
func toolsFromMaps(tools []map[string]any) []pluginapi.Tool {
	if len(tools) == 0 {
		return nil
	}
	out := make([]pluginapi.Tool, 0, len(tools))
	for _, tool := range tools {
		raw, err := json.Marshal(tool)
		if err != nil {
			continue
		}
		def := tool
		if fn, ok := tool["function"].(map[string]any); ok {
			def = fn
		}
		t := pluginapi.Tool{Raw: raw}
		t.Name, _ = def["name"].(string)
		t.Description, _ = def["description"].(string)
		if params, ok := def["parameters"]; ok && params != nil {
			if encoded, err := json.Marshal(params); err == nil {
				t.Parameters = encoded
			}
		}
		out = append(out, t)
	}
	return out
}
