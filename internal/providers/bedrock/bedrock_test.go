package bedrock

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	brtypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/providers"
)

func TestParseBaseURL(t *testing.T) {
	cases := []struct {
		name         string
		in           string
		wantRegion   string
		wantEndpoint string
	}{
		{"empty", "", "", ""},
		{"region", "us-east-1", "us-east-1", ""},
		{"runtime endpoint", "https://bedrock-runtime.us-west-2.amazonaws.com", "us-west-2", "https://bedrock-runtime.us-west-2.amazonaws.com"},
		{"control endpoint", "https://bedrock.eu-west-1.amazonaws.com", "eu-west-1", "https://bedrock.eu-west-1.amazonaws.com"},
		{"unknown host", "https://internal.example.com/bedrock", "", "https://internal.example.com/bedrock"},
		{"non-AWS host with bedrock subdomain leaves region empty", "https://bedrock.internal.example.com", "", "https://bedrock.internal.example.com"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			region, endpoint := parseBaseURL(tc.in)
			if region != tc.wantRegion {
				t.Errorf("region = %q, want %q", region, tc.wantRegion)
			}
			if endpoint != tc.wantEndpoint {
				t.Errorf("endpoint = %q, want %q", endpoint, tc.wantEndpoint)
			}
		})
	}
}

func TestPlaneEndpoint(t *testing.T) {
	cases := []struct {
		name        string
		in          string
		wantRuntime string
		wantControl string
	}{
		{
			name:        "runtime URL stays runtime, control derived",
			in:          "https://bedrock-runtime.us-east-1.amazonaws.com",
			wantRuntime: "https://bedrock-runtime.us-east-1.amazonaws.com",
			wantControl: "https://bedrock.us-east-1.amazonaws.com",
		},
		{
			name:        "control URL is rewritten for runtime, kept for control",
			in:          "https://bedrock.us-east-1.amazonaws.com",
			wantRuntime: "https://bedrock-runtime.us-east-1.amazonaws.com",
			wantControl: "https://bedrock.us-east-1.amazonaws.com",
		},
		{
			name:        "custom host with bedrock. in path is left alone",
			in:          "https://internal.example.com/bedrock",
			wantRuntime: "https://internal.example.com/bedrock",
			wantControl: "https://internal.example.com/bedrock",
		},
		{
			name:        "custom hostname containing bedrock-runtime. is not corrupted",
			in:          "https://my-bedrock-runtime.internal.example.com",
			wantRuntime: "https://my-bedrock-runtime.internal.example.com",
			wantControl: "https://my-bedrock-runtime.internal.example.com",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := runtimePlaneEndpoint(tc.in); got != tc.wantRuntime {
				t.Errorf("runtimePlaneEndpoint(%q) = %q, want %q", tc.in, got, tc.wantRuntime)
			}
			if got := controlPlaneEndpoint(tc.in); got != tc.wantControl {
				t.Errorf("controlPlaneEndpoint(%q) = %q, want %q", tc.in, got, tc.wantControl)
			}
		})
	}
}

