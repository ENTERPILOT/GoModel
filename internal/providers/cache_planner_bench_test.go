package providers

import (
	"fmt"
	"strings"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
)

// benchChatRequest builds a multi-turn conversation of ~1.8KB messages, the
// shape of agentic traffic the planner sees on every OpenAI-family request.
func benchChatRequest(messages int, withTools bool) *core.ChatRequest {
	text := strings.Repeat("The quick brown fox jumps over the lazy dog. ", 40)
	req := &core.ChatRequest{Model: "gpt-4o"}
	for i := range messages {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		req.Messages = append(req.Messages, core.Message{Role: role, Content: text})
	}
	if withTools {
		req.Tools = []map[string]any{{
			"type": "function",
			"function": map[string]any{
				"name":       "search",
				"parameters": map[string]any{"type": "object"},
			},
		}}
	}
	return req
}

// BenchmarkPlanChat measures the per-request cost of prompt-cache planning for
// an eligible OpenAI chat request (prefix above the cache minimum, no client
// directives), which is the common case for long conversations.
func BenchmarkPlanChat(b *testing.B) {
	planner := &cachePlanner{enabled: true}
	selector := core.ModelSelector{Provider: "openai", Model: "gpt-4o"}
	for _, messages := range []int{20, 60} {
		for _, withTools := range []bool{false, true} {
			req := benchChatRequest(messages, withTools)
			b.Run(fmt.Sprintf("messages=%d/tools=%v", messages, withTools), func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					if planned := planner.planChat(req, "openai", selector); planned == req {
						b.Fatal("request was not planned")
					}
				}
			})
		}
	}
}
