package bedrock

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	brtypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers"
)

func TestCallObservationCoversBedrockSDKAndFirstChunk(t *testing.T) {
	var starts []llmclient.RequestInfo
	var ends, chunks []llmclient.ResponseInfo
	type contextKey struct{}
	p := &Provider{hooks: llmclient.Hooks{
		OnRequestStart: func(ctx context.Context, info llmclient.RequestInfo) context.Context {
			starts = append(starts, info)
			return context.WithValue(ctx, contextKey{}, true)
		},
		OnRequestEnd: func(_ context.Context, info llmclient.ResponseInfo) {
			ends = append(ends, info)
		},
		OnStreamFirstChunk: func(ctx context.Context, info llmclient.ResponseInfo) {
			started, _ := ctx.Value(contextKey{}).(bool)
			assert.True(t, started, "first-chunk hook must see the OnRequestStart context")
			chunks = append(chunks, info)
		},
	}}

	observation := p.beginCallObservation(t.Context(), "anthropic.claude", true)
	observation.end(http.StatusOK, nil)
	stream := observedStream(io.NopCloser(strings.NewReader("data: first\n\n")), observation)
	require.Empty(t, chunks)
	_, err := io.ReadAll(stream)
	require.NoError(t, err)
	require.Len(t, starts, 1)
	require.Len(t, ends, 1)
	require.Len(t, chunks, 1)
	require.Equal(t, llmclient.OperationChat, starts[0].Operation)
	require.Equal(t, converseEndpoint, starts[0].Endpoint)
	require.True(t, starts[0].Stream)
}

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
			assert.Equal(t, tc.wantRegion, region)
			assert.Equal(t, tc.wantEndpoint, endpoint)
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
			assert.Equal(t, tc.wantRuntime, runtimePlaneEndpoint(tc.in), "runtimePlaneEndpoint(%q)", tc.in)
			assert.Equal(t, tc.wantControl, controlPlaneEndpoint(tc.in), "controlPlaneEndpoint(%q)", tc.in)
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
			assert.Equal(t, tc.want, mapStopReason(tc.reason, tc.hasTools))
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
	require.NoError(t, err)
	assert.Equal(t, req.Model, awssdk.ToString(parts.modelID))
	require.Len(t, parts.system, 1)
	got := parts.system[0].(*brtypes.SystemContentBlockMemberText).Value
	assert.Equal(t, "You are concise", got)
	require.Len(t, parts.messages, 1)
	assert.Equal(t, brtypes.ConversationRoleUser, parts.messages[0].Role)
	require.NotNil(t, parts.infCfg)
	assert.Equal(t, int32(maxTokens), awssdk.ToInt32(parts.infCfg.MaxTokens))
	assert.Equal(t, float32(temp), awssdk.ToFloat32(parts.infCfg.Temperature))
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
	require.NoError(t, err)
	require.NotNil(t, parts.infCfg)
	got := awssdk.ToInt32(parts.infCfg.MaxTokens)
	assert.Equal(t, int32(256), got)
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
	require.NoError(t, err)
	got := awssdk.ToInt32(parts.infCfg.MaxTokens)
	assert.Equal(t, int32(128), got)
}