func TestMapStopReason(t *testing.T) {
	cases := []struct {
		name     string
		reason   brtypes.StopReason
		hasTools bool
		want     string
	}{
		{"end_turn", brtypes.StopReasonEndTurn, false, "stop"},
		{"stop_sequence", brtypes.StopReasonStopSequence, false, "stop"},
		{"empty", "", false, "stop"},
		{"max_tokens", brtypes.StopReasonMaxTokens, false, "length"},
		{"context_window_exceeded", brtypes.StopReasonModelContextWindowExceeded, false, "length"},
		{"tool_use_with_calls", brtypes.StopReasonToolUse, true, "tool_calls"},
		{"tool_use_no_calls", brtypes.StopReasonToolUse, false, "tool_use"},
		{"unknown", "weird_reason", false, "weird_reason"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := mapStopReason(tc.reason, tc.hasTools)
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestBuildConverseParts_BasicRequest(t *testing.T) {
	temp := 0.5
	maxTokens := 1024
	req := &core.ChatRequest{
		Model:       "anthropic.claude-3-5-haiku-20241022-v1:0",
		Temperature: &temp,
		MaxTokens:   &maxTokens,
		Messages: []core.Message{
			{Role: "system", Content: "You are concise"},
			{Role: "user", Content: "hi"},
		},
	}

	parts, err := buildConverseParts(req)
	if err != nil {
		t.Fatalf("buildConverseParts: %v", err)
	}
	if awssdk.ToString(parts.modelID) != req.Model {
		t.Errorf("modelID = %q, want %q", awssdk.ToString(parts.modelID), req.Model)
	}
	if len(parts.system) != 1 {
		t.Fatalf("expected 1 system block, got %d", len(parts.system))
	}
	if got := parts.system[0].(*brtypes.SystemContentBlockMemberText).Value; got != "You are concise" {
		t.Errorf("system text = %q", got)
	}
	if len(parts.messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(parts.messages))
	}
	if parts.messages[0].Role != brtypes.ConversationRoleUser {
		t.Errorf("role = %q", parts.messages[0].Role)
	}
	if parts.infCfg == nil {
		t.Fatal("inference config should be set")
	}
	if awssdk.ToInt32(parts.infCfg.MaxTokens) != int32(maxTokens) {
		t.Errorf("max tokens = %d", awssdk.ToInt32(parts.infCfg.MaxTokens))
	}
	if awssdk.ToFloat32(parts.infCfg.Temperature) != float32(temp) {
		t.Errorf("temperature = %v", awssdk.ToFloat32(parts.infCfg.Temperature))
	}
}

func TestBuildConverseParts_MaxCompletionTokensFallback(t *testing.T) {
	req := &core.ChatRequest{
		Model:    "anthropic.claude-3-5-haiku-20241022-v1:0",
		Messages: []core.Message{{Role: "user", Content: "hi"}},
		ExtraFields: core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{
			"max_completion_tokens": json.RawMessage("256"),
		}),
	}
	parts, err := buildConverseParts(req)
	if err != nil {
		t.Fatalf("buildConverseParts: %v", err)
	}
	if parts.infCfg == nil {
		t.Fatal("inference config should be set when max_completion_tokens is provided via ExtraFields")
	}
	if got := awssdk.ToInt32(parts.infCfg.MaxTokens); got != 256 {
		t.Errorf("max tokens = %d, want 256", got)
	}
}

func TestBuildConverseParts_MaxTokensWinsOverFallback(t *testing.T) {
	maxTokens := 128
	req := &core.ChatRequest{
		Model:     "anthropic.claude-3-5-haiku-20241022-v1:0",
		MaxTokens: &maxTokens,
		Messages:  []core.Message{{Role: "user", Content: "hi"}},
		ExtraFields: core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{
			"max_completion_tokens": json.RawMessage("999"),
		}),
	}
	parts, err := buildConverseParts(req)
	if err != nil {
		t.Fatalf("buildConverseParts: %v", err)
	}
	if got := awssdk.ToInt32(parts.infCfg.MaxTokens); got != 128 {
		t.Errorf("max tokens = %d, want 128 (max_tokens should take precedence)", got)
	}
}

func TestBuildConverseParts_RejectsEmptyModel(t *testing.T) {
	_, err := buildConverseParts(&core.ChatRequest{Messages: []core.Message{{Role: "user", Content: "hi"}}})
	if err == nil {
		t.Fatal("expected error for missing model")
	}
	var ge *core.GatewayError
	if !errors.As(err, &ge) || ge.Type != core.ErrorTypeInvalidRequest {
		t.Fatalf("expected invalid_request_error, got %v", err)
	}
}

