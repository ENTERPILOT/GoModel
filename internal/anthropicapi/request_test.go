package anthropicapi

import (
	"encoding/json"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func mustDecode(t *testing.T, body string) *MessagesRequest {
	t.Helper()
	req, err := DecodeMessagesRequest([]byte(body))
	require.NoError(t, err)

	return req
}

func TestDecodeMessagesRequest(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantErr bool
	}{
		{name: "valid", body: `{"model":"m","max_tokens":10,"messages":[]}`},
		{name: "empty", body: "  ", wantErr: true},
		{name: "malformed", body: `{"model":`, wantErr: true},
		{name: "trailing object", body: `{"model":"m","max_tokens":10,"messages":[]}{"x":1}`, wantErr: true},
		{name: "trailing garbage", body: `{"model":"m","max_tokens":10,"messages":[]} oops`, wantErr: true},
		{name: "trailing brace", body: `{"model":"m","max_tokens":10,"messages":[]}}`, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := DecodeMessagesRequest([]byte(tc.body))
			require.Equal(t, tc.wantErr, err != nil)
		})
	}
}

func TestToChatRequestValidation(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "missing model", body: `{"max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`},
		{name: "zero max_tokens", body: `{"model":"m","max_tokens":0,"messages":[{"role":"user","content":"hi"}]}`},
		{name: "empty messages", body: `{"model":"m","max_tokens":10,"messages":[]}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ToChatRequest(mustDecode(t, tc.body))
			require.Error(t, err)
			_, ok := err.(*core.GatewayError)
			require.True(t, ok)
		})
	}
}

func TestToChatRequestRejectsInvalidShapes(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "typo role", body: `{"model":"m","max_tokens":10,"messages":[{"role":"assisstant","content":"hi"}]}`},
		{name: "malformed system", body: `{"model":"m","max_tokens":10,"system":42,"messages":[{"role":"user","content":"hi"}]}`},
		{name: "non-text system block", body: `{"model":"m","max_tokens":10,"system":[{"type":"image"}],"messages":[{"role":"user","content":"hi"}]}`},
		{name: "malformed tool_result content", body: `{"model":"m","max_tokens":10,"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":42}]}]}`},
		{name: "unsupported tool_result block", body: `{"model":"m","max_tokens":10,"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":[{"type":"browser_state"}]}]}]}`},
		{name: "tool_result document without source", body: `{"model":"m","max_tokens":10,"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":[{"type":"document"}]}]}]}`},
		{name: "document content source without content", body: `{"model":"m","max_tokens":10,"messages":[{"role":"user","content":[{"type":"document","source":{"type":"content"}}]}]}`},
		{name: "tool_result image without source", body: `{"model":"m","max_tokens":10,"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":[{"type":"image"}]}]}]}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ToChatRequest(mustDecode(t, tc.body))
			require.Error(t, err)

			gatewayErr, ok := err.(*core.GatewayError)
			require.True(t, ok)
			require.Equal(t, core.ErrorTypeInvalidRequest, gatewayErr.Type)
		})
	}
}

func TestToChatRequestBasic(t *testing.T) {
	chat, err := ToChatRequest(mustDecode(t, `{
		"model":"claude-test","max_tokens":256,"temperature":0.5,"stream":true,
		"system":"be brief",
		"messages":[{"role":"user","content":"hello"}]
	}`))
	require.NoError(t, err)
	assert.Equal(t, "claude-test", chat.Model)
	require.NotNil(t, chat.MaxTokens)
	assert.Equal(t, 256, *chat.MaxTokens)
	require.NotNil(t, chat.Temperature)
	assert.Equal(t, 0.5, *chat.Temperature)
	assert.True(t, chat.Stream)
	require.NotNil(t, chat.StreamOptions)
	assert.True(t, chat.StreamOptions.IncludeUsage)
	require.Len(t, chat.Messages, 2)
	assert.Equal(t, "system", chat.Messages[0].Role)
	assert.Equal(t, "be brief", chat.Messages[0].Content, "system message = %+v", chat.Messages[0])
	assert.Equal(t, "user", chat.Messages[1].Role)
	assert.Equal(t, "hello", chat.Messages[1].Content, "user message = %+v", chat.Messages[1])
}