func TestBuildConverseParts_RejectsEmptyModel(t *testing.T) {
	_, err := buildConverseParts(&core.ChatRequest{Messages: []core.Message{{Role: "user", Content: "hi"}}})
	require.Error(t, err)

	var ge *core.GatewayError
	require.ErrorAs(t, err, &ge)
	require.Equal(t, core.ErrorTypeInvalidRequest, ge.Type)
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
	require.NoError(t, err)

	// Expect: user(text), assistant(2 tool_use), user(2 tool_result) — three messages.
	require.Len(t, parts.messages, 3)

	last := parts.messages[2]
	require.Equal(t, brtypes.ConversationRoleUser, last.Role)
	require.Len(t, last.Content, 2)

	for i, want := range []string{"call_1", "call_2"} {
		tr, ok := last.Content[i].(*brtypes.ContentBlockMemberToolResult)
		require.True(t, ok, "block %d not a ToolResult: %T", i, last.Content[i])
		got := awssdk.ToString(tr.Value.ToolUseId)
		assert.Equal(t, want, got)
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
	require.NoError(t, err)
	require.NotNil(t, parts.infCfg)
	require.NotNil(t, parts.infCfg.TopP)
	got := awssdk.ToFloat32(parts.infCfg.TopP)
	assert.Equal(t, float32(0.7), got)
}

func TestBuildConverseParts_TopPFromTypedField(t *testing.T) {
	topP := 0.8
	req := &core.ChatRequest{
		Model:    "anthropic.claude-3-5-haiku-20241022-v1:0",
		Messages: []core.Message{{Role: "user", Content: "hi"}},
		TopP:     &topP,
	}
	parts, err := buildConverseParts(req)
	require.NoError(t, err)
	require.NotNil(t, parts.infCfg)
	require.NotNil(t, parts.infCfg.TopP)
	got := awssdk.ToFloat32(parts.infCfg.TopP)
	assert.Equal(t, float32(0.8), got)
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
	require.NoError(t, err)
	require.NotNil(t, parts.infCfg)
	require.NotNil(t, parts.infCfg.TopP)
	got := awssdk.ToFloat32(parts.infCfg.TopP)
	assert.Equal(t, float32(0.8), got)
}

func TestBuildConverseParts_RejectsMaxTokensOverflow(t *testing.T) {
	overflow := int(int64(1) << 33) // 2^33, fits in int64 but not int32
	req := &core.ChatRequest{
		Model:     "anthropic.claude-3-5-haiku-20241022-v1:0",
		MaxTokens: &overflow,
		Messages:  []core.Message{{Role: "user", Content: "hi"}},
	}
	_, err := buildConverseParts(req)
	require.Error(t, err)

	var ge *core.GatewayError
	require.ErrorAs(t, err, &ge)
	require.Equal(t, core.ErrorTypeInvalidRequest, ge.Type)
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
	require.NoError(t, err)

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
	assert.Equal(t, []string{"c1", "c2"}, firstBatch, "first turn tool result IDs (aliasing bug overwrote them)")
	assert.Equal(t, []string{"c3"}, secondBatch, "second turn tool result IDs")
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
	require.NoError(t, err)
	require.Len(t, parts.messages, 3)

	last := parts.messages[2]
	require.Equal(t, brtypes.ConversationRoleUser, last.Role)

	// Expect [ToolResult, Text] in the merged user message.
	require.Len(t, last.Content, 2)
	_, ok := last.Content[0].(*brtypes.ContentBlockMemberToolResult)
	assert.True(t, ok, "first block should be ToolResult, got %T", last.Content[0])

	tb, ok := last.Content[1].(*brtypes.ContentBlockMemberText)
	require.True(t, ok, "second block should be Text, got %T", last.Content[1])
	assert.Equal(t, "thanks!", tb.Value)
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
	require.NoError(t, err)
	require.Len(t, parts.messages, 3)

	// Assistant message should carry a ToolUse content block
	asst := parts.messages[1]
	require.Equal(t, brtypes.ConversationRoleAssistant, asst.Role)

	tu, ok := asst.Content[0].(*brtypes.ContentBlockMemberToolUse)
	require.True(t, ok, "expected tool use block, got %T", asst.Content[0])
	assert.Equal(t, "tool_call_1", awssdk.ToString(tu.Value.ToolUseId))

	// Tool result message must be sent as user role with ContentBlockMemberToolResult
	toolMsg := parts.messages[2]
	require.Equal(t, brtypes.ConversationRoleUser, toolMsg.Role)

	tr, ok := toolMsg.Content[0].(*brtypes.ContentBlockMemberToolResult)
	require.True(t, ok, "expected tool result block, got %T", toolMsg.Content[0])
	assert.Equal(t, "tool_call_1", awssdk.ToString(tr.Value.ToolUseId))
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
			require.NoError(t, err)

			if tc.wantNil {
				require.Nil(t, cfg)
				return
			}
			require.NotNil(t, cfg)

			gotName := "<nil>"
			switch cfg.ToolChoice.(type) {
			case *brtypes.ToolChoiceMemberAuto:
				gotName = "Auto"
			case *brtypes.ToolChoiceMemberAny:
				gotName = "Any"
			case *brtypes.ToolChoiceMemberTool:
				gotName = "Tool"
			}
			assert.Equal(t, tc.wantChoice, gotName)
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
	assert.Equal(t, providerName, resp.Provider)
	require.Len(t, resp.Choices, 1)
	assert.Equal(t, "stop", resp.Choices[0].FinishReason)
	got := core.ExtractTextContent(resp.Choices[0].Message.Content)
	assert.Equal(t, "Hello there", got)
	assert.Equal(t, 10, resp.Usage.PromptTokens)
	assert.Equal(t, 20, resp.Usage.CompletionTokens)
	assert.Equal(t, 30, resp.Usage.TotalTokens)
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
	require.Equal(t, "tool_calls", resp.Choices[0].FinishReason)

	calls := resp.Choices[0].Message.ToolCalls
	require.Len(t, calls, 1)
	assert.Equal(t, "get_weather", calls[0].Function.Name)

	var args map[string]any
	err := json.Unmarshal([]byte(calls[0].Function.Arguments), &args)
	require.NoError(t, err, "arguments not valid JSON: %s", calls[0].Function.Arguments)
	assert.Equal(t, "Paris", args["city"])
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
	require.NoError(t, p.configErr)
	require.NotNil(t, p.runtime)
	assert.Equal(t, "us-east-1", p.region)
	err := p.ready()
	assert.NoError(t, err)
}

func TestRegistration(t *testing.T) {
	assert.Equal(t, providerName, Registration.Type)
	assert.True(t, Registration.Discovery.AllowAPIKeyless)
	require.NotNil(t, Registration.New)
}

func TestStreamConverter_FormatChunkContent(t *testing.T) {
	sc := newOpenAIStream(nil, "test-model")
	chunk := sc.formatChunk(map[string]any{"content": "Hi"}, nil, nil)
	require.True(t, strings.HasPrefix(chunk, "data: "))
	require.True(t, strings.HasSuffix(chunk, "\n\n"), "malformed SSE framing: %q", chunk)

	payload := strings.TrimSuffix(strings.TrimPrefix(chunk, "data: "), "\n\n")
	var parsed map[string]any
	err := json.Unmarshal([]byte(payload), &parsed)
	require.NoError(t, err)
	assert.Equal(t, "chat.completion.chunk", parsed["object"])
	assert.Equal(t, "test-model", parsed["model"])
	assert.Equal(t, providerName, parsed["provider"])

	choices, _ := parsed["choices"].([]any)
	require.Len(t, choices, 1)

	choice := choices[0].(map[string]any)
	delta := choice["delta"].(map[string]any)
	assert.Equal(t, "Hi", delta["content"])
}

// TestStreamConverter_DeferredFinishWithUsage asserts that messageStop alone
// does not emit a finish chunk; the chunk is emitted on metadata and carries
// both finish_reason and usage so include_usage callers see token counts.
func TestStreamConverter_DeferredFinishWithUsage(t *testing.T) {
	sc := newOpenAIStream(nil, "test-model")

	sc.handleEvent(&brtypes.ConverseStreamOutputMemberMessageStop{
		Value: brtypes.MessageStopEvent{StopReason: brtypes.StopReasonEndTurn},
	})
	require.Empty(t, sc.buf)
	require.True(t, sc.havePendingStop)
	require.False(t, sc.finishSent)

	sc.handleEvent(&brtypes.ConverseStreamOutputMemberMetadata{
		Value: brtypes.ConverseStreamMetadataEvent{
			Usage: &brtypes.TokenUsage{
				InputTokens:  awssdk.Int32(11),
				OutputTokens: awssdk.Int32(13),
				TotalTokens:  awssdk.Int32(24),
			},
		},
	})
	require.True(t, sc.finishSent)

	payload := strings.TrimSuffix(strings.TrimPrefix(string(sc.buf), "data: "), "\n\n")
	var parsed map[string]any
	err := json.Unmarshal([]byte(payload), &parsed)
	require.NoError(t, err, "payload not JSON: %q", payload)

	choices := parsed["choices"].([]any)
	choice := choices[0].(map[string]any)
	assert.Equal(t, "stop", choice["finish_reason"])

	usage, ok := parsed["usage"].(map[string]any)
	require.True(t, ok, "usage missing from finish chunk: %v", parsed)
	assert.Equal(t, float64(24), usage["total_tokens"])
}

// TestStreamConverter_DeferredFinishWithoutMetadata asserts that we still
// emit a finish chunk if the stream closes before a metadata event — usage
// is absent in that case but finish_reason must not be swallowed.
func TestStreamConverter_DeferredFinishWithoutMetadata(t *testing.T) {
	sc := newOpenAIStream(nil, "test-model")
	sc.handleEvent(&brtypes.ConverseStreamOutputMemberMessageStop{
		Value: brtypes.MessageStopEvent{StopReason: brtypes.StopReasonMaxTokens},
	})
	require.False(t, sc.finishSent)

	sc.flushFinish()
	require.True(t, sc.finishSent)

	payload := strings.TrimSuffix(strings.TrimPrefix(string(sc.buf), "data: "), "\n\n")
	var parsed map[string]any
	err := json.Unmarshal([]byte(payload), &parsed)
	require.NoError(t, err)
	_, ok := parsed["usage"]
	assert.False(t, ok)

	choice := parsed["choices"].([]any)[0].(map[string]any)
	assert.Equal(t, "length", choice["finish_reason"])
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
	err := json.Unmarshal([]byte(payload), &parsed)
	require.NoError(t, err)

	usage, ok := parsed["usage"].(map[string]any)
	require.True(t, ok, "usage missing: %v", parsed)
	assert.Equal(t, float64(10), usage["total_tokens"])
}

func TestGatewayCachePointUsesJSONBooleanAndNeverCreatesEmptyMessage(t *testing.T) {
	fields := core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{
		core.GatewayCachePointField: json.RawMessage("  true\n"),
	})
	require.True(t, isGatewayCachePoint(fields))

	falseFields := core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{
		core.GatewayCachePointField: json.RawMessage(`false`),
	})
	require.False(t, isGatewayCachePoint(falseFields))

	_, messages, err := convertMessages([]core.Message{{Role: "assistant", ExtraFields: fields}})
	require.NoError(t, err)
	require.Empty(t, messages)
}

