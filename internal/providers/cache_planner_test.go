package providers

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strings"
	"testing"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/require"

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
			require.NotSame(t, req, planned)

			if tt.field != "" {
				require.NotEmpty(t, planned.ExtraFields.Lookup(tt.field), "planned request lacks %q", tt.field)
			}
			if tt.marker != "" {
				require.NotEmpty(t, planned.Messages[0].ExtraFields.Lookup(tt.marker), "stable prefix lacks %q", tt.marker)
			}
			if tt.provider == "gemini" {
				require.NotNil(t, planned.PromptCachePlan, "Gemini plan lacks an internal cached-content key")
				require.NotEmpty(t, planned.PromptCachePlan.Key, "Gemini plan lacks an internal cached-content key")
			}
			if promptCacheProfileFor(tt.provider).mode == promptCacheOpenAI {
				parts, ok := planned.Messages[0].Content.([]core.ContentPart)
				require.True(t, ok)
				require.Len(t, parts, 1)
				require.NotEmpty(t, parts[0].ExtraFields.Lookup("prompt_cache_breakpoint"), "OpenAI stable content lacks a breakpoint: %#v", planned.Messages[0].Content)
			}
			require.True(t, req.ExtraFields.IsEmpty())
			require.True(t, req.Messages[0].ExtraFields.IsEmpty())
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
			got := providerCacheMinimum(profile, tt.model)
			require.Equal(t, tt.want, got)
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
			got := newCachePlanner().enabled
			require.Equal(t, tt.enabled, got)
		})
	}
}

func TestCachePlannerHonorsMinimumAndClientDirective(t *testing.T) {
	planner := &cachePlanner{enabled: true}
	short := &core.ChatRequest{Messages: []core.Message{{Role: "system", Content: "short"}, {Role: "user", Content: "turn"}}}
	got := planner.planChat(short, "openai", core.ModelSelector{Model: "gpt-5.6"})
	require.Same(t, short, got)

	directed := &core.ChatRequest{
		Messages:    []core.Message{{Role: "system", Content: strings.Repeat("x", 9000)}, {Role: "user", Content: "turn"}},
		ExtraFields: core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{"prompt_cache_key": json.RawMessage(`"client"`)}),
	}
	got = planner.planChat(directed, "openai", core.ModelSelector{Model: "gpt-5.6"})
	require.Same(t, directed, got)
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
		{name: "map parts", content: []map[string]any{{"type": "input_text", "text": prefix}}},
	}
	for _, shape := range shapes {
		t.Run(shape.name, func(t *testing.T) {
			req := &core.ResponsesRequest{Model: "gpt-5.6", Input: []core.ResponsesInputElement{
				{Role: "user", Content: shape.content},
				{Role: "user", Content: "dynamic"},
			}}
			before, err := json.Marshal(req)
			require.NoError(t, err)

			planned := planner.planResponses(req, "openai", core.ModelSelector{Provider: "openai-primary", Model: "gpt-5.6"})
			require.NotSame(t, req, planned)
			require.NotEmpty(t, planned.ExtraFields.Lookup("prompt_cache_key"))
			require.NotEmpty(t, planned.ExtraFields.Lookup("prompt_cache_options"))

			plannedJSON, err := json.Marshal(planned)
			require.NoError(t, err)
			require.Contains(t, string(plannedJSON), string([]byte(`"prompt_cache_breakpoint"`)))

			// The Responses API rejects the Chat content vocabulary, so a
			// breakpoint must never downgrade a part to {"type":"text"}.
			require.False(t, bytes.Contains(plannedJSON, []byte(`"type":"text"`)), "Responses plan emitted Chat content vocabulary: %s", plannedJSON)
			require.Contains(t, string(plannedJSON), string([]byte(`"type":"input_text"`)))

			assertResponsesBreakpointBlock(t, plannedJSON, map[string]any{
				"type": "input_text", "text": prefix,
				"prompt_cache_breakpoint": map[string]any{"mode": "explicit"},
			})
			after, err := json.Marshal(req)
			require.NoError(t, err)
			require.Equal(t, string(after), string(before), "planner mutated caller: before=%s after=%s", before, after)
		})
	}
	anthropic := &core.ResponsesRequest{Input: []core.ResponsesInputElement{
		{Role: "user", Content: prefix}, {Role: "user", Content: "dynamic"},
	}}
	got := planner.planResponses(anthropic, "anthropic", core.ModelSelector{Model: "claude-sonnet-4-5"})
	require.NotSame(t, anthropic, got)
	require.NotEmpty(t, got.ExtraFields.Lookup("cache_control"))

	short := &core.ResponsesRequest{Input: []core.ResponsesInputElement{
		{Role: "user", Content: "short"}, {Role: "user", Content: "dynamic"},
	}}
	got = planner.planResponses(short, "openai", core.ModelSelector{Model: "gpt-5.6"})
	require.Same(t, short, got)
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
	got := (&cachePlanner{enabled: true}).planChat(req, "openai", core.ModelSelector{Model: "gpt-5.6"})
	require.Same(t, req, got)
}

