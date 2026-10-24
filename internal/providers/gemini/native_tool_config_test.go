package gemini

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
)

func TestGeminiToolConfigFromOpenAI(t *testing.T) {
	allowedTools := func(mode string, names ...string) map[string]any {
		tools := make([]any, 0, len(names))
		for _, name := range names {
			tools = append(tools, map[string]any{"type": "function", "function": map[string]any{"name": name}})
		}
		return map[string]any{
			"type":          "allowed_tools",
			"allowed_tools": map[string]any{"mode": mode, "tools": tools},
		}
	}

	tests := []struct {
		name        string
		choice      any
		strict      bool
		wantMode    string
		wantAllowed []string
	}{
		{name: "unset", choice: nil},
		{name: "auto", choice: "auto", wantMode: "AUTO"},
		{name: "required", choice: "required", wantMode: "ANY"},
		{name: "none", choice: "none", wantMode: "NONE"},
		{
			name:        "named function",
			choice:      map[string]any{"type": "function", "function": map[string]any{"name": "tool_a"}},
			wantMode:    "ANY",
			wantAllowed: []string{"tool_a"},
		},
		{
			name:        "allowed_tools required",
			choice:      allowedTools("required", "tool_a", "tool_b"),
			wantMode:    "ANY",
			wantAllowed: []string{"tool_a", "tool_b"},
		},
		{
			// Gemini rejects allowedFunctionNames with AUTO.
			name:        "allowed_tools auto",
			choice:      allowedTools("auto", "tool_b"),
			wantMode:    "VALIDATED",
			wantAllowed: []string{"tool_b"},
		},
		{name: "strict unset", choice: nil, strict: true, wantMode: "VALIDATED"},
		{name: "strict auto", choice: "auto", strict: true, wantMode: "VALIDATED"},
		{name: "strict required", choice: "required", strict: true, wantMode: "ANY"},
		{name: "strict none", choice: "none", strict: true, wantMode: "NONE"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := geminiToolConfigFromOpenAI(tt.choice, tt.strict)
			require.NoError(t, err)
			if tt.wantMode == "" {
				assert.Nil(t, got)
				return
			}
			require.NotNil(t, got)
			assert.Equal(t, tt.wantMode, got.FunctionCallingConfig.Mode)
			assert.Equal(t, tt.wantAllowed, got.FunctionCallingConfig.AllowedFunctionNames)
		})
	}
}

func TestGeminiToolConfigFromOpenAIRejectsEmptyAllowedTools(t *testing.T) {
	for _, mode := range []string{"auto", "required"} {
		t.Run(mode, func(t *testing.T) {
			_, err := geminiToolConfigFromOpenAI(map[string]any{
				"type":          "allowed_tools",
				"allowed_tools": map[string]any{"mode": mode, "tools": []any{}},
			}, false)
			var gatewayErr *core.GatewayError
			require.ErrorAs(t, err, &gatewayErr)
			assert.Equal(t, core.ErrorTypeInvalidRequest, gatewayErr.Type)
		})
	}
}

func TestConvertChatRequestToGeminiStrictToolUsesValidatedMode(t *testing.T) {
	out, err := convertChatRequestToGemini(&core.ChatRequest{
		Model:    "gemini-2.5-flash",
		Messages: []core.Message{{Role: "user", Content: "hi"}},
		Tools: []map[string]any{
			{"type": "function", "function": map[string]any{"name": "tool_a"}},
			{"type": "function", "function": map[string]any{"name": "tool_b", "strict": true}},
		},
	})
	require.NoError(t, err)
	require.NotNil(t, out.ToolConfig)
	assert.Equal(t, "VALIDATED", out.ToolConfig.FunctionCallingConfig.Mode)
}
