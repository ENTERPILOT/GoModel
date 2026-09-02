package providers

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
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

func TestCachePlannerSkipsUnsupportedResponsesModesBeforeCloning(t *testing.T) {
	req := &core.ResponsesRequest{Input: []core.ResponsesInputElement{
		{Role: "user", Content: strings.Repeat("x", 20000)},
		{Role: "user", Content: "turn"},
	}}
	planner := &cachePlanner{enabled: true}
	for _, provider := range []string{"bedrock", "gemini", "openrouter", "unknown"} {
		if got := planner.planResponses(req, provider, core.ModelSelector{Model: "model"}); got != req {
			t.Fatalf("provider %q unexpectedly received a Responses plan", provider)
		}
	}
}

func TestCloneChatRequestPreservesInternalCachePlan(t *testing.T) {
	req := &core.ChatRequest{PromptCachePlan: &core.PromptCachePlan{Key: "stable"}}
	clone := cloneChatRequest(req)
	if clone.PromptCachePlan == nil || clone.PromptCachePlan.Key != "stable" {
		t.Fatalf("clone lost internal cache metadata: %+v", clone)
	}
	if clone.PromptCachePlan == req.PromptCachePlan {
		t.Fatal("clone aliases internal cache metadata")
	}
}

// The planner clones shallowly, so every write it performs must land on a
// copied slice element or map rather than on memory the caller still holds.
func TestCachePlannerDoesNotAliasCallerContentOrExtras(t *testing.T) {
	planner := &cachePlanner{enabled: true}
	prefix := strings.Repeat("stable context ", 1500)

	t.Run("chat parts and message extras", func(t *testing.T) {
		parts := []core.ContentPart{{Type: "text", Text: prefix}}
		req := &core.ChatRequest{
			Model:       "gpt-5.6",
			Messages:    []core.Message{{Role: "system", Content: parts}, {Role: "user", Content: "new turn"}},
			ExtraFields: core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{"seed": json.RawMessage(`1`)}),
		}
		before, err := json.Marshal(req)
		if err != nil {
			t.Fatal(err)
		}
		planned := planner.planChat(req, "openai", core.ModelSelector{Provider: "openai-primary", Model: "gpt-5.6"})
		if planned == req {
			t.Fatal("planner returned caller-owned request")
		}
		plannedParts, ok := planned.Messages[0].Content.([]core.ContentPart)
		if !ok || len(plannedParts) != 1 || len(plannedParts[0].ExtraFields.Lookup("prompt_cache_breakpoint")) == 0 {
			t.Fatalf("planned content lacks breakpoint: %#v", planned.Messages[0].Content)
		}
		if &plannedParts[0] == &parts[0] || !parts[0].ExtraFields.IsEmpty() {
			t.Fatal("planner wrote into the caller's content parts")
		}
		if &planned.Messages[0] == &req.Messages[0] {
			t.Fatal("planner aliases the caller's messages slice")
		}
		if len(req.ExtraFields.Lookup("prompt_cache_key")) != 0 || len(req.ExtraFields.Lookup("seed")) == 0 {
			t.Fatal("planner rewrote the caller's extra fields")
		}
		after, err := json.Marshal(req)
		if err != nil {
			t.Fatal(err)
		}
		if string(before) != string(after) {
			t.Fatalf("planner mutated caller: before=%s after=%s", before, after)
		}
		bedrock := planner.planChat(req, "bedrock", core.ModelSelector{Provider: "bedrock-primary", Model: "anthropic.claude-sonnet-4-5"})
		if len(bedrock.Messages[0].ExtraFields.Lookup(core.GatewayCachePointField)) == 0 || !req.Messages[0].ExtraFields.IsEmpty() {
			t.Fatal("bedrock marker leaked into caller message")
		}
	})

	t.Run("responses generic blocks", func(t *testing.T) {
		block := map[string]any{"type": "input_text", "text": prefix}
		blocks := []any{block}
		req := &core.ResponsesRequest{Model: "gpt-5.6", Input: []core.ResponsesInputElement{
			{Role: "user", Content: blocks},
			{Role: "user", Content: "dynamic"},
		}}
		planned := planner.planResponses(req, "openai", core.ModelSelector{Provider: "openai-primary", Model: "gpt-5.6"})
		items, ok := planned.Input.([]core.ResponsesInputElement)
		if !ok || planned == req {
			t.Fatalf("unexpected plan: %+v", planned)
		}
		plannedBlocks, ok := items[0].Content.([]any)
		if !ok || len(plannedBlocks) != 1 {
			t.Fatalf("unexpected planned content: %#v", items[0].Content)
		}
		marked, _ := plannedBlocks[0].(map[string]any)
		if _, exists := marked["prompt_cache_breakpoint"]; !exists {
			t.Fatal("planned block lacks breakpoint")
		}
		if _, leaked := block["prompt_cache_breakpoint"]; leaked {
			t.Fatal("planner wrote into the caller's block map")
		}
		if &plannedBlocks[0] == &blocks[0] {
			t.Fatal("planner aliases the caller's block slice")
		}
		original := req.Input.([]core.ResponsesInputElement)
		if &items[0] == &original[0] {
			t.Fatal("planner aliases the caller's input slice")
		}
	})

	t.Run("responses typed map blocks", func(t *testing.T) {
		block := map[string]any{"type": "input_text", "text": prefix}
		req := &core.ResponsesRequest{Model: "gpt-5.6", Input: []core.ResponsesInputElement{
			{Role: "user", Content: []map[string]any{block}},
			{Role: "user", Content: "dynamic"},
		}}
		planned := planner.planResponses(req, "openai", core.ModelSelector{Provider: "openai-primary", Model: "gpt-5.6"})
		body, err := json.Marshal(planned)
		if err != nil || !bytes.Contains(body, []byte(`"prompt_cache_breakpoint"`)) {
			t.Fatalf("plan lacks breakpoint: %s (err=%v)", body, err)
		}
		if _, leaked := block["prompt_cache_breakpoint"]; leaked {
			t.Fatal("planner wrote into the caller's block map")
		}
	})
}

