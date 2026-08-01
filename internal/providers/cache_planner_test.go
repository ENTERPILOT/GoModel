package providers

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
)

func TestCachePlannerAppliesProviderSpecificChatPlanWithoutMutatingCaller(t *testing.T) {
	planner := &cachePlanner{enabled: true}
	prefix := strings.Repeat("stable context ", 1500)
	tests := []struct {
		provider string
		model    string
		field    string
		marker   string
	}{
		{provider: "openai", model: "gpt-5.6", field: "prompt_cache_key"},
		{provider: "anthropic", model: "claude-sonnet-4-5", field: "cache_control"},
		{provider: "bedrock", model: "anthropic.claude-sonnet-4-5", marker: core.GatewayCachePointField},
		{provider: "bedrock-mantle", model: "gpt-5.6", field: "prompt_cache_key"},
		{provider: "gemini", model: "gemini-2.5-pro"},
	}
	for _, tt := range tests {
		t.Run(tt.provider, func(t *testing.T) {
			req := &core.ChatRequest{Model: tt.model, Messages: []core.Message{
				{Role: "system", Content: prefix},
				{Role: "user", Content: "new turn"},
			}}
			planned := planner.planChat(req, tt.provider, core.ModelSelector{Provider: tt.provider + "-primary", Model: tt.model})
			if planned == req {
				t.Fatal("planner returned caller-owned request")
			}
			if tt.field != "" && len(planned.ExtraFields.Lookup(tt.field)) == 0 {
				t.Fatalf("planned request lacks %q", tt.field)
			}
			if tt.marker != "" && len(planned.Messages[0].ExtraFields.Lookup(tt.marker)) == 0 {
				t.Fatalf("stable prefix lacks %q", tt.marker)
			}
			if tt.provider == "gemini" && (planned.PromptCachePlan == nil || planned.PromptCachePlan.Key == "") {
				t.Fatal("Gemini plan lacks an internal cached-content key")
			}
			if promptCacheProfileFor(tt.provider).mode == promptCacheOpenAI {
				parts, ok := planned.Messages[0].Content.([]core.ContentPart)
				if !ok || len(parts) != 1 || len(parts[0].ExtraFields.Lookup("prompt_cache_breakpoint")) == 0 {
					t.Fatalf("OpenAI stable content lacks a breakpoint: %#v", planned.Messages[0].Content)
				}
			}
			if !req.ExtraFields.IsEmpty() || !req.Messages[0].ExtraFields.IsEmpty() {
				t.Fatal("planner mutated caller-owned request")
			}
		})
	}
}

