package opencodego

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
)

func TestChatCompletion_AppliesDeepSeekCompatibilityToDeepSeekModels(t *testing.T) {
	tests := []struct {
		name        string
		model       string
		wantAdapted bool
	}{
		{name: "deepseek model", model: "deepseek-v4.1-flash", wantAdapted: true},
		{name: "prefixed deepseek model", model: "opencode_go/deepseek-v4.1-flash", wantAdapted: true},
		{name: "other model", model: "glm-5.1", wantAdapted: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, capture := providertest.JSONServer(t, http.StatusOK, providertest.ChatCompletionJSON)

			var req core.ChatRequest
			require.NoError(t, json.Unmarshal(fmt.Appendf(nil, `{
				"model":%q,
				"messages":[
					{"role":"user","content":"hi"},
					{"role":"assistant","content":"hello"},
					{"role":"user","content":"weather?"}
				],
				"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object"}}}],
				"response_format":{"type":"json_schema","json_schema":{"name":"weather","schema":{"type":"object"}}}
			}`, tt.model), &req))

			_, err := newTestProvider(server.URL, server.Client()).ChatCompletion(context.Background(), &req)
			require.NoError(t, err)

			got := capture.Last(t).JSON(t)
			format, _ := got["response_format"].(map[string]any)
			messages, _ := got["messages"].([]any)
			require.NotEmpty(t, messages)
			var assistant map[string]any
			for _, raw := range messages {
				if message, _ := raw.(map[string]any); message["role"] == "assistant" {
					assistant = message
				}
			}
			first, _ := messages[0].(map[string]any)

			if tt.wantAdapted {
				assert.Equal(t, "json_object", format["type"], "response_format = %#v", format)
				assert.Equal(t, "system", first["role"], "messages[0] = %#v, want schema instruction", first)
				assert.Equal(t, " ", assistant["reasoning_content"], "assistant reasoning_content should be one space")
				return
			}
			assert.Equal(t, "json_schema", format["type"], "response_format = %#v, want json_schema forwarded", format)
			assert.Len(t, messages, 3, "want the client messages only")
			assert.NotContains(t, assistant, "reasoning_content")
		})
	}
}