func TestToChatRequestSystemMessageInMessages(t *testing.T) {
	// System messages in the messages array should be extracted and prepended to the system prompt
	chat, err := ToChatRequest(mustDecode(t, `{
		"model":"m","max_tokens":10,
		"messages":[
			{"role":"system","content":"system prompt"},
			{"role":"user","content":"hello"}
		]
	}`))
	require.NoError(t, err)
	require.Len(t, chat.Messages, 2)
	assert.Equal(t, "system", chat.Messages[0].Role)

	systemContent, ok := chat.Messages[0].Content.(string)
	require.True(t, ok, "system content is not a string: %T", chat.Messages[0].Content)
	assert.Equal(t, "system prompt", systemContent)
	assert.Equal(t, "user", chat.Messages[1].Role)
	assert.Equal(t, "hello", chat.Messages[1].Content, "user message = %+v", chat.Messages[1])
}

func TestToChatRequestSystemMessageCombined(t *testing.T) {
	// A top-level system field becomes the leading system message; system
	// messages in the messages array keep their own position and identity.
	chat, err := ToChatRequest(mustDecode(t, `{
		"model":"m","max_tokens":10,
		"system":"top-level system",
		"messages":[
			{"role":"system","content":"message system"},
			{"role":"user","content":"hello"}
		]
	}`))
	require.NoError(t, err)
	require.Len(t, chat.Messages, 3)
	assert.Equal(t, "system", chat.Messages[0].Role)
	assert.Equal(t, "top-level system", chat.Messages[0].Content, "first message = %+v, want top-level system", chat.Messages[0])
	assert.Equal(t, "system", chat.Messages[1].Role)
	assert.Equal(t, "message system", chat.Messages[1].Content, "second message = %+v, want message system", chat.Messages[1])
	assert.Equal(t, "user", chat.Messages[2].Role)
	assert.Equal(t, "hello", chat.Messages[2].Content, "user message = %+v", chat.Messages[2])
}

func TestToChatRequestSystemMessageKeepsPositionAndCacheControl(t *testing.T) {
	// Mid-conversation system messages (Claude Code system reminders) must
	// keep their position and block-level cache_control breakpoints so
	// provider prompt caching keeps working through the gateway.
	chat, err := ToChatRequest(mustDecode(t, `{
		"model":"m","max_tokens":10,
		"system":[{"type":"text","text":"base","cache_control":{"type":"ephemeral"}}],
		"messages":[
			{"role":"user","content":"hi"},
			{"role":"assistant","content":"hello"},
			{"role":"system","content":[{"type":"text","text":"reminder","cache_control":{"type":"ephemeral"}}]}
		]
	}`))
	require.NoError(t, err)
	require.Len(t, chat.Messages, 4)

	last := chat.Messages[3]
	require.Equal(t, "system", last.Role)

	parts, ok := last.Content.([]core.ContentPart)
	require.True(t, ok, "last system content is %T, want []core.ContentPart", last.Content)
	require.Len(t, parts, 1)
	require.Equal(t, "reminder", parts[0].Text)
	got := string(parts[0].ExtraFields.Lookup("cache_control"))
	assert.Equal(t, `{"type":"ephemeral"}`, got)
}

func TestToChatRequestImageBlock(t *testing.T) {
	chat, err := ToChatRequest(mustDecode(t, `{
		"model":"m","max_tokens":10,
		"messages":[{"role":"user","content":[
			{"type":"text","text":"look"},
			{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AAAA"}}
		]}]
	}`))
	require.NoError(t, err)

	parts, ok := chat.Messages[0].Content.([]core.ContentPart)
	require.True(t, ok, "content type = %T, want []core.ContentPart", chat.Messages[0].Content)
	require.Len(t, parts, 2)
	require.Equal(t, "image_url", parts[1].Type)
	got := parts[1].ImageURL.URL
	assert.Equal(t, "data:image/png;base64,AAAA", got)
}

