package providers

import (
	"strings"
	"testing"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
)

// TestChatPlanAppliesAgreesWithPlanChat pins the fast-path predicate to the
// planner: for every fixture, chatPlanApplies must be true exactly when
// planChat returns a planned copy instead of the caller's request.
func TestChatPlanAppliesAgreesWithPlanChat(t *testing.T) {
	long := strings.Repeat("stable context ", 1500)
	textParts := func(text string) []core.ContentPart {
		return []core.ContentPart{{Type: "text", Text: text}}
	}
	tools := []map[string]any{{"type": "function", "function": map[string]any{"name": "search", "parameters": map[string]any{"type": "object"}}}}
	tests := []struct {
		name     string
		planner  *cachePlanner
		provider string
		model    string
		req      *core.ChatRequest
	}{
		{name: "nil planner", provider: "openai", model: "gpt-5.6", req: &core.ChatRequest{Messages: []core.Message{{Role: "system", Content: long}, {Role: "user", Content: "turn"}}}},
		{name: "planner disabled", planner: &cachePlanner{}, provider: "openai", model: "gpt-5.6", req: &core.ChatRequest{Messages: []core.Message{{Role: "system", Content: long}, {Role: "user", Content: "turn"}}}},
		{name: "single message", planner: &cachePlanner{enabled: true}, provider: "openai", model: "gpt-5.6", req: &core.ChatRequest{Messages: []core.Message{{Role: "user", Content: long}}}},
		{name: "unsupported provider", planner: &cachePlanner{enabled: true}, provider: "groq", model: "llama", req: &core.ChatRequest{Messages: []core.Message{{Role: "system", Content: long}, {Role: "user", Content: "turn"}}}},
		{name: "text prefix below minimum", planner: &cachePlanner{enabled: true}, provider: "openai", model: "gpt-5.6", req: &core.ChatRequest{Messages: []core.Message{{Role: "system", Content: "short"}, {Role: "user", Content: "turn"}}}},
		{name: "text prefix above minimum", planner: &cachePlanner{enabled: true}, provider: "openai", model: "gpt-5.6", req: &core.ChatRequest{Messages: []core.Message{{Role: "system", Content: long}, {Role: "user", Content: "turn"}}}},
		{name: "text parts above minimum", planner: &cachePlanner{enabled: true}, provider: "anthropic", model: "claude-sonnet-4-5", req: &core.ChatRequest{Messages: []core.Message{{Role: "system", Content: textParts(long)}, {Role: "user", Content: "turn"}}}},
		{name: "client directive present", planner: &cachePlanner{enabled: true}, provider: "openai", model: "gpt-5.6", req: &core.ChatRequest{
			Messages:    []core.Message{{Role: "system", Content: long}, {Role: "user", Content: "turn"}},
			ExtraFields: core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{"prompt_cache_key": json.RawMessage(`"client"`)}),
		}},
		{name: "tools inconclusive below minimum", planner: &cachePlanner{enabled: true}, provider: "openai", model: "gpt-5.6", req: &core.ChatRequest{Tools: tools, Messages: []core.Message{{Role: "system", Content: "short"}, {Role: "user", Content: "turn"}}}},
		{name: "tools inconclusive above minimum", planner: &cachePlanner{enabled: true}, provider: "openai", model: "gpt-5.6", req: &core.ChatRequest{Tools: tools, Messages: []core.Message{{Role: "system", Content: long}, {Role: "user", Content: "turn"}}}},
		{name: "image part inconclusive below minimum", planner: &cachePlanner{enabled: true}, provider: "openai", model: "gpt-5.6", req: &core.ChatRequest{Messages: []core.Message{
			{Role: "user", Content: []core.ContentPart{{Type: "image_url"}}},
			{Role: "user", Content: "turn"},
		}}},
		{name: "tool calls inconclusive above minimum", planner: &cachePlanner{enabled: true}, provider: "bedrock", model: "anthropic.claude-sonnet-4-5", req: &core.ChatRequest{Messages: []core.Message{
			{Role: "system", Content: long},
			{Role: "assistant", ToolCalls: []core.ToolCall{{ID: "call_1", Type: "function"}}},
			{Role: "user", Content: "turn"},
		}}},
		{name: "gemini above minimum", planner: &cachePlanner{enabled: true}, provider: "gemini", model: "gemini-2.5-pro", req: &core.ChatRequest{Messages: []core.Message{{Role: "system", Content: long}, {Role: "user", Content: "turn"}}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			selector := core.ModelSelector{Provider: tt.provider + "-primary", Model: tt.model}
			wantApplies := tt.planner.planChat(tt.req, tt.provider, selector) != tt.req
			if got := tt.planner.chatPlanApplies(tt.req, tt.provider, selector); got != wantApplies {
				t.Fatalf("chatPlanApplies = %v, planChat applied = %v", got, wantApplies)
			}
		})
	}
}

func TestRouterPromptCachePlanAppliesWithoutPlanner(t *testing.T) {
	router := &Router{}
	req := &core.ChatRequest{Messages: []core.Message{{Role: "system", Content: strings.Repeat("x", 9000)}, {Role: "user", Content: "turn"}}}
	if router.PromptCachePlanApplies("openai", core.ModelSelector{Model: "gpt-5.6"}, req) {
		t.Fatal("router without a planner reported an applicable plan")
	}
	router.cachePlanner = &cachePlanner{enabled: true}
	if !router.PromptCachePlanApplies("openai", core.ModelSelector{Model: "gpt-5.6"}, req) {
		t.Fatal("router with a planner did not report an applicable plan")
	}
}