func TestBuildConverseParts_MergesParallelToolResults(t *testing.T) {
	// Caller sends one assistant turn with two parallel tool_calls, then two
	// consecutive tool-role messages with the results. Bedrock requires
	// alternating user/assistant turns, so both tool results must collapse
	// into a single user message holding two ToolResult blocks.
	req := &core.ChatRequest{
		Model: "anthropic.claude-3-5-haiku-20241022-v1:0",
		Messages: []core.Message{
			{Role: "user", Content: "weather in warsaw and tokyo?"},
			{
				Role: "assistant",
				ToolCalls: []core.ToolCall{
					{ID: "call_1", Type: "function", Function: core.FunctionCall{Name: "get_weather", Arguments: `{"city":"Warsaw"}`}},
					{ID: "call_2", Type: "function", Function: core.FunctionCall{Name: "get_weather", Arguments: `{"city":"Tokyo"}`}},
				},
			},
			{Role: "tool", ToolCallID: "call_1", Content: "Warsaw 15C"},
			{Role: "tool", ToolCallID: "call_2", Content: "Tokyo 22C"},
		},
	}
	parts, err := buildConverseParts(req)
	if err != nil {
		t.Fatalf("buildConverseParts: %v", err)
	}
	// Expect: user(text), assistant(2 tool_use), user(2 tool_result) — three messages.
	if len(parts.messages) != 3 {
		t.Fatalf("expected 3 messages, got %d: %+v", len(parts.messages), parts.messages)
	}
	last := parts.messages[2]
	if last.Role != brtypes.ConversationRoleUser {
		t.Fatalf("merged tool results must be user-role, got %q", last.Role)
	}
	if len(last.Content) != 2 {
		t.Fatalf("expected 2 ToolResult blocks in merged message, got %d", len(last.Content))
	}
	for i, want := range []string{"call_1", "call_2"} {
		tr, ok := last.Content[i].(*brtypes.ContentBlockMemberToolResult)
		if !ok {
			t.Fatalf("block %d not a ToolResult: %T", i, last.Content[i])
		}
		if got := awssdk.ToString(tr.Value.ToolUseId); got != want {
			t.Errorf("block %d ToolUseId = %q, want %q", i, got, want)
		}
	}
}

func TestBuildConverseParts_TopPFromExtraFields(t *testing.T) {
	req := &core.ChatRequest{
		Model:    "anthropic.claude-3-5-haiku-20241022-v1:0",
		Messages: []core.Message{{Role: "user", Content: "hi"}},
		ExtraFields: core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{
			"top_p": json.RawMessage("0.7"),
		}),
	}
	parts, err := buildConverseParts(req)
	if err != nil {
		t.Fatalf("buildConverseParts: %v", err)
	}
	if parts.infCfg == nil || parts.infCfg.TopP == nil {
		t.Fatal("top_p was not forwarded to InferenceConfiguration.TopP")
	}
	if got := awssdk.ToFloat32(parts.infCfg.TopP); got != 0.7 {
		t.Errorf("top_p = %v, want 0.7", got)
	}
}

func TestBuildConverseParts_TopPFromTypedField(t *testing.T) {
	topP := 0.8
	req := &core.ChatRequest{
		Model:    "anthropic.claude-3-5-haiku-20241022-v1:0",
		Messages: []core.Message{{Role: "user", Content: "hi"}},
		TopP:     &topP,
	}
	parts, err := buildConverseParts(req)
	if err != nil {
		t.Fatalf("buildConverseParts: %v", err)
	}
	if parts.infCfg == nil || parts.infCfg.TopP == nil {
		t.Fatal("typed top_p was not forwarded to InferenceConfiguration.TopP")
	}
	if got := awssdk.ToFloat32(parts.infCfg.TopP); got != 0.8 {
		t.Errorf("top_p = %v, want 0.8", got)
	}
}

func TestBuildConverseParts_TypedTopPWinsOverExtraFields(t *testing.T) {
	topP := 0.8
	req := &core.ChatRequest{
		Model:    "anthropic.claude-3-5-haiku-20241022-v1:0",
		Messages: []core.Message{{Role: "user", Content: "hi"}},
		TopP:     &topP,
		ExtraFields: core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{
			"top_p": json.RawMessage("0.2"),
		}),
	}
	parts, err := buildConverseParts(req)
	if err != nil {
		t.Fatalf("buildConverseParts: %v", err)
	}
	if parts.infCfg == nil || parts.infCfg.TopP == nil {
		t.Fatal("typed top_p was not forwarded to InferenceConfiguration.TopP")
	}
	if got := awssdk.ToFloat32(parts.infCfg.TopP); got != 0.8 {
		t.Errorf("top_p = %v, want typed value 0.8", got)
	}
}