type testBedrockAPIError struct{ code, message string }

func (e testBedrockAPIError) Error() string        { return e.code + ": " + e.message }
func (e testBedrockAPIError) ErrorCode() string    { return e.code }
func (e testBedrockAPIError) ErrorMessage() string { return e.message }

func TestCachePointFallbackIsNarrowAndLossless(t *testing.T) {
	require.True(t, isCachePointValidationError(testBedrockAPIError{"ValidationException", "cache point below minimum tokens"}))
	require.False(t, isCachePointValidationError(testBedrockAPIError{"ValidationException", "invalid tool schema"}))

	parts := converseParts{
		system: []brtypes.SystemContentBlock{
			&brtypes.SystemContentBlockMemberText{Value: "system"},
			&brtypes.SystemContentBlockMemberCachePoint{Value: brtypes.CachePointBlock{Type: brtypes.CachePointTypeDefault}},
		},
		messages: []brtypes.Message{{Role: brtypes.ConversationRoleUser, Content: []brtypes.ContentBlock{
			&brtypes.ContentBlockMemberText{Value: "user"},
			&brtypes.ContentBlockMemberCachePoint{Value: brtypes.CachePointBlock{Type: brtypes.CachePointTypeDefault}},
		}}},
	}
	clean := withoutCachePoints(parts)
	require.False(t, partsHaveCachePoints(clean))
	require.Len(t, clean.system, 1)
	require.Len(t, clean.messages, 1)
	require.Len(t, clean.messages[0].Content, 1, "cache-point removal damaged request content")
	require.True(t, partsHaveCachePoints(parts))
}

