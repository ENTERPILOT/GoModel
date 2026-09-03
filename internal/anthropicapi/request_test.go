package anthropicapi

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
)

func mustDecode(t *testing.T, body string) *MessagesRequest {
	t.Helper()
	req, err := DecodeMessagesRequest([]byte(body))
	if err != nil {
		t.Fatalf("DecodeMessagesRequest: %v", err)
	}
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
			if tc.wantErr != (err != nil) {
				t.Fatalf("err=%v, wantErr=%v", err, tc.wantErr)
			}
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
			if err == nil {
				t.Fatal("expected error")
			}
			if _, ok := err.(*core.GatewayError); !ok {
				t.Fatalf("expected *core.GatewayError, got %T", err)
			}
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
		{name: "unsupported tool_result block", body: `{"model":"m","max_tokens":10,"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":[{"type":"document"}]}]}]}`},
		{name: "tool_result image without source", body: `{"model":"m","max_tokens":10,"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":[{"type":"image"}]}]}]}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ToChatRequest(mustDecode(t, tc.body))
			if err == nil {
				t.Fatal("expected error")
			}
			gatewayErr, ok := err.(*core.GatewayError)
			if !ok || gatewayErr.Type != core.ErrorTypeInvalidRequest {
				t.Fatalf("expected invalid_request_error, got %T: %v", err, err)
			}
		})
	}
}

func TestToChatRequestBasic(t *testing.T) {
	chat, err := ToChatRequest(mustDecode(t, `{
		"model":"claude-test","max_tokens":256,"temperature":0.5,"stream":true,
		"system":"be brief",
		"messages":[{"role":"user","content":"hello"}]
	}`))
	if err != nil {
		t.Fatalf("ToChatRequest: %v", err)
	}
	if chat.Model != "claude-test" {
		t.Errorf("Model = %q", chat.Model)
	}
	if chat.MaxTokens == nil || *chat.MaxTokens != 256 {
		t.Errorf("MaxTokens = %v", chat.MaxTokens)
	}
	if chat.Temperature == nil || *chat.Temperature != 0.5 {
		t.Errorf("Temperature = %v", chat.Temperature)
	}
	if !chat.Stream || chat.StreamOptions == nil || !chat.StreamOptions.IncludeUsage {
		t.Errorf("stream options not set: stream=%v opts=%v", chat.Stream, chat.StreamOptions)
	}
	if len(chat.Messages) != 2 {
		t.Fatalf("messages = %d, want 2 (system + user)", len(chat.Messages))
	}
	if chat.Messages[0].Role != "system" || chat.Messages[0].Content != "be brief" {
		t.Errorf("system message = %+v", chat.Messages[0])
	}
	if chat.Messages[1].Role != "user" || chat.Messages[1].Content != "hello" {
		t.Errorf("user message = %+v", chat.Messages[1])
	}
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
	if err != nil {
		t.Fatalf("ToChatRequest: %v", err)
	}
	if len(chat.Messages) != 2 {
		t.Fatalf("messages = %d, want 2 (system + user)", len(chat.Messages))
	}
	if chat.Messages[0].Role != "system" {
		t.Errorf("first message role = %q, want system", chat.Messages[0].Role)
	}
	systemContent, ok := chat.Messages[0].Content.(string)
	if !ok {
		t.Fatalf("system content is not a string: %T", chat.Messages[0].Content)
	}
	if systemContent != "system prompt" {
		t.Errorf("system content = %q, want system prompt", systemContent)
	}
	if chat.Messages[1].Role != "user" || chat.Messages[1].Content != "hello" {
		t.Errorf("user message = %+v", chat.Messages[1])
	}
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
	if err != nil {
		t.Fatalf("ToChatRequest: %v", err)
	}
	if len(chat.Messages) != 3 {
		t.Fatalf("messages = %d, want 3 (system + system + user)", len(chat.Messages))
	}
	if chat.Messages[0].Role != "system" || chat.Messages[0].Content != "top-level system" {
		t.Errorf("first message = %+v, want top-level system", chat.Messages[0])
	}
	if chat.Messages[1].Role != "system" || chat.Messages[1].Content != "message system" {
		t.Errorf("second message = %+v, want message system", chat.Messages[1])
	}
	if chat.Messages[2].Role != "user" || chat.Messages[2].Content != "hello" {
		t.Errorf("user message = %+v", chat.Messages[2])
	}
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
	if err != nil {
		t.Fatalf("ToChatRequest: %v", err)
	}
	if len(chat.Messages) != 4 {
		t.Fatalf("messages = %d, want 4 (system + user + assistant + system)", len(chat.Messages))
	}
	last := chat.Messages[3]
	if last.Role != "system" {
		t.Fatalf("last message role = %q, want system", last.Role)
	}
	parts, ok := last.Content.([]core.ContentPart)
	if !ok {
		t.Fatalf("last system content is %T, want []core.ContentPart", last.Content)
	}
	if len(parts) != 1 || parts[0].Text != "reminder" {
		t.Fatalf("last system parts = %+v", parts)
	}
	if got := string(parts[0].ExtraFields.Lookup("cache_control")); got != `{"type":"ephemeral"}` {
		t.Errorf("last system cache_control = %q, want ephemeral marker", got)
	}
}

func TestToChatRequestImageBlock(t *testing.T) {
	chat, err := ToChatRequest(mustDecode(t, `{
		"model":"m","max_tokens":10,
		"messages":[{"role":"user","content":[
			{"type":"text","text":"look"},
			{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AAAA"}}
		]}]
	}`))
	if err != nil {
		t.Fatalf("ToChatRequest: %v", err)
	}
	parts, ok := chat.Messages[0].Content.([]core.ContentPart)
	if !ok {
		t.Fatalf("content type = %T, want []core.ContentPart", chat.Messages[0].Content)
	}
	if len(parts) != 2 || parts[1].Type != "image_url" {
		t.Fatalf("parts = %+v", parts)
	}
	if got := parts[1].ImageURL.URL; got != "data:image/png;base64,AAAA" {
		t.Errorf("image URL = %q", got)
	}
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
	if err != nil {
		t.Fatalf("ToChatRequest: %v", err)
	}
	if len(chat.Messages) != 3 {
		t.Fatalf("messages = %d, want 3 (assistant, tool, user)", len(chat.Messages))
	}
	assistant := chat.Messages[0]
	if assistant.Role != "assistant" || len(assistant.ToolCalls) != 1 {
		t.Fatalf("assistant = %+v", assistant)
	}
	if assistant.ToolCalls[0].ID != "tu_1" || assistant.ToolCalls[0].Function.Name != "get_weather" {
		t.Errorf("tool call = %+v", assistant.ToolCalls[0])
	}
	if assistant.ToolCalls[0].Function.Arguments != `{"city":"paris"}` {
		t.Errorf("arguments = %q", assistant.ToolCalls[0].Function.Arguments)
	}
	tool := chat.Messages[1]
	if tool.Role != "tool" || tool.ToolCallID != "tu_1" || tool.Content != "sunny" {
		t.Errorf("tool message = %+v", tool)
	}
	if chat.Messages[2].Role != "user" || chat.Messages[2].Content != "thanks" {
		t.Errorf("user message = %+v", chat.Messages[2])
	}
}

func TestToChatRequestTools(t *testing.T) {
	// An explicit type of "custom" is accepted alongside the typeless form.
	chat, err := ToChatRequest(mustDecode(t, `{
		"model":"m","max_tokens":10,
		"messages":[{"role":"user","content":"hi"}],
		"tools":[{"type":"custom","name":"get_weather","description":"weather","input_schema":{"type":"object","properties":{}}}],
		"tool_choice":{"type":"tool","name":"get_weather"}
	}`))
	if err != nil {
		t.Fatalf("ToChatRequest: %v", err)
	}
	if len(chat.Tools) != 1 {
		t.Fatalf("tools = %d", len(chat.Tools))
	}
	if chat.Tools[0]["type"] != "function" {
		t.Errorf("tool type = %v", chat.Tools[0]["type"])
	}
	fn, _ := chat.Tools[0]["function"].(map[string]any)
	if fn["name"] != "get_weather" || fn["parameters"] == nil {
		t.Errorf("function = %+v", fn)
	}
	choice, ok := chat.ToolChoice.(map[string]any)
	if !ok || choice["type"] != "function" {
		t.Fatalf("tool_choice = %#v", chat.ToolChoice)
	}
}

func TestToChatRequestRejectsServerTool(t *testing.T) {
	_, err := ToChatRequest(mustDecode(t, `{
		"model":"m","max_tokens":10,
		"messages":[{"role":"user","content":"hi"}],
		"tools":[{"type":"web_search_20250305","name":"web_search"}]
	}`))
	if err == nil {
		t.Fatal("expected error for Anthropic server tool")
	}
	gatewayErr, ok := err.(*core.GatewayError)
	if !ok || gatewayErr.Type != core.ErrorTypeInvalidRequest {
		t.Fatalf("err = %#v, want invalid_request_error", err)
	}
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
			if err != nil {
				t.Fatalf("ToChatRequest: %v", err)
			}
			if chat.ToolChoice != tc.want {
				t.Errorf("ToolChoice = %#v, want %#v", chat.ToolChoice, tc.want)
			}
		})
	}
}

func TestToChatRequestDisableParallelToolUse(t *testing.T) {
	chat, err := ToChatRequest(mustDecode(t, `{
		"model":"m","max_tokens":10,
		"messages":[{"role":"user","content":"hi"}],
		"tool_choice":{"type":"auto","disable_parallel_tool_use":true}
	}`))
	if err != nil {
		t.Fatalf("ToChatRequest: %v", err)
	}
	if chat.ParallelToolCalls == nil || *chat.ParallelToolCalls {
		t.Errorf("ParallelToolCalls = %v, want false", chat.ParallelToolCalls)
	}
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
			if err != nil {
				t.Fatalf("ToChatRequest: %v", err)
			}
			if chat.Reasoning == nil || chat.Reasoning.Effort != tc.effort {
				t.Errorf("Reasoning = %+v, want effort %q", chat.Reasoning, tc.effort)
			}
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
	if err != nil {
		t.Fatalf("ToChatRequest: %v", err)
	}
	if raw := chat.ExtraFields.Lookup("stop"); string(raw) != `["STOP"]` {
		t.Errorf("ExtraFields[stop] = %s, want [\"STOP\"]", raw)
	}
	// top_p and user have typed ChatRequest fields; they must land there so
	// internal consumers of the typed fields (Responses lowering, provider
	// adapters) see them, and must not also ride in ExtraFields.
	if chat.TopP == nil || *chat.TopP != 0.9 {
		t.Errorf("TopP = %v, want 0.9", chat.TopP)
	}
	if chat.User != "u-123" {
		t.Errorf("User = %q, want u-123", chat.User)
	}
	for _, key := range []string{"top_p", "user"} {
		if raw := chat.ExtraFields.Lookup(key); len(raw) > 0 {
			t.Errorf("ExtraFields[%q] = %s, want typed field only", key, raw)
		}
	}
	// top_k has no portable OpenAI-compatible equivalent and OpenAI-family
	// providers reject unknown request fields; it must be dropped, not carried.
	if raw := chat.ExtraFields.Lookup("top_k"); len(raw) > 0 {
		t.Errorf("ExtraFields[top_k] = %s, want dropped", raw)
	}
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
	if err != nil {
		t.Fatalf("ToChatRequest: %v", err)
	}
	if len(chat.Messages) != 2 {
		t.Fatalf("messages = %d, want assistant, tool", len(chat.Messages))
	}
	tool := chat.Messages[1]
	if tool.Role != "tool" || tool.ToolCallID != "tu_1" {
		t.Fatalf("tool message = %+v", tool)
	}
	parts, ok := tool.Content.([]core.ContentPart)
	if !ok || len(parts) != 2 {
		t.Fatalf("tool content = %T %#v, want two structured parts", tool.Content, tool.Content)
	}
	if parts[0].Type != "text" || parts[0].Text != "captured" {
		t.Errorf("parts[0] = %+v, want text part", parts[0])
	}
	if parts[1].Type != "image_url" || parts[1].ImageURL == nil || parts[1].ImageURL.URL != "data:image/png;base64,aGVsbG8=" {
		t.Errorf("parts[1] = %+v, want image_url data URL", parts[1])
	}
	if got := EstimateInputTokens(mustDecode(t, `{"model":"m","max_tokens":10,"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu_1","content":[{"type":"text","text":"captured"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aGVsbG8="}}]}]}]}`)); got != 2 {
		t.Errorf("EstimateInputTokens = %d, want 2 (text only)", got)
	}
}

func TestToChatRequestToolResultTextOnlyStaysString(t *testing.T) {
	chat, err := ToChatRequest(mustDecode(t, `{
		"model":"m","max_tokens":10,
		"messages":[{"role":"user","content":[
			{"type":"tool_result","tool_use_id":"tu_1","content":[{"type":"text","text":"a"},{"type":"text","text":"b"}]}
		]}]
	}`))
	if err != nil {
		t.Fatalf("ToChatRequest: %v", err)
	}
	if got := chat.Messages[0].Content; got != "a\nb" {
		t.Errorf("tool content = %#v, want joined string", got)
	}
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
	if err != nil {
		t.Fatalf("ToChatRequest: %v", err)
	}
	want := `{"type":"ephemeral"}`
	if got := string(chat.ExtraFields.Lookup("cache_control")); got != want {
		t.Errorf("request cache_control = %s, want %s", got, want)
	}
	if len(chat.Messages) != 4 {
		t.Fatalf("messages = %d, want system, user, assistant, tool", len(chat.Messages))
	}
	for _, index := range []int{0, 1} {
		parts, ok := chat.Messages[index].Content.([]core.ContentPart)
		if !ok || len(parts) != 1 {
			t.Fatalf("messages[%d].Content = %T %#v, want one structured part", index, chat.Messages[index].Content, chat.Messages[index].Content)
		}
		if got := string(parts[0].ExtraFields.Lookup("cache_control")); got != want {
			t.Errorf("messages[%d] cache_control = %s, want %s", index, got, want)
		}
	}
	if got := string(chat.Messages[2].ToolCalls[0].ExtraFields.Lookup("cache_control")); got != want {
		t.Errorf("tool_use cache_control = %s, want %s", got, want)
	}
	if got := string(chat.Messages[3].ExtraFields.Lookup("cache_control")); got != want {
		t.Errorf("tool_result cache_control = %s, want %s", got, want)
	}
	if got, ok := chat.Tools[0]["cache_control"].(map[string]any); !ok || got["type"] != "ephemeral" {
		t.Errorf("tool cache_control = %#v, want ephemeral object", chat.Tools[0]["cache_control"])
	}
}

func TestToChatRequestRejectsInvalidCacheControl(t *testing.T) {
	_, err := ToChatRequest(mustDecode(t, `{
		"model":"m","max_tokens":10,
		"messages":[{"role":"user","content":[{"type":"text","text":"hi","cache_control":"invalid"}]}]
	}`))
	if err == nil || !strings.Contains(err.Error(), "cache_control must be an object") {
		t.Fatalf("error = %v, want cache_control object error", err)
	}
}

func TestToChatRequestRejectsInvalidTopLevelCacheControl(t *testing.T) {
	_, err := ToChatRequest(mustDecode(t, `{
		"model":"m","max_tokens":10,"cache_control":"invalid",
		"messages":[{"role":"user","content":"hi"}]
	}`))
	if err == nil || !strings.Contains(err.Error(), "cache_control must be an object") {
		t.Fatalf("error = %v, want cache_control object error", err)
	}
}

func TestToChatRequestRejectsUnsupportedContentBlock(t *testing.T) {
	_, err := ToChatRequest(mustDecode(t, `{
		"model":"m","max_tokens":10,
		"messages":[{"role":"user","content":[
			{"type":"text","text":"summarize"},
			{"type":"document","source":{"type":"base64","media_type":"application/pdf","data":"eA=="}}
		]}]
	}`))
	if err == nil {
		t.Fatal("expected error for unsupported document content block")
	}
	gatewayErr, ok := err.(*core.GatewayError)
	if !ok || gatewayErr.Type != core.ErrorTypeInvalidRequest {
		t.Fatalf("expected invalid_request_error, got %T: %v", err, err)
	}
	if !strings.Contains(gatewayErr.Message, "document") {
		t.Errorf("error message should name the block type: %q", gatewayErr.Message)
	}
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
	if err != nil {
		t.Fatalf("ToChatRequest: %v", err)
	}
	if len(chat.Messages) != 1 || chat.Messages[0].Content != "answer" {
		t.Fatalf("messages = %+v", chat.Messages)
	}
}

func TestEstimateInputTokens(t *testing.T) {
	req := mustDecode(t, `{
		"model":"m","max_tokens":10,
		"system":"you are helpful",
		"messages":[{"role":"user","content":"count these characters please"}]
	}`)
	got := EstimateInputTokens(req)
	if got <= 0 {
		t.Fatalf("EstimateInputTokens = %d, want > 0", got)
	}
	if EstimateInputTokens(nil) != 0 {
		t.Error("EstimateInputTokens(nil) should be 0")
	}
}

func TestToChatRequestRoundTripsAsJSON(t *testing.T) {
	// The translated request must marshal cleanly for the response cache key.
	chat, err := ToChatRequest(mustDecode(t, `{
		"model":"m","max_tokens":10,
		"messages":[{"role":"user","content":"hi"}]
	}`))
	if err != nil {
		t.Fatalf("ToChatRequest: %v", err)
	}
	if _, err := json.Marshal(chat); err != nil {
		t.Fatalf("json.Marshal(chat): %v", err)
	}
}

func TestEstimateChatInputTokens(t *testing.T) {
	tests := []struct {
		name string
		req  *core.ChatRequest
		want int
	}{
		{
			name: "nil request",
			req:  nil,
			want: 0,
		},
		{
			name: "messages only",
			req: &core.ChatRequest{
				Messages: []core.Message{
					{Role: "system", Content: "You are terse."},
					{Role: "user", Content: "What is 2+2?"},
				},
			},
			// "You are terse." (14) + "What is 2+2?" (12) = 26 chars → ceil(26/4) = 7
			want: 7,
		},
		{
			name: "tool calls and tool definitions",
			req: &core.ChatRequest{
				Messages: []core.Message{
					{
						Role: "assistant",
						ToolCalls: []core.ToolCall{
							{Function: core.FunctionCall{Name: "weather", Arguments: `{"city":"Paris"}`}},
						},
					},
				},
				Tools: []map[string]any{
					{"type": "function", "function": map[string]any{"name": "weather"}},
				},
			},
			// "weather" (7) + `{"city":"Paris"}` (16) = 23 chars, plus the
			// marshaled tool definition
			// `{"function":{"name":"weather"},"type":"function"}` (49 chars).
			// Total 72 chars → ceil(72/4) = 18.
			want: 18,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := EstimateChatInputTokens(tc.req); got != tc.want {
				t.Errorf("estimate = %d, want %d", got, tc.want)
			}
		})
	}
}