func TestBuildConverseParts_RejectsMaxTokensOverflow(t *testing.T) {
	overflow := int(int64(1) << 33) // 2^33, fits in int64 but not int32
	req := &core.ChatRequest{
		Model:     "anthropic.claude-3-5-haiku-20241022-v1:0",
		MaxTokens: &overflow,
		Messages:  []core.Message{{Role: "user", Content: "hi"}},
	}
	_, err := buildConverseParts(req)
	if err == nil {
		t.Fatal("expected invalid_request_error for oversized max_tokens")
	}
	var ge *core.GatewayError
	if !errors.As(err, &ge) || ge.Type != core.ErrorTypeInvalidRequest {
		t.Fatalf("expected invalid_request_error, got %v", err)
	}
}

func TestBuildConverseParts_ToolResultBatchesDoNotAliasAcrossTurns(t *testing.T) {
	// Regression: flushToolResults previously returned blocks[:0], which
	// shared the backing array with the emitted message's Content. The next
	// pending tool result then overwrote the first element of the earlier
	// turn's Content. Verify both turns retain their original tool IDs.
	req := &core.ChatRequest{
		Model: "anthropic.claude-3-5-haiku-20241022-v1:0",
		Messages: []core.Message{
			{Role: "user", Content: "weather in A and B?"},
			{Role: "assistant", ToolCalls: []core.ToolCall{
				{ID: "c1", Type: "function", Function: core.FunctionCall{Name: "get_weather", Arguments: `{"city":"A"}`}},
				{ID: "c2", Type: "function", Function: core.FunctionCall{Name: "get_weather", Arguments: `{"city":"B"}`}},
			}},
			{Role: "tool", ToolCallID: "c1", Content: "A: sunny"},
			{Role: "tool", ToolCallID: "c2", Content: "B: rainy"},
			{Role: "assistant", ToolCalls: []core.ToolCall{
				{ID: "c3", Type: "function", Function: core.FunctionCall{Name: "get_weather", Arguments: `{"city":"C"}`}},
			}},
			{Role: "tool", ToolCallID: "c3", Content: "C: snowy"},
		},
	}
	parts, err := buildConverseParts(req)
	if err != nil {
		t.Fatalf("buildConverseParts: %v", err)
	}

	collectIDs := func(content []brtypes.ContentBlock) []string {
		var ids []string
		for _, blk := range content {
			if tr, ok := blk.(*brtypes.ContentBlockMemberToolResult); ok {
				ids = append(ids, awssdk.ToString(tr.Value.ToolUseId))
			}
		}
		return ids
	}
	var firstBatch, secondBatch []string
	for _, msg := range parts.messages {
		if msg.Role != brtypes.ConversationRoleUser {
			continue
		}
		ids := collectIDs(msg.Content)
		if len(ids) == 0 {
			continue
		}
		if firstBatch == nil {
			firstBatch = ids
		} else {
			secondBatch = ids
		}
	}
	if got, want := firstBatch, []string{"c1", "c2"}; !equalStrings(got, want) {
		t.Errorf("first turn tool result IDs = %v, want %v (aliasing bug overwrote them)", got, want)
	}
	if got, want := secondBatch, []string{"c3"}; !equalStrings(got, want) {
		t.Errorf("second turn tool result IDs = %v, want %v", got, want)
	}
}