func TestToChatRequestToolUseAndResult(t *testing.T) {
	chat, err := ToChatRequest(mustDecode(t, `{
		"model":"m","max_tokens":10,
		"messages":[
			{"role":"assistant","content":[
				{"type":"text","text":"calling"},
				{"type":"tool_use","id":"tu_1","name":"get_weather","input":{"city":"paris"}}
			]},
			{"role":"user","content":[
				{"type":"tool_result","tool_use_id":"tu_1","content":"sunny"},
				{"type":"text","text":"thanks"}
			]}
		]
	}`))
	require.NoError(t, err)
	require.Len(t, chat.Messages, 3)

	assistant := chat.Messages[0]
	require.Equal(t, "assistant", assistant.Role)
	require.Len(t, assistant.ToolCalls, 1, "assistant = %+v", assistant)
	assert.Equal(t, "tu_1", assistant.ToolCalls[0].ID)
	assert.Equal(t, "get_weather", assistant.ToolCalls[0].Function.Name, "tool call = %+v", assistant.ToolCalls[0])
	assert.Equal(t, `{"city":"paris"}`, assistant.ToolCalls[0].Function.Arguments)

	tool := chat.Messages[1]
	assert.Equal(t, "tool", tool.Role)
	assert.Equal(t, "tu_1", tool.ToolCallID)
	assert.Equal(t, "sunny", tool.Content, "tool message = %+v", tool)
	assert.Equal(t, "user", chat.Messages[2].Role)
	assert.Equal(t, "thanks", chat.Messages[2].Content, "user message = %+v", chat.Messages[2])
}

func TestToChatRequestTools(t *testing.T) {
	// An explicit type of "custom" is accepted alongside the typeless form.
	chat, err := ToChatRequest(mustDecode(t, `{
		"model":"m","max_tokens":10,
		"messages":[{"role":"user","content":"hi"}],
		"tools":[{"type":"custom","name":"get_weather","description":"weather","input_schema":{"type":"object","properties":{}}}],
		"tool_choice":{"type":"tool","name":"get_weather"}
	}`))
	require.NoError(t, err)
	require.Len(t, chat.Tools, 1)
	assert.Equal(t, "function", chat.Tools[0]["type"])

	fn, _ := chat.Tools[0]["function"].(map[string]any)
	assert.Equal(t, "get_weather", fn["name"])
	assert.NotNil(t, fn["parameters"], "function = %+v", fn)

	choice, ok := chat.ToolChoice.(map[string]any)
	require.True(t, ok)
	require.Equal(t, "function", choice["type"], "tool_choice = %#v", chat.ToolChoice)
}

func TestToChatRequestRejectsServerTool(t *testing.T) {
	_, err := ToChatRequest(mustDecode(t, `{
		"model":"m","max_tokens":10,
		"messages":[{"role":"user","content":"hi"}],
		"tools":[{"type":"web_search_20250305","name":"web_search"}]
	}`))
	require.Error(t, err)

	gatewayErr, ok := err.(*core.GatewayError)
	require.True(t, ok)
	require.Equal(t, core.ErrorTypeInvalidRequest, gatewayErr.Type)
}

func TestToChatRequestToolChoiceMapping(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  any
	}{
		{name: "auto", input: `{"type":"auto"}`, want: "auto"},
		{name: "any", input: `{"type":"any"}`, want: "required"},
		{name: "none", input: `{"type":"none"}`, want: "none"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			chat, err := ToChatRequest(mustDecode(t, `{
				"model":"m","max_tokens":10,
				"messages":[{"role":"user","content":"hi"}],
				"tool_choice":`+tc.input+`}`))
			require.NoError(t, err)
			assert.Equal(t, tc.want, chat.ToolChoice)
		})
	}
}

func TestToChatRequestDisableParallelToolUse(t *testing.T) {
	chat, err := ToChatRequest(mustDecode(t, `{
		"model":"m","max_tokens":10,
		"messages":[{"role":"user","content":"hi"}],
		"tool_choice":{"type":"auto","disable_parallel_tool_use":true}
	}`))
	require.NoError(t, err)

	require.NotNil(t, chat.ParallelToolCalls)
	assert.False(t, *chat.ParallelToolCalls)
}

func TestToChatRequestThinking(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		effort string
	}{
		{name: "low budget", input: `{"type":"enabled","budget_tokens":5000}`, effort: "low"},
		{name: "medium budget", input: `{"type":"enabled","budget_tokens":12000}`, effort: "medium"},
		{name: "high budget", input: `{"type":"enabled","budget_tokens":24000}`, effort: "high"},
		{name: "adaptive", input: `{"type":"adaptive"}`, effort: "medium"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			chat, err := ToChatRequest(mustDecode(t, `{
				"model":"m","max_tokens":30000,
				"messages":[{"role":"user","content":"hi"}],
				"thinking":`+tc.input+`}`))
			require.NoError(t, err)
			require.NotNil(t, chat.Reasoning)
			assert.Equal(t, tc.effort, chat.Reasoning.Effort)
		})
	}
}