func TestStreamConverter_FormatChunkForwardsCacheUsage(t *testing.T) {
	cases := []struct {
		name       string
		read       *int32
		write      *int32
		wantRead   int // -1 means the key must be absent
		wantCreate int
	}{
		{"both present", awssdk.Int32(5000), awssdk.Int32(1200), 5000, 1200},
		{"read only", awssdk.Int32(5000), nil, 5000, -1},
		{"write only", nil, awssdk.Int32(1200), -1, 1200},
		{"both absent", nil, nil, -1, -1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sc := newOpenAIStream(nil, "test-model")
			chunk := sc.formatChunk(map[string]any{}, "stop", &brtypes.TokenUsage{
				InputTokens:           awssdk.Int32(3),
				OutputTokens:          awssdk.Int32(7),
				TotalTokens:           awssdk.Int32(10),
				CacheReadInputTokens:  tc.read,
				CacheWriteInputTokens: tc.write,
			})
			payload := strings.TrimSuffix(strings.TrimPrefix(chunk, "data: "), "\n\n")
			var parsed map[string]any
			err := json.Unmarshal([]byte(payload), &parsed)
			require.NoError(t, err)

			usage, ok := parsed["usage"].(map[string]any)
			require.True(t, ok, "usage missing: %v", parsed)

			assertCacheKey(t, usage, "cache_read_input_tokens", tc.wantRead)
			assertCacheKey(t, usage, "cache_creation_input_tokens", tc.wantCreate)
		})
	}
}