func TestBuildConverseParts_MergesUserTextAfterToolResult(t *testing.T) {
	// [user, assistant_tool_call, tool, user_text] would otherwise produce
	// [user, asst, user_tool_result, user_text] — two consecutive user turns,
	// which Bedrock rejects with ValidationException. The two adjacent user
	// blocks must merge into one turn.
	req := &core.ChatRequest{
		Model: "anthropic.claude-3-5-haiku-20241022-v1:0",
		Messages: []core.Message{
			{Role: "user", Content: "weather in Warsaw?"},
			{Role: "assistant", ToolCalls: []core.ToolCall{
				{ID: "c1", Type: "function", Function: core.FunctionCall{Name: "get_weather", Arguments: `{"city":"Warsaw"}`}},
			}},
			{Role: "tool", ToolCallID: "c1", Content: "15C sunny"},
			{Role: "user", Content: "thanks!"},
		},
	}
	parts, err := buildConverseParts(req)
	if err != nil {
		t.Fatalf("buildConverseParts: %v", err)
	}
	if len(parts.messages) != 3 {
		t.Fatalf("expected 3 turns (user, asst, merged-user), got %d", len(parts.messages))
	}
	last := parts.messages[2]
	if last.Role != brtypes.ConversationRoleUser {
		t.Fatalf("last role = %q, want user", last.Role)
	}
	// Expect [ToolResult, Text] in the merged user message.
	if len(last.Content) != 2 {
		t.Fatalf("merged user message should have 2 blocks, got %d", len(last.Content))
	}
	if _, ok := last.Content[0].(*brtypes.ContentBlockMemberToolResult); !ok {
		t.Errorf("first block should be ToolResult, got %T", last.Content[0])
	}
	tb, ok := last.Content[1].(*brtypes.ContentBlockMemberText)
	if !ok {
		t.Fatalf("second block should be Text, got %T", last.Content[1])
	}
	if tb.Value != "thanks!" {
		t.Errorf("merged text = %q, want %q", tb.Value, "thanks!")
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestBuildConverseParts_AssistantToolCallsRoundtrip(t *testing.T) {
	req := &core.ChatRequest{
		Model: "anthropic.claude-3-5-haiku-20241022-v1:0",
		Messages: []core.Message{
			{Role: "user", Content: "what is the weather"},
			{
				Role: "assistant",
				ToolCalls: []core.ToolCall{{
					ID:   "tool_call_1",
					Type: "function",
					Function: core.FunctionCall{
						Name:      "get_weather",
						Arguments: `{"city":"Paris"}`,
					},
				}},
			},
			{Role: "tool", ToolCallID: "tool_call_1", Content: "Sunny, 22C"},
		},
	}
	parts, err := buildConverseParts(req)
	if err != nil {
		t.Fatalf("buildConverseParts: %v", err)
	}
	if len(parts.messages) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(parts.messages))
	}
	// Assistant message should carry a ToolUse content block
	asst := parts.messages[1]
	if asst.Role != brtypes.ConversationRoleAssistant {
		t.Fatalf("expected assistant role, got %q", asst.Role)
	}
	tu, ok := asst.Content[0].(*brtypes.ContentBlockMemberToolUse)
	if !ok {
		t.Fatalf("expected tool use block, got %T", asst.Content[0])
	}
	if awssdk.ToString(tu.Value.ToolUseId) != "tool_call_1" {
		t.Errorf("tool use id = %q", awssdk.ToString(tu.Value.ToolUseId))
	}
	// Tool result message must be sent as user role with ContentBlockMemberToolResult
	toolMsg := parts.messages[2]
	if toolMsg.Role != brtypes.ConversationRoleUser {
		t.Fatalf("expected tool result to use user role, got %q", toolMsg.Role)
	}
	tr, ok := toolMsg.Content[0].(*brtypes.ContentBlockMemberToolResult)
	if !ok {
		t.Fatalf("expected tool result block, got %T", toolMsg.Content[0])
	}
	if awssdk.ToString(tr.Value.ToolUseId) != "tool_call_1" {
		t.Errorf("tool result id = %q", awssdk.ToString(tr.Value.ToolUseId))
	}
}