func TestProviderCacheMinimumByModelGeneration(t *testing.T) {
	for _, tt := range []struct {
		provider string
		model    string
		want     int
	}{
		{provider: "anthropic", model: "claude-haiku-4-5-20251001", want: 4096},
		{provider: "anthropic", model: "claude-haiku-3-5-latest", want: 2048},
		{provider: "anthropic", model: "claude-3-haiku-20240307", want: 2048},
		{provider: "anthropic", model: "claude-opus-4-5-20251101", want: 4096},
		{provider: "anthropic", model: "claude-opus-4.6", want: 4096},
		{provider: "anthropic", model: "claude-opus-4-7", want: 2048},
		{provider: "anthropic", model: "claude-sonnet-4-5", want: 1024},
		{provider: "bedrock", model: "anthropic.claude-haiku-4-5-20251001-v1:0", want: 4096},
		{provider: "bedrock", model: "anthropic.claude-sonnet-4-5-20250929-v1:0", want: 4096},
		{provider: "bedrock", model: "anthropic.claude-sonnet-4-6", want: 1024},
		{provider: "bedrock-mantle", model: "openai.gpt-5.6-sol", want: 1024},
	} {
		t.Run(tt.provider+"/"+tt.model, func(t *testing.T) {
			profile := promptCacheProfileFor(tt.provider)
			if got := providerCacheMinimum(profile, tt.model); got != tt.want {
				t.Fatalf("providerCacheMinimum() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestNewCachePlanner_EnvironmentKillSwitch(t *testing.T) {
	for _, tt := range []struct {
		name    string
		value   string
		set     bool
		enabled bool
	}{
		{name: "default on", enabled: true},
		{name: "empty keeps safe default", value: "", set: true, enabled: true},
		{name: "explicit on", value: "true", set: true, enabled: true},
		{name: "explicit off", value: "false", set: true, enabled: false},
		{name: "invalid keeps safe default", value: "sometimes", set: true, enabled: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			old, existed := os.LookupEnv(providerPromptCachePlannerEnabledEnv)
			t.Cleanup(func() {
				if existed {
					_ = os.Setenv(providerPromptCachePlannerEnabledEnv, old)
				} else {
					_ = os.Unsetenv(providerPromptCachePlannerEnabledEnv)
				}
			})
			if tt.set {
				_ = os.Setenv(providerPromptCachePlannerEnabledEnv, tt.value)
			} else {
				_ = os.Unsetenv(providerPromptCachePlannerEnabledEnv)
			}
			if got := newCachePlanner().enabled; got != tt.enabled {
				t.Fatalf("newCachePlanner() enabled = %v, want %v", got, tt.enabled)
			}
		})
	}
}

func TestCachePlannerHonorsMinimumAndClientDirective(t *testing.T) {
	planner := &cachePlanner{enabled: true}
	short := &core.ChatRequest{Messages: []core.Message{{Role: "system", Content: "short"}, {Role: "user", Content: "turn"}}}
	if got := planner.planChat(short, "openai", core.ModelSelector{Model: "gpt-5.6"}); got != short {
		t.Fatal("planned prefix below provider minimum")
	}

	directed := &core.ChatRequest{
		Messages:    []core.Message{{Role: "system", Content: strings.Repeat("x", 9000)}, {Role: "user", Content: "turn"}},
		ExtraFields: core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{"prompt_cache_key": json.RawMessage(`"client"`)}),
	}
	if got := planner.planChat(directed, "openai", core.ModelSelector{Model: "gpt-5.6"}); got != directed {
		t.Fatal("overrode client cache directive")
	}
}

func TestCachePlannerResponsesShapesAndCallerOwnership(t *testing.T) {
	planner := &cachePlanner{enabled: true}
	prefix := strings.Repeat("stable response context ", 1200)
	shapes := []struct {
		name    string
		content any
	}{
		{name: "string", content: prefix},
		{name: "typed parts", content: []core.ContentPart{{Type: "input_text", Text: prefix}}},
		{name: "generic parts", content: []any{map[string]any{"type": "input_text", "text": prefix}}},
	}
	for _, shape := range shapes {
		t.Run(shape.name, func(t *testing.T) {
			req := &core.ResponsesRequest{Model: "gpt-5.6", Input: []core.ResponsesInputElement{
				{Role: "user", Content: shape.content},
				{Role: "user", Content: "dynamic"},
			}}
			before, err := json.Marshal(req)
			if err != nil {
				t.Fatal(err)
			}
			planned := planner.planResponses(req, "openai", core.ModelSelector{Provider: "openai-primary", Model: "gpt-5.6"})
			if planned == req || len(planned.ExtraFields.Lookup("prompt_cache_key")) == 0 ||
				len(planned.ExtraFields.Lookup("prompt_cache_options")) == 0 {
				t.Fatalf("Responses plan missing cache fields: %+v", planned)
			}
			plannedJSON, err := json.Marshal(planned)
			if err != nil || !bytes.Contains(plannedJSON, []byte(`"prompt_cache_breakpoint"`)) {
				t.Fatalf("Responses plan lacks explicit breakpoint: %s (err=%v)", plannedJSON, err)
			}
			after, err := json.Marshal(req)
			if err != nil {
				t.Fatal(err)
			}
			if string(before) != string(after) {
				t.Fatalf("planner mutated caller: before=%s after=%s", before, after)
			}
		})
	}
	anthropic := &core.ResponsesRequest{Input: []core.ResponsesInputElement{
		{Role: "user", Content: prefix}, {Role: "user", Content: "dynamic"},
	}}
	if got := planner.planResponses(anthropic, "anthropic", core.ModelSelector{Model: "claude-sonnet-4-5"}); got == anthropic || len(got.ExtraFields.Lookup("cache_control")) == 0 {
		t.Fatal("Anthropic Responses plan lacks cache_control")
	}
	short := &core.ResponsesRequest{Input: []core.ResponsesInputElement{
		{Role: "user", Content: "short"}, {Role: "user", Content: "dynamic"},
	}}
	if got := planner.planResponses(short, "openai", core.ModelSelector{Model: "gpt-5.6"}); got != short {
		t.Fatal("planned a Responses prefix below the provider minimum")
	}
}

func TestCachePlannerFindsNestedClientDirective(t *testing.T) {
	prefix := strings.Repeat("x", 9000)
	req := &core.ChatRequest{Messages: []core.Message{
		{Role: "system", Content: []core.ContentPart{{
			Type: "text", Text: prefix,
			ExtraFields: core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{
				"cache_control": json.RawMessage(`{"type":"ephemeral"}`),
			}),
		}}},
		{Role: "user", Content: "turn"},
	}}
	if got := (&cachePlanner{enabled: true}).planChat(req, "openai", core.ModelSelector{Model: "gpt-5.6"}); got != req {
		t.Fatal("planner overrode a nested client cache directive")
	}
}

func TestCachePlannerProviderCapabilityBoundaries(t *testing.T) {
	req := &core.ChatRequest{Messages: []core.Message{
		{Role: "system", Content: strings.Repeat("x", 20000)},
		{Role: "user", Content: "turn"},
	}}
	planner := &cachePlanner{enabled: true}
	for _, provider := range []string{"openrouter", "vertex", "unknown"} {
		if got := planner.planChat(req, provider, core.ModelSelector{Model: "gemini-2.5-pro"}); got != req {
			t.Fatalf("provider %q unexpectedly received an automatic plan", provider)
		}
	}
}

func TestCloneChatRequestPreservesInternalCachePlan(t *testing.T) {
	req := &core.ChatRequest{PromptCachePlan: &core.PromptCachePlan{Key: "stable"}}
	clone, ok := cloneChatRequest(req)
	if !ok || clone.PromptCachePlan == nil || clone.PromptCachePlan.Key != "stable" {
		t.Fatalf("clone lost internal cache metadata: %+v", clone)
	}
	if clone.PromptCachePlan == req.PromptCachePlan {
		t.Fatal("clone aliases internal cache metadata")
	}
}

func TestGeminiPlanKeyIncludesEntireNativePrefixAndBoundary(t *testing.T) {
	planner := &cachePlanner{enabled: true}
	makeRequest := func(system, boundary, toolName string) *core.ChatRequest {
		return &core.ChatRequest{
			Model: "gemini-2.5-pro",
			Messages: []core.Message{
				{Role: "system", Content: strings.Repeat(system, 5000)},
				{Role: "user", Content: boundary},
				{Role: "user", Content: "live"},
			},
			Tools: []map[string]any{{"type": "function", "function": map[string]any{"name": toolName}}},
		}
	}
	keys := make(map[string]struct{})
	for _, req := range []*core.ChatRequest{
		makeRequest("system-a", "boundary-a", "lookup"),
		makeRequest("system-b", "boundary-a", "lookup"),
		makeRequest("system-a", "boundary-b", "lookup"),
		makeRequest("system-a", "boundary-a", "search"),
	} {
		planned := planner.planChat(req, "gemini", core.ModelSelector{Provider: "gemini-primary", Model: req.Model})
		if planned.PromptCachePlan == nil || planned.PromptCachePlan.Key == "" {
			t.Fatal("Gemini request was not planned")
		}
		keys[planned.PromptCachePlan.Key] = struct{}{}
	}
	if len(keys) != 4 {
		t.Fatalf("system, boundary, or tools were omitted from Gemini keys: %v", keys)
	}
}