func TestToChatRequestExtraFields(t *testing.T) {
	chat, err := ToChatRequest(mustDecode(t, `{
		"model":"m","max_tokens":10,
		"messages":[{"role":"user","content":"hi"}],
		"stop_sequences":["STOP"],"top_p":0.9,"top_k":40,
		"metadata":{"user_id":"u-123"}
	}`))
	require.NoError(t, err)
	raw := chat.ExtraFields.Lookup("stop")
	assert.Equal(t, `["STOP"]`, string(raw))

	// top_p and user have typed ChatRequest fields; they must land there so
	// internal consumers of the typed fields (Responses lowering, provider
	// adapters) see them, and must not also ride in ExtraFields.
	require.NotNil(t, chat.TopP)
	assert.Equal(t, 0.9, *chat.TopP)
	assert.Equal(t, "u-123", chat.User)

	for _, key := range []string{"top_p", "user"} {
		raw := chat.ExtraFields.Lookup(key)
		assert.Empty(t, raw, "ExtraFields[%q] should be absent, typed field only", key)
	}
	// top_k has no portable OpenAI-compatible equivalent and OpenAI-family
	// providers reject unknown request fields; it must be dropped, not carried.
	raw = chat.ExtraFields.Lookup("top_k")
	assert.Empty(t, raw)
}

func TestToChatRequestToolResultWithImage(t *testing.T) {
	chat, err := ToChatRequest(mustDecode(t, `{
		"model":"m","max_tokens":10,
		"messages":[
			{"role":"assistant","content":[{"type":"tool_use","id":"tu_1","name":"screenshot","input":{}}]},
			{"role":"user","content":[
				{"type":"tool_result","tool_use_id":"tu_1","content":[
					{"type":"text","text":"captured"},
					{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aGVsbG8="}}
				]}
			]}
		]
	}`))
	require.NoError(t, err)
	require.Len(t, chat.Messages, 2)

	tool := chat.Messages[1]
	require.Equal(t, "tool", tool.Role)
	require.Equal(t, "tu_1", tool.ToolCallID, "tool message = %+v", tool)

	parts, ok := tool.Content.([]core.ContentPart)
	require.True(t, ok)
	require.Len(t, parts, 2)
	assert.Equal(t, "text", parts[0].Type)
	assert.Equal(t, "captured", parts[0].Text, "parts[0] = %+v, want text part", parts[0])
	assert.Equal(t, "image_url", parts[1].Type)
	require.NotNil(t, parts[1].ImageURL)
	assert.Equal(t, "data:image/png;base64,aGVsbG8=", parts[1].ImageURL.URL, "parts[1] = %+v, want image_url data URL", parts[1])
	// The image inside the tool result is priced too. Its payload here is not
	// a real image, so it is charged the upper bound rather than ignored.
	got := EstimateInputTokens(mustDecode(t, `{"model":"m","max_tokens":10,"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu_1","content":[{"type":"text","text":"captured"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aGVsbG8="}}]}]}]}`))
	assert.Greater(t, got, imageMaxTokens)
}

func TestToChatRequestToolResultTextOnlyStaysString(t *testing.T) {
	chat, err := ToChatRequest(mustDecode(t, `{
		"model":"m","max_tokens":10,
		"messages":[{"role":"user","content":[
			{"type":"tool_result","tool_use_id":"tu_1","content":[{"type":"text","text":"a"},{"type":"text","text":"b"}]}
		]}]
	}`))
	require.NoError(t, err)
	got := chat.Messages[0].Content
	assert.Equal(t, "a\nb", got)
}