// assertCacheKey checks a JSON-decoded usage map: want < 0 asserts the key is
// absent, otherwise the key must be present with that value.
func assertCacheKey(t *testing.T, usage map[string]any, key string, want int) {
	t.Helper()
	if want < 0 {
		assert.NotContains(t, usage, key)
		return
	}
	assert.Equal(t, float64(want), usage[key], "%s", key)
}

func TestBedrockUsageExtrasCacheKeys(t *testing.T) {
	cases := []struct {
		name       string
		read       *int32
		write      *int32
		wantKeys   map[string]int
		absentKeys []string
	}{
		{
			name:     "both present",
			read:     awssdk.Int32(5000),
			write:    awssdk.Int32(1200),
			wantKeys: map[string]int{"cache_read_input_tokens": 5000, "cache_creation_input_tokens": 1200, "cache_write_input_tokens": 1200},
		},
		{
			name:       "read only",
			read:       awssdk.Int32(5000),
			wantKeys:   map[string]int{"cache_read_input_tokens": 5000},
			absentKeys: []string{"cache_creation_input_tokens", "cache_write_input_tokens"},
		},
		{
			name:       "write only",
			write:      awssdk.Int32(1200),
			wantKeys:   map[string]int{"cache_creation_input_tokens": 1200, "cache_write_input_tokens": 1200},
			absentKeys: []string{"cache_read_input_tokens"},
		},
		{name: "both absent"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := bedrockUsageExtras(&brtypes.TokenUsage{
				CacheReadInputTokens:  tc.read,
				CacheWriteInputTokens: tc.write,
			})
			if len(tc.wantKeys) == 0 {
				require.Nil(t, out)
				return
			}
			for key, want := range tc.wantKeys {
				assert.Equal(t, want, out[key])
			}
			for _, key := range tc.absentKeys {
				assert.NotContains(t, out, key)
			}
		})
	}
}