func TestConvertTools_ToolChoiceNormalization(t *testing.T) {
	tools := []map[string]any{{
		"type": "function",
		"function": map[string]any{
			"name":        "lookup",
			"description": "find a thing",
			"parameters": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"q": map[string]any{"type": "string"},
				},
			},
		},
	}}

	cases := []struct {
		name       string
		choice     any
		wantNil    bool
		wantChoice string // type name suffix for assertion when cfg is non-nil
	}{
		{"auto string", "auto", false, "Auto"},
		{"required string", "required", false, "Any"},
		{"none string drops tool config", "none", true, ""},
		{"none object drops tool config", map[string]any{"type": "none"}, true, ""},
		{"function object", map[string]any{"type": "function", "function": map[string]any{"name": "lookup"}}, false, "Tool"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := convertTools(tools, tc.choice)
			if err != nil {
				t.Fatalf("convertTools: %v", err)
			}
			if tc.wantNil {
				if cfg != nil {
					t.Fatalf("expected nil ToolConfiguration when tool_choice is none, got %+v", cfg)
				}
				return
			}
			if cfg == nil {
				t.Fatal("expected ToolConfiguration")
			}
			gotName := "<nil>"
			switch cfg.ToolChoice.(type) {
			case *brtypes.ToolChoiceMemberAuto:
				gotName = "Auto"
			case *brtypes.ToolChoiceMemberAny:
				gotName = "Any"
			case *brtypes.ToolChoiceMemberTool:
				gotName = "Tool"
			}
			if gotName != tc.wantChoice {
				t.Errorf("got %s, want %s", gotName, tc.wantChoice)
			}
		})
	}
}

func TestConvertConverseOutput_TextAndUsage(t *testing.T) {
	usage := &brtypes.TokenUsage{
		InputTokens:  awssdk.Int32(10),
		OutputTokens: awssdk.Int32(20),
		TotalTokens:  awssdk.Int32(30),
	}
	out := &bedrockruntime.ConverseOutput{
		Output: &brtypes.ConverseOutputMemberMessage{
			Value: brtypes.Message{
				Role: brtypes.ConversationRoleAssistant,
				Content: []brtypes.ContentBlock{
					&brtypes.ContentBlockMemberText{Value: "Hello there"},
				},
			},
		},
		StopReason: brtypes.StopReasonEndTurn,
		Usage:      usage,
	}
	resp := convertConverseOutput("anthropic.claude-3-5-haiku-20241022-v1:0", out)
	if resp.Provider != providerName {
		t.Errorf("provider = %q", resp.Provider)
	}
	if len(resp.Choices) != 1 {
		t.Fatalf("expected 1 choice, got %d", len(resp.Choices))
	}
	if resp.Choices[0].FinishReason != "stop" {
		t.Errorf("finish_reason = %q", resp.Choices[0].FinishReason)
	}
	if got := core.ExtractTextContent(resp.Choices[0].Message.Content); got != "Hello there" {
		t.Errorf("content = %q", got)
	}
	if resp.Usage.PromptTokens != 10 || resp.Usage.CompletionTokens != 20 || resp.Usage.TotalTokens != 30 {
		t.Errorf("usage = %+v", resp.Usage)
	}
}

func TestConvertConverseOutput_ToolUseRoundtripsArguments(t *testing.T) {
	out := &bedrockruntime.ConverseOutput{
		Output: &brtypes.ConverseOutputMemberMessage{
			Value: brtypes.Message{
				Role: brtypes.ConversationRoleAssistant,
				Content: []brtypes.ContentBlock{
					&brtypes.ContentBlockMemberToolUse{
						Value: brtypes.ToolUseBlock{
							ToolUseId: awssdk.String("tu_1"),
							Name:      awssdk.String("get_weather"),
							Input:     toDocument(map[string]any{"city": "Paris"}),
						},
					},
				},
			},
		},
		StopReason: brtypes.StopReasonToolUse,
		Usage:      &brtypes.TokenUsage{InputTokens: awssdk.Int32(1), OutputTokens: awssdk.Int32(1), TotalTokens: awssdk.Int32(2)},
	}
	resp := convertConverseOutput("model", out)
	if resp.Choices[0].FinishReason != "tool_calls" {
		t.Fatalf("finish_reason = %q", resp.Choices[0].FinishReason)
	}
	calls := resp.Choices[0].Message.ToolCalls
	if len(calls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(calls))
	}
	if calls[0].Function.Name != "get_weather" {
		t.Errorf("name = %q", calls[0].Function.Name)
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(calls[0].Function.Arguments), &args); err != nil {
		t.Fatalf("arguments not valid JSON: %v (%s)", err, calls[0].Function.Arguments)
	}
	if args["city"] != "Paris" {
		t.Errorf("args = %v", args)
	}
}