func TestToChatRequestPreservesAnthropicCacheControls(t *testing.T) {
	chat, err := ToChatRequest(mustDecode(t, `{
		"model":"claude-test","max_tokens":64,
		"cache_control":{"type":"ephemeral"},
		"system":[{"type":"text","text":"stable system","cache_control":{"type":"ephemeral"}}],
		"messages":[
			{"role":"user","content":[{"type":"text","text":"stable prefix","cache_control":{"type":"ephemeral"}}]},
			{"role":"assistant","content":[{"type":"tool_use","id":"tool-1","name":"lookup","input":{"q":"x"},"cache_control":{"type":"ephemeral"}}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"tool-1","content":"result","cache_control":{"type":"ephemeral"}}]}
		],
		"tools":[{"name":"lookup","input_schema":{"type":"object"},"cache_control":{"type":"ephemeral"}}]
	}`))
	require.NoError(t, err)

	want := `{"type":"ephemeral"}`
	got := string(chat.ExtraFields.Lookup("cache_control"))
	assert.Equal(t, want, got)
	require.Len(t, chat.Messages, 4)

	for _, index := range []int{0, 1} {
		parts, ok := chat.Messages[index].Content.([]core.ContentPart)
		require.True(t, ok)
		require.Len(t, parts, 1, "messages[%d].Content = %T %#v, want one structured part", index, chat.Messages[index].Content, chat.Messages[index].Content)
		got := string(parts[0].ExtraFields.Lookup("cache_control"))
		assert.Equal(t, want, got)
	}
	got = string(chat.Messages[2].ToolCalls[0].ExtraFields.Lookup("cache_control"))
	assert.Equal(t, want, got)
	got = string(chat.Messages[3].ExtraFields.Lookup("cache_control"))
	assert.Equal(t, want, got)

	toolCache, ok := chat.Tools[0]["cache_control"].(map[string]any)
	require.True(t, ok, "tool cache_control = %#v, want ephemeral object", chat.Tools[0]["cache_control"])
	assert.Equal(t, "ephemeral", toolCache["type"])
}

func TestToChatRequestRejectsInvalidCacheControl(t *testing.T) {
	_, err := ToChatRequest(mustDecode(t, `{
		"model":"m","max_tokens":10,
		"messages":[{"role":"user","content":[{"type":"text","text":"hi","cache_control":"invalid"}]}]
	}`))
	require.ErrorContains(t, err, "cache_control must be an object")
}

func TestToChatRequestRejectsInvalidTopLevelCacheControl(t *testing.T) {
	_, err := ToChatRequest(mustDecode(t, `{
		"model":"m","max_tokens":10,"cache_control":"invalid",
		"messages":[{"role":"user","content":"hi"}]
	}`))
	require.ErrorContains(t, err, "cache_control must be an object")
}

func TestToChatRequestRejectsUnsupportedContentBlock(t *testing.T) {
	_, err := ToChatRequest(mustDecode(t, `{
		"model":"m","max_tokens":10,
		"messages":[{"role":"user","content":[
			{"type":"text","text":"summarize"},
			{"type":"container_upload","file_id":"file_1"}
		]}]
	}`))
	require.Error(t, err)

	gatewayErr, ok := err.(*core.GatewayError)
	require.True(t, ok)
	require.Equal(t, core.ErrorTypeInvalidRequest, gatewayErr.Type)
	assert.Contains(t, gatewayErr.Message, "container_upload")
}

func TestToChatRequestDropsThinkingBlocks(t *testing.T) {
	// thinking blocks are assistant-side artifacts and are dropped, not rejected.
	chat, err := ToChatRequest(mustDecode(t, `{
		"model":"m","max_tokens":10,
		"messages":[{"role":"assistant","content":[
			{"type":"thinking","thinking":"hmm"},
			{"type":"text","text":"answer"}
		]}]
	}`))
	require.NoError(t, err)
	require.Len(t, chat.Messages, 1)
	require.Equal(t, "answer", chat.Messages[0].Content)
}

func TestEstimateInputTokens(t *testing.T) {
	req := mustDecode(t, `{
		"model":"m","max_tokens":10,
		"system":"you are helpful",
		"messages":[{"role":"user","content":"count these characters please"}]
	}`)
	got := EstimateInputTokens(req)
	require.Greater(t, got, 0)
	assert.Equal(t, 0, EstimateInputTokens(nil))
}

func TestToChatRequestRoundTripsAsJSON(t *testing.T) {
	// The translated request must marshal cleanly for the response cache key.
	chat, err := ToChatRequest(mustDecode(t, `{
		"model":"m","max_tokens":10,
		"messages":[{"role":"user","content":"hi"}]
	}`))
	require.NoError(t, err)
	_, err = json.Marshal(chat)
	require.NoError(t, err)
}