// legacyCacheAffinityKey is the pre-streaming key derivation: sha256 over the
// header fields followed by the fully marshaled prefix object. The streaming
// digest must produce the same key so deployed cache affinity survives upgrades.
func legacyCacheAffinityKey(t *testing.T, providerType string, selector core.ModelSelector, user string, prefix any) (string, int) {
	t.Helper()
	body, err := json.Marshal(prefix)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.New()
	for _, part := range []string{normalizedProviderType(providerType), selector.Provider, selector.Model, user} {
		hash.Write([]byte(part))
		hash.Write([]byte{0})
	}
	hash.Write(body)
	return "gomodel-" + hex.EncodeToString(hash.Sum(nil)[:16]), (len(body) + 3) / 4
}

func TestPrefixDigestMatchesLegacyMarshaledPrefix(t *testing.T) {
	selector := core.ModelSelector{Provider: "openai-primary", Model: "gpt-5.6"}
	tools := []map[string]any{{"type": "function", "function": map[string]any{"name": "lookup", "description": "a <b> & c"}}}
	messages := []core.Message{
		{Role: "system", Content: "line one\nline \"two\" <html> & more"},
		{Role: "user", Content: []core.ContentPart{{Type: "text", Text: "part"}, {Type: "image_url", ImageURL: &core.ImageURLContent{URL: "https://x/y?a=1&b=2"}}}},
		{Role: "assistant", ToolCalls: []core.ToolCall{{ID: "call_1", Type: "function", Function: core.FunctionCall{Name: "lookup", Arguments: `{"q":"x"}`}}},
			ExtraFields: core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{"name": json.RawMessage(`"bot"`)})},
		{Role: "tool", ToolCallID: "call_1", Content: "result"},
	}

	t.Run("chat", func(t *testing.T) {
		for _, withTools := range []bool{false, true} {
			var reqTools []map[string]any
			if withTools {
				reqTools = tools
			}
			want, wantTokens := legacyCacheAffinityKey(t, "OpenAI ", selector, "user-1", struct {
				Tools    []map[string]any `json:"tools,omitempty"`
				Messages []core.Message   `json:"messages"`
			}{reqTools, messages})
			digest := newPrefixDigest("OpenAI ", selector, "user-1")
			digest.writeChatPrefix(reqTools, messages)
			if digest.err != nil {
				t.Fatal(digest.err)
			}
			if got := digest.key(); got != want {
				t.Fatalf("tools=%v key mismatch: got %s want %s", withTools, got, want)
			}
			if got := digest.tokens(); got != wantTokens {
				t.Fatalf("tools=%v token estimate mismatch: got %d want %d", withTools, got, wantTokens)
			}
		}
	})

	t.Run("responses", func(t *testing.T) {
		items := []core.ResponsesInputElement{
			{Role: "user", Content: "hello <there>"},
			{Role: "user", Content: []core.ContentPart{{Type: "input_text", Text: "typed"}}},
			{Role: "user", Content: []any{map[string]any{"type": "input_text", "text": "generic"}}},
			{Type: "function_call", CallID: "c1", Name: "lookup", Arguments: `{}`},
		}
		for _, tc := range []struct {
			instructions string
			tools        []map[string]any
		}{{}, {instructions: "be brief"}, {tools: tools}, {instructions: "be brief", tools: tools}} {
			want, wantTokens := legacyCacheAffinityKey(t, "openai", selector, "", struct {
				Instructions string                       `json:"instructions,omitempty"`
				Tools        []map[string]any             `json:"tools,omitempty"`
				Input        []core.ResponsesInputElement `json:"input"`
			}{tc.instructions, tc.tools, items})
			digest := newPrefixDigest("openai", selector, "")
			digest.writeResponsesPrefix(tc.instructions, tc.tools, items)
			if digest.err != nil {
				t.Fatal(digest.err)
			}
			if got := digest.key(); got != want {
				t.Fatalf("%+v key mismatch: got %s want %s", tc, got, want)
			}
			if got := digest.tokens(); got != wantTokens {
				t.Fatalf("%+v token estimate mismatch: got %d want %d", tc, got, wantTokens)
			}
		}
	})
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