// TestNew_BearerTokenOnly ensures the provider initializes when the only AWS
// credential source is the bearer-token env var (AWS_BEARER_TOKEN_BEDROCK).
// The AWS SDK reads that name natively as part of the default credential
// chain; this test guards against config-loading regressions on that path.
func TestNew_BearerTokenOnly(t *testing.T) {
	// Isolate from the host's real AWS state so the only credential signal
	// the SDK can find is the bearer token we set below.
	t.Setenv("AWS_BEARER_TOKEN_BEDROCK", "ABSKBedrockTestToken-fake")
	t.Setenv("AWS_ACCESS_KEY_ID", "")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "")
	t.Setenv("AWS_SESSION_TOKEN", "")
	t.Setenv("AWS_PROFILE", "")
	t.Setenv("AWS_REGION", "")
	t.Setenv("AWS_DEFAULT_REGION", "")
	t.Setenv("AWS_CONFIG_FILE", "/dev/null")
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", "/dev/null")

	p := New(providers.ProviderConfig{BaseURL: "us-east-1"}, providers.ProviderOptions{}).(*Provider)
	if p.configErr != nil {
		t.Fatalf("configErr = %v, want nil", p.configErr)
	}
	if p.runtime == nil {
		t.Fatal("runtime client should be constructed")
	}
	if p.region != "us-east-1" {
		t.Errorf("region = %q, want us-east-1", p.region)
	}
	if err := p.ready(); err != nil {
		t.Errorf("ready() = %v, want nil", err)
	}
}

func TestRegistration(t *testing.T) {
	if Registration.Type != providerName {
		t.Errorf("Registration.Type = %q, want %q", Registration.Type, providerName)
	}
	if !Registration.Discovery.AllowAPIKeyless {
		t.Error("Registration.Discovery.AllowAPIKeyless should be true")
	}
	if Registration.New == nil {
		t.Fatal("Registration.New should not be nil")
	}
}

func TestStreamConverter_FormatChunkContent(t *testing.T) {
	sc := newOpenAIStream(nil, "test-model")
	chunk := sc.formatChunk(map[string]any{"content": "Hi"}, nil, nil)
	if !strings.HasPrefix(chunk, "data: ") || !strings.HasSuffix(chunk, "\n\n") {
		t.Fatalf("malformed SSE framing: %q", chunk)
	}
	payload := strings.TrimSuffix(strings.TrimPrefix(chunk, "data: "), "\n\n")
	var parsed map[string]any
	if err := json.Unmarshal([]byte(payload), &parsed); err != nil {
		t.Fatalf("payload not JSON: %v", err)
	}
	if parsed["object"] != "chat.completion.chunk" {
		t.Errorf("object = %v", parsed["object"])
	}
	if parsed["model"] != "test-model" {
		t.Errorf("model = %v", parsed["model"])
	}
	if parsed["provider"] != providerName {
		t.Errorf("provider = %v", parsed["provider"])
	}
	choices, _ := parsed["choices"].([]any)
	if len(choices) != 1 {
		t.Fatalf("expected 1 choice, got %d", len(choices))
	}
	choice := choices[0].(map[string]any)
	delta := choice["delta"].(map[string]any)
	if delta["content"] != "Hi" {
		t.Errorf("delta.content = %v", delta["content"])
	}
}