func TestEstimateChatInputTokens(t *testing.T) {
	got := EstimateChatInputTokens(nil)
	require.Equal(t, 0, got)

	plain := EstimateChatInputTokens(&core.ChatRequest{
		Messages: []core.Message{
			{Role: "system", Content: "You are terse."},
			{Role: "user", Content: "What is 2+2?"},
		},
	})
	// Two short messages: their text plus the per-message framing.
	assert.GreaterOrEqual(t, plain, 2*messageOverhead+7)
	assert.LessOrEqual(t, plain, 2*messageOverhead+20)

	withTools := EstimateChatInputTokens(&core.ChatRequest{
		Messages: []core.Message{{
			Role:      "assistant",
			ToolCalls: []core.ToolCall{{Function: core.FunctionCall{Name: "weather", Arguments: `{"city":"Paris"}`}}},
		}},
		Tools: []map[string]any{{"type": "function", "function": map[string]any{"name": "weather"}}},
	})
	// A tool definition brings Anthropic's tool-use system prompt with it.
	assert.GreaterOrEqual(t, withTools, toolUseSystemPrompt+toolDefinitionOverhead+toolBlockOverhead)
}

func TestToChatRequestDocumentBlocks(t *testing.T) {
	tests := []struct {
		name     string
		block    string
		wantType string
		wantText string
		check    func(t *testing.T, part core.ContentPart)
	}{
		{
			name:     "base64 pdf",
			block:    `{"type":"document","title":"report.pdf","source":{"type":"base64","media_type":"application/pdf","data":"JVBERi0="},"cache_control":{"type":"ephemeral"}}`,
			wantType: "file",
			check: func(t *testing.T, part core.ContentPart) {
				assert.Equal(t, "data:application/pdf;base64,JVBERi0=", part.File.FileData)
				assert.Equal(t, "report.pdf", part.File.Filename, "file = %+v", part.File)
				got := string(part.ExtraFields.Lookup("cache_control"))
				assert.Equal(t, `{"type":"ephemeral"}`, got)
			},
		},
		{
			name:     "plain text",
			block:    `{"type":"document","source":{"type":"text","media_type":"text/plain","data":"hello"}}`,
			wantType: "file",
			check: func(t *testing.T, part core.ContentPart) {
				assert.Equal(t, "data:text/plain;base64,aGVsbG8=", part.File.FileData, "file = %+v", part.File)
			},
		},
		{
			name:     "url",
			block:    `{"type":"document","source":{"type":"url","url":"https://example.com/a.pdf"}}`,
			wantType: "file",
			check: func(t *testing.T, part core.ContentPart) {
				assert.Equal(t, "https://example.com/a.pdf", part.File.FileURL)
				assert.Empty(t, part.File.FileData, "file = %+v", part.File)
			},
		},
		{
			name:     "file id",
			block:    `{"type":"document","source":{"type":"file","file_id":"file_123"}}`,
			wantType: "file",
			check: func(t *testing.T, part core.ContentPart) {
				assert.Equal(t, "file_123", part.File.FileID)
				assert.Empty(t, part.File.FileData, "file = %+v", part.File)
			},
		},
		{
			name:     "custom content degrades to text",
			block:    `{"type":"document","title":"Notes","source":{"type":"content","content":[{"type":"text","text":"one"},{"type":"text","text":"two"}]}}`,
			wantType: "text",
			wantText: "Notes\n\none\ntwo",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			chat, err := ToChatRequest(mustDecode(t, `{"model":"m","max_tokens":10,"messages":[{"role":"user","content":[{"type":"text","text":"read"},`+tc.block+`]}]}`))
			require.NoError(t, err)

			if tc.wantType == "text" {
				// Text-only content collapses to a string.
				got := chat.Messages[0].Content
				require.Equal(t, "read\n"+tc.wantText, got)
				return
			}
			parts, ok := chat.Messages[0].Content.([]core.ContentPart)
			require.True(t, ok)
			require.Len(t, parts, 2)
			require.Equal(t, tc.wantType, parts[1].Type)

			tc.check(t, parts[1])
		})
	}
}

