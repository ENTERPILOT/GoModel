package providers

import (
	"strings"
	"testing"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
)

func TestCachePlannerAppliesProviderSpecificChatPlanWithoutMutatingCaller(t *testing.T) {
	prefix := strings.Repeat("stable context ", 1500)
	tests := []struct {
		provider string
		model    string
		field    string
		marker   string
	}{
		{provider: "openai", model: "gpt-5.6", field: "prompt_cache_key"},
		{provider: "anthropic", model: "claude-sonnet-4-5", field: "cache_control"},
		{provider: "bedrock", model: "anthropic.claude-sonnet-4-5", marker: cachePointField},
		{provider: "gemini", model: "gemini-2.5-pro"},
	}
	for _, tt := range tests {
		t.Run(tt.provider, func(t *testing.T) {
			req := &core.ChatRequest{Model: tt.model, Messages: []core.Message{
				{Role: "system", Content: prefix},
				{Role: "user", Content: "new turn"},
			}}
			planned := newCachePlanner().planChat(req, tt.provider, core.ModelSelector{Provider: tt.provider + "-primary", Model: tt.model})
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
			if tt.provider == "openai" {
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

func TestNewCachePlanner_EnvironmentKillSwitch(t *testing.T) {
	for _, tt := range []struct {
		name    string
		value   string
		enabled bool
	}{
		{name: "default on", enabled: true},
		{name: "explicit on", value: "true", enabled: true},
		{name: "explicit off", value: "false", enabled: false},
		{name: "invalid keeps safe default", value: "sometimes", enabled: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(providerPromptCachePlannerEnabledEnv, tt.value)
			if got := newCachePlanner() != nil; got != tt.enabled {
				t.Fatalf("newCachePlanner() enabled = %v, want %v", got, tt.enabled)
			}
		})
	}
}

func TestCachePlannerHonorsMinimumAndClientDirective(t *testing.T) {
	planner := newCachePlanner()
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