// TestStreamConverter_DeferredFinishWithUsage asserts that messageStop alone
// does not emit a finish chunk; the chunk is emitted on metadata and carries
// both finish_reason and usage so include_usage callers see token counts.
func TestStreamConverter_DeferredFinishWithUsage(t *testing.T) {
	sc := newOpenAIStream(nil, "test-model")

	sc.handleEvent(&brtypes.ConverseStreamOutputMemberMessageStop{
		Value: brtypes.MessageStopEvent{StopReason: brtypes.StopReasonEndTurn},
	})
	if len(sc.buf) != 0 {
		t.Fatalf("messageStop should not emit yet, got %q", string(sc.buf))
	}
	if !sc.havePendingStop || sc.finishSent {
		t.Fatalf("expected pending finish, sent=%v pending=%v", sc.finishSent, sc.havePendingStop)
	}

	sc.handleEvent(&brtypes.ConverseStreamOutputMemberMetadata{
		Value: brtypes.ConverseStreamMetadataEvent{
			Usage: &brtypes.TokenUsage{
				InputTokens:  awssdk.Int32(11),
				OutputTokens: awssdk.Int32(13),
				TotalTokens:  awssdk.Int32(24),
			},
		},
	})
	if !sc.finishSent {
		t.Fatal("metadata should have flushed the finish chunk")
	}

	payload := strings.TrimSuffix(strings.TrimPrefix(string(sc.buf), "data: "), "\n\n")
	var parsed map[string]any
	if err := json.Unmarshal([]byte(payload), &parsed); err != nil {
		t.Fatalf("payload not JSON: %v (%q)", err, payload)
	}
	choices := parsed["choices"].([]any)
	choice := choices[0].(map[string]any)
	if choice["finish_reason"] != "stop" {
		t.Errorf("finish_reason = %v, want stop", choice["finish_reason"])
	}
	usage, ok := parsed["usage"].(map[string]any)
	if !ok {
		t.Fatalf("usage missing from finish chunk: %v", parsed)
	}
	if usage["total_tokens"].(float64) != 24 {
		t.Errorf("total_tokens = %v, want 24", usage["total_tokens"])
	}
}

// TestStreamConverter_DeferredFinishWithoutMetadata asserts that we still
// emit a finish chunk if the stream closes before a metadata event — usage
// is absent in that case but finish_reason must not be swallowed.
func TestStreamConverter_DeferredFinishWithoutMetadata(t *testing.T) {
	sc := newOpenAIStream(nil, "test-model")
	sc.handleEvent(&brtypes.ConverseStreamOutputMemberMessageStop{
		Value: brtypes.MessageStopEvent{StopReason: brtypes.StopReasonMaxTokens},
	})
	if sc.finishSent {
		t.Fatal("finish should still be deferred")
	}
	sc.flushFinish()
	if !sc.finishSent {
		t.Fatal("flushFinish should have sent the chunk")
	}
	payload := strings.TrimSuffix(strings.TrimPrefix(string(sc.buf), "data: "), "\n\n")
	var parsed map[string]any
	if err := json.Unmarshal([]byte(payload), &parsed); err != nil {
		t.Fatalf("payload not JSON: %v", err)
	}
	if _, ok := parsed["usage"]; ok {
		t.Errorf("usage should be absent when metadata never arrived")
	}
	choice := parsed["choices"].([]any)[0].(map[string]any)
	if choice["finish_reason"] != "length" {
		t.Errorf("finish_reason = %v, want length", choice["finish_reason"])
	}
}

func TestStreamConverter_FormatChunkUsage(t *testing.T) {
	sc := newOpenAIStream(nil, "test-model")
	chunk := sc.formatChunk(map[string]any{}, "stop", &brtypes.TokenUsage{
		InputTokens:  awssdk.Int32(3),
		OutputTokens: awssdk.Int32(7),
		TotalTokens:  awssdk.Int32(10),
	})
	payload := strings.TrimSuffix(strings.TrimPrefix(chunk, "data: "), "\n\n")
	var parsed map[string]any
	if err := json.Unmarshal([]byte(payload), &parsed); err != nil {
		t.Fatalf("payload not JSON: %v", err)
	}
	usage, ok := parsed["usage"].(map[string]any)
	if !ok {
		t.Fatalf("usage missing: %v", parsed)
	}
	if usage["total_tokens"].(float64) != 10 {
		t.Errorf("total_tokens = %v", usage["total_tokens"])
	}
}