func TestToChatRequestDocumentInsideToolResult(t *testing.T) {
	chat, err := ToChatRequest(mustDecode(t, `{
		"model":"m","max_tokens":10,
		"messages":[{"role":"user","content":[
			{"type":"tool_result","tool_use_id":"tu_1","content":[
				{"type":"document","source":{"type":"base64","media_type":"application/pdf","data":"JVBERi0="}}
			]}
		]}]
	}`))
	require.NoError(t, err)

	parts, ok := chat.Messages[0].Content.([]core.ContentPart)
	require.True(t, ok)
	require.Len(t, parts, 1)
	require.Equal(t, "file", parts[0].Type)
	require.Equal(t, "data:application/pdf;base64,JVBERi0=", parts[0].File.FileData, "tool content = %#v, want one file part", chat.Messages[0].Content)
}

func TestToChatRequestSearchResultBlock(t *testing.T) {
	chat, err := ToChatRequest(mustDecode(t, `{
		"model":"m","max_tokens":10,
		"messages":[{"role":"user","content":[
			{"type":"tool_result","tool_use_id":"tu_1","content":[
				{"type":"search_result","source":"https://example.com/doc","title":"Doc","content":[{"type":"text","text":"body"}],"citations":{"enabled":true}}
			]}
		]}]
	}`))
	require.NoError(t, err)
	got := chat.Messages[0].Content
	assert.Equal(t, "Title: Doc\nSource: https://example.com/doc\n\nbody", got)
}

func TestToChatRequestToolResultIsError(t *testing.T) {
	chat, err := ToChatRequest(mustDecode(t, `{
		"model":"m","max_tokens":10,
		"messages":[{"role":"user","content":[
			{"type":"tool_result","tool_use_id":"tu_1","content":"boom","is_error":true,"cache_control":{"type":"ephemeral"}},
			{"type":"tool_result","tool_use_id":"tu_2","content":"fine"}
		]}]
	}`))
	require.NoError(t, err)
	got := string(chat.Messages[0].ExtraFields.ExtraContent(core.ExtraContentVendorAnthropic))
	assert.Equal(t, `{"is_error":true}`, got)
	got = string(chat.Messages[0].ExtraFields.Lookup("cache_control"))
	assert.Equal(t, `{"type":"ephemeral"}`, got)

	assert.Empty(t, chat.Messages[1].ExtraFields.Lookup(core.ExtraContentField), "messages[1] extra_content should be absent")
}

func TestToChatRequestCarriesToolUseExtraContent(t *testing.T) {
	chat, err := ToChatRequest(mustDecode(t, `{
		"model":"m","max_tokens":10,
		"messages":[
			{"role":"user","content":"hi"},
			{"role":"assistant","content":[
				{"type":"tool_use","id":"tu_1","name":"lookup","input":{},"cache_control":{"type":"ephemeral"},
				 "extra_content":{"google":{"thought_signature":"sig"}}},
				{"type":"tool_use","id":"tu_2","name":"lookup","input":{},"extra_content":null}
			]}
		]
	}`))
	require.NoError(t, err)

	calls := chat.Messages[1].ToolCalls
	require.Len(t, calls, 2)
	got := string(calls[0].ExtraFields.Lookup(core.ExtraContentField))
	assert.Equal(t, `{"google":{"thought_signature":"sig"}}`, got)
	got = string(calls[0].ExtraFields.Lookup("cache_control"))
	assert.Equal(t, `{"type":"ephemeral"}`, got)

	assert.Empty(t, calls[1].ExtraFields.Lookup(core.ExtraContentField), "null extra_content should be dropped")
}

func TestToChatRequestPreservesAssistantThinkingBlocks(t *testing.T) {
	chat, err := ToChatRequest(mustDecode(t, `{
		"model":"m","max_tokens":10,
		"messages":[
			{"role":"user","content":"hi"},
			{"role":"assistant","content":[
				{"type":"thinking","thinking":"","signature":"sig1"},
				{"type":"redacted_thinking","data":"opaque"},
				{"type":"tool_use","id":"tu_1","name":"lookup","input":{}}
			]},
			{"role":"user","content":[{"type":"thinking","thinking":"stray"},{"type":"text","text":"ok"}]}
		]
	}`))
	require.NoError(t, err)

	assistant := chat.Messages[1]
	want := `{"thinking_blocks":[{"type":"thinking","thinking":"","signature":"sig1"},{"type":"redacted_thinking","data":"opaque"}]}`
	got := string(assistant.ExtraFields.ExtraContent(core.ExtraContentVendorAnthropic))
	assert.Equal(t, want, got)
	assert.Len(t, assistant.ToolCalls, 1)

	assert.Empty(t, chat.Messages[2].ExtraFields.Lookup(core.ExtraContentField), "user extra_content should be dropped")
	assert.Equal(t, "ok", chat.Messages[2].Content)
}