func TestCachePlannerProviderCapabilityBoundaries(t *testing.T) {
	req := &core.ChatRequest{Messages: []core.Message{
		{Role: "system", Content: strings.Repeat("x", 20000)},
		{Role: "user", Content: "turn"},
	}}
	planner := &cachePlanner{enabled: true}
	for _, provider := range []string{"openrouter", "vertex", "unknown"} {
		got := planner.planChat(req, provider, core.ModelSelector{Model: "gemini-2.5-pro"})
		require.Same(t, req, got, "provider %q unexpectedly received an automatic plan", provider)
	}
}

func TestCachePlannerSkipsUnsupportedResponsesModesBeforeCloning(t *testing.T) {
	req := &core.ResponsesRequest{Input: []core.ResponsesInputElement{
		{Role: "user", Content: strings.Repeat("x", 20000)},
		{Role: "user", Content: "turn"},
	}}
	planner := &cachePlanner{enabled: true}
	for _, provider := range []string{"bedrock", "gemini", "openrouter", "unknown"} {
		got := planner.planResponses(req, provider, core.ModelSelector{Model: "model"})
		require.Same(t, req, got, "provider %q unexpectedly received a Responses plan", provider)
	}
}

func TestCloneChatRequestPreservesInternalCachePlan(t *testing.T) {
	req := &core.ChatRequest{PromptCachePlan: &core.PromptCachePlan{Key: "stable"}}
	clone := cloneChatRequest(req)
	require.NotNil(t, clone.PromptCachePlan)
	require.Equal(t, "stable", clone.PromptCachePlan.Key, "clone lost internal cache metadata: %+v", clone)
	require.NotSame(t, req.PromptCachePlan, clone.PromptCachePlan)
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
		require.NoError(t, err)

		planned := planner.planChat(req, "openai", core.ModelSelector{Provider: "openai-primary", Model: "gpt-5.6"})
		require.NotSame(t, req, planned)

		plannedParts, ok := planned.Messages[0].Content.([]core.ContentPart)
		require.True(t, ok)
		require.Len(t, plannedParts, 1)
		require.NotEmpty(t, plannedParts[0].ExtraFields.Lookup("prompt_cache_breakpoint"), "planned content lacks breakpoint: %#v", planned.Messages[0].Content)
		require.NotSame(t, &parts[0], &plannedParts[0])
		require.True(t, parts[0].ExtraFields.IsEmpty())
		require.NotSame(t, &req.Messages[0], &planned.Messages[0])
		require.Empty(t, req.ExtraFields.Lookup("prompt_cache_key"))
		require.NotEmpty(t, req.ExtraFields.Lookup("seed"))

		after, err := json.Marshal(req)
		require.NoError(t, err)
		require.Equal(t, string(after), string(before), "planner mutated caller: before=%s after=%s", before, after)

		bedrock := planner.planChat(req, "bedrock", core.ModelSelector{Provider: "bedrock-primary", Model: "anthropic.claude-sonnet-4-5"})
		require.NotEmpty(t, bedrock.Messages[0].ExtraFields.Lookup(core.GatewayCachePointField))
		require.True(t, req.Messages[0].ExtraFields.IsEmpty())
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
		require.True(t, ok)
		require.NotSame(t, req, planned)

		plannedBlocks, ok := items[0].Content.([]any)
		require.True(t, ok)
		require.Len(t, plannedBlocks, 1, "unexpected planned content: %#v", items[0].Content)

		marked, _ := plannedBlocks[0].(map[string]any)
		_, exists := marked["prompt_cache_breakpoint"]
		require.True(t, exists)
		_, leaked := block["prompt_cache_breakpoint"]
		require.False(t, leaked)
		require.NotSame(t, &blocks[0], &plannedBlocks[0])

		original := req.Input.([]core.ResponsesInputElement)
		require.NotSame(t, &original[0], &items[0])
	})

	t.Run("responses typed map blocks", func(t *testing.T) {
		block := map[string]any{"type": "input_text", "text": prefix}
		req := &core.ResponsesRequest{Model: "gpt-5.6", Input: []core.ResponsesInputElement{
			{Role: "user", Content: []map[string]any{block}},
			{Role: "user", Content: "dynamic"},
		}}
		planned := planner.planResponses(req, "openai", core.ModelSelector{Provider: "openai-primary", Model: "gpt-5.6"})
		body, err := json.Marshal(planned)
		require.NoError(t, err)
		require.Contains(t, string(body), string([]byte(`"prompt_cache_breakpoint"`)))
		_, leaked := block["prompt_cache_breakpoint"]
		require.False(t, leaked)
	})
}

// legacyCacheAffinityKey is the pre-streaming key derivation: sha256 over the
// header fields followed by the fully marshaled prefix object. The streaming
// digest must produce the same key so deployed cache affinity survives upgrades.
func legacyCacheAffinityKey(t *testing.T, providerType string, selector core.ModelSelector, user string, prefix any) (string, int) {
	t.Helper()
	body, err := json.Marshal(prefix)
	require.NoError(t, err)

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
			require.NoError(t, digest.err)
			require.Equal(t, want, digest.key(), "tools=%v", withTools)
			require.Equal(t, wantTokens, digest.tokens(), "tools=%v token estimate", withTools)
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
			require.NoError(t, digest.err)
			require.Equal(t, want, digest.key(), "%+v", tc)
			require.Equal(t, wantTokens, digest.tokens(), "%+v token estimate", tc)
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
		require.NotNil(t, planned.PromptCachePlan)
		require.NotEmpty(t, planned.PromptCachePlan.Key)

		keys[planned.PromptCachePlan.Key] = struct{}{}
	}
	require.Len(t, keys, 4)
}

