package opencodego

import (
	"context"
	"fmt"
	"testing"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
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
			var got map[string]any
			server := captureBody(t, &got)
			defer server.Close()

			var req core.ChatRequest
			if err := json.Unmarshal(fmt.Appendf(nil, `{
				"model":%q,
				"messages":[
					{"role":"user","content":"hi"},
					{"role":"assistant","content":"hello"},
					{"role":"user","content":"weather?"}
				],
				"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object"}}}],
				"response_format":{"type":"json_schema","json_schema":{"name":"weather","schema":{"type":"object"}}}
			}`, tt.model), &req); err != nil {
				t.Fatalf("json.Unmarshal() error = %v", err)
			}

			if _, err := newTestProvider(server.URL, server.Client()).ChatCompletion(context.Background(), &req); err != nil {
				t.Fatalf("ChatCompletion() error = %v", err)
			}

			format, _ := got["response_format"].(map[string]any)
			messages, _ := got["messages"].([]any)
			var assistant map[string]any
			for _, raw := range messages {
				if message, _ := raw.(map[string]any); message["role"] == "assistant" {
					assistant = message
				}
			}
			first, _ := messages[0].(map[string]any)

			if tt.wantAdapted {
				if format["type"] != "json_object" {
					t.Errorf("response_format = %#v, want json_object", format)
				}
				if first["role"] != "system" {
					t.Errorf("messages[0] = %#v, want schema instruction", first)
				}
				if assistant["reasoning_content"] != " " {
					t.Errorf("assistant reasoning_content = %#v, want one space", assistant["reasoning_content"])
				}
				return
			}
			if format["type"] != "json_schema" {
				t.Errorf("response_format = %#v, want json_schema forwarded", format)
			}
			if len(messages) != 3 {
				t.Errorf("messages = %#v, want the client messages only", messages)
			}
			if _, ok := assistant["reasoning_content"]; ok {
				t.Errorf("assistant = %#v, want no reasoning_content", assistant)
			}
		})
	}
}