// The documented agent loop echoes the assistant turn back verbatim. A turn
// from a provider that does not sign its reasoning carries an empty signature,
// which must survive the round trip as replay state rather than failing the
// request: the block reaches the same provider again, and the Anthropic egress
// drops it before it can reach Claude.
func TestMessagesRoundTripUnsignedThinking(t *testing.T) {
	resp := &core.ChatResponse{Choices: []core.Choice{{
		Message: core.ResponseMessage{
			Role:        "assistant",
			Content:     "391",
			ExtraFields: core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{"reasoning_content": json.RawMessage(`"17*23"`)}),
		},
		FinishReason: "stop",
	}}}
	content, err := json.Marshal(FromChatResponse(resp).Content)
	require.NoError(t, err)
	require.Contains(t, string(content), `"signature":""`, "content = %s, want an empty signature on the thinking block", content)

	body := `{"model":"m","max_tokens":10,"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":` +
		string(content) + `},{"role":"user","content":"and now?"}]}`
	decoded := mustDecode(t, body)
	assert.True(t, HasUnsignedThinking(decoded))

	chat, err := ToChatRequest(decoded)
	require.NoError(t, err)

	want := `{"thinking_blocks":[{"type":"thinking","thinking":"17*23"}]}`
	got := string(chat.Messages[1].ExtraFields.ExtraContent(core.ExtraContentVendorAnthropic))
	assert.Equal(t, want, got)
}

func TestHasUnsignedThinking(t *testing.T) {
	tests := []struct {
		name string
		body string
		want bool
	}{
		{
			name: "signed assistant thinking",
			body: `{"model":"m","max_tokens":10,"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"t","signature":"sig"}]}]}`,
		},
		{
			name: "redacted thinking carries data instead of a signature",
			body: `{"model":"m","max_tokens":10,"messages":[{"role":"assistant","content":[{"type":"redacted_thinking","data":"opaque"}]}]}`,
		},
		{
			name: "string content",
			body: `{"model":"m","max_tokens":10,"messages":[{"role":"assistant","content":"hi"}]}`,
		},
		{
			name: "a stray user thinking block is not replay state",
			body: `{"model":"m","max_tokens":10,"messages":[{"role":"user","content":[{"type":"thinking","thinking":"t"}]}]}`,
		},
		{
			name: "missing signature",
			body: `{"model":"m","max_tokens":10,"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"t"}]}]}`,
			want: true,
		},
		{
			name: "empty signature",
			body: `{"model":"m","max_tokens":10,"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"t","signature":"  "}]}]}`,
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := HasUnsignedThinking(mustDecode(t, tt.body))
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestToChatRequestLenientDropsUnsupportedContent(t *testing.T) {
	body := `{
		"model":"m","max_tokens":10,
		"system":[{"type":"text","text":"sys"},{"type":"image","source":{"type":"url","url":"https://x/y.png"}}],
		"tools":[{"type":"web_search_20250305","name":"web_search"},{"name":"lookup","input_schema":{"type":"object"}}],
		"messages":[
			{"role":"user","content":"search"},
			{"role":"assistant","content":[
				{"type":"server_tool_use","id":"srv_1","name":"web_search","input":{"query":"q"}},
				{"type":"web_search_tool_result","tool_use_id":"srv_1","content":[]},
				{"type":"text","text":"found"}
			]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu_1","content":[{"type":"tool_reference","tool_name":"x"}]}]}
		]
	}`
	_, err := ToChatRequest(mustDecode(t, body))
	require.Error(t, err)

	chat, err := ToChatRequestLenient(mustDecode(t, body))
	require.NoError(t, err)
	assert.Equal(t, "m", chat.Model)
	assert.Len(t, chat.Tools, 1)
	assert.Len(t, chat.Messages, 4)
	assert.Equal(t, "sys", chat.Messages[0].Content)
	assert.Equal(t, "found", chat.Messages[2].Content)
	assert.Equal(t, "tool", chat.Messages[3].Role)
	_, err = ToChatRequestLenient(mustDecode(t, `{"model":"m","max_tokens":10,"messages":[{"role":"user","content":42}]}`))
	assert.Error(t, err)
}