// assertResponsesBreakpointBlock decodes the planned request and checks the
// first input item's content is exactly one block equal to want.
func assertResponsesBreakpointBlock(t *testing.T, plannedJSON []byte, want map[string]any) {
	t.Helper()
	blocks := decodePrefixContentBlocks(t, plannedJSON)
	require.Len(t, blocks, 1, "expected one content block on the prefix item: %s", plannedJSON)

	got, err := json.Marshal(blocks[0])
	require.NoError(t, err)

	expected, err := json.Marshal(want)
	require.NoError(t, err)
	require.Equal(t, string(expected), string(got), "breakpoint block mismatch:\n got: %s\nwant: %s", got, expected)
}

func TestCachePlannerResponsesTypedPartsKeepVocabularyAndExtras(t *testing.T) {
	planner := &cachePlanner{enabled: true}
	prefix := strings.Repeat("stable response context ", 1200)
	req := &core.ResponsesRequest{Model: "gpt-5.6", Input: []core.ResponsesInputElement{
		{Role: "user", Content: []core.ContentPart{
			{
				Type: "input_text", Text: prefix,
				ExtraFields: core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{"x_note": json.RawMessage(`"keep"`)}),
			},
			{Type: "input_image", ImageURL: &core.ImageURLContent{URL: "https://example.com/a.png", Detail: "low"}},
		}},
		{Role: "user", Content: "dynamic"},
	}}
	planned := planner.planResponses(req, "openai", core.ModelSelector{Provider: "openai-primary", Model: "gpt-5.6"})
	require.NotSame(t, req, planned)

	plannedJSON, err := json.Marshal(planned)
	require.NoError(t, err)

	blocks := decodePrefixContentBlocks(t, plannedJSON)
	require.Len(t, blocks, 2)
	require.Equal(t, "input_text", blocks[0]["type"])
	require.Equal(t, "keep", blocks[0]["x_note"])
	require.Nil(t, blocks[0]["prompt_cache_breakpoint"], "text block lost vocabulary or extras, or gained a breakpoint: %v", blocks[0])

	image := blocks[1]
	require.Equal(t, "input_image", image["type"])
	require.NotNil(t, image["prompt_cache_breakpoint"], "last cacheable block should carry the breakpoint with input_image type: %v", image)

	imageURL, _ := image["image_url"].(map[string]any)
	require.Equal(t, "https://example.com/a.png", imageURL["url"])
	require.Equal(t, "low", imageURL["detail"], "image payload not preserved: %v", image)
}

// decodePrefixContentBlocks returns the content blocks of the first input item
// of a marshalled Responses request.
func decodePrefixContentBlocks(t *testing.T, plannedJSON []byte) []map[string]any {
	t.Helper()
	var decoded struct {
		Input []struct {
			Content json.RawMessage `json:"content"`
		} `json:"input"`
	}
	err := json.Unmarshal(plannedJSON, &decoded)
	require.NoError(t, err)
	require.NotEmpty(t, decoded.Input, "planned request has no input items: %s", plannedJSON)

	var blocks []map[string]any
	require.NoError(t, json.Unmarshal(decoded.Input[0].Content, &blocks), "prefix item content is not a block array: %s", decoded.Input[0].Content)

	return blocks
}

func TestMarkOpenAIResponsesBreakpointEdgeCases(t *testing.T) {
	t.Run("keeps an existing breakpoint untouched", func(t *testing.T) {
		block := map[string]any{"type": "input_text", "text": "prefix", "prompt_cache_breakpoint": map[string]any{"mode": "explicit"}}
		req := &core.ResponsesRequest{Input: []core.ResponsesInputElement{
			{Role: "user", Content: []any{block}},
			{Role: "user", Content: "dynamic"},
		}}
		require.True(t, markOpenAIResponsesBreakpoint(req))

		items := req.Input.([]core.ResponsesInputElement)
		blocks, _ := items[0].Content.([]any)
		require.Len(t, blocks, 1)
		require.NotNil(t, blocks[0].(map[string]any)["prompt_cache_breakpoint"], "breakpoint block changed: %v", items[0].Content)
	})
	t.Run("skips typed parts that cannot be encoded and unsupported shapes", func(t *testing.T) {
		req := &core.ResponsesRequest{Input: []core.ResponsesInputElement{
			{Role: "user", Content: 42},
			{Role: "user", Content: []core.ContentPart{{Type: "input_file"}}},
			{Role: "user", Content: []any{"not a block"}},
			{Role: "user", Content: "dynamic"},
		}}
		require.False(t, markOpenAIResponsesBreakpoint(req), "marked an item with no cacheable block: %+v", req.Input)
	})
}
