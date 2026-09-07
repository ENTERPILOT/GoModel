package exchange

import (
	"strings"
	"testing"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/pluginapi"
)

const chatFixture = `{
  "model": "gpt-4o",
  "messages": [
    {"role": "system", "content": "be brief", "cache_control": {"type": "ephemeral"}},
    {"role": "user", "name": "alice", "content": [
      {"type": "text", "text": "what is this?"},
      {"type": "image_url", "image_url": {"url": "data:image/png;base64,AAAA", "detail": "low"}},
      {"type": "text", "text": "second text"}
    ]},
    {"role": "assistant", "content": null, "tool_calls": [
      {"id": "call_1", "type": "function", "function": {"name": "lookup", "arguments": "{\"q\":\"x\"}"}, "custom": 1}
    ]},
    {"role": "tool", "tool_call_id": "call_1", "content": "result text"},
    {"role": "user", "content": "thanks", "x_extra": {"a": 1}}
  ],
  "tools": [{"type": "function", "function": {"name": "lookup", "description": "d", "parameters": {"type": "object"}}}],
  "tool_choice": "auto",
  "max_tokens": 10,
  "temperature": 0.2,
  "user": "u1",
  "custom_top": "yes"
}`

func chatPrompt(t *testing.T) (*core.ChatRequest, *pluginapi.Prompt) {
	t.Helper()
	req := decodeChat(t, chatFixture)
	p, err := FromChatRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	return req, p
}

func TestFromChatRequestMapping(t *testing.T) {
	_, p := chatPrompt(t)
	if len(p.Messages) != 5 {
		t.Fatalf("messages = %d", len(p.Messages))
	}
	m0, m1, m2, m3, m4 := p.Messages[0], p.Messages[1], p.Messages[2], p.Messages[3], p.Messages[4]
	if m0.ID != "m0" || m0.Role != pluginapi.RoleSystem || !m0.CacheBreakpoint || m0.Text() != "be brief" {
		t.Errorf("m0 = %+v", m0)
	}
	if m1.Name != "alice" || len(m1.Parts) != 3 || m1.Parts[1].Kind != pluginapi.PartImage || m1.Parts[1].URL != "data:image/png;base64,AAAA" || m1.Parts[1].MediaType != "image/png" || m1.Parts[1].Data != nil {
		t.Errorf("m1 = %+v", m1)
	}
	if len(m2.Parts) != 1 || m2.Parts[0].Kind != pluginapi.PartToolCall || m2.Parts[0].ToolCall.ID != "call_1" || string(m2.Parts[0].ToolCall.Arguments) != `{"q":"x"}` {
		t.Errorf("m2 = %+v", m2)
	}
	if m3.Role != pluginapi.RoleTool || m3.ToolCallID != "call_1" || m3.Parts[0].Kind != pluginapi.PartToolResult || m3.Parts[0].ToolResult.CallID != "call_1" || m3.Text() != "result text" {
		t.Errorf("m3 = %+v", m3)
	}
	if m4.Text() != "thanks" || m4.CacheBreakpoint {
		t.Errorf("m4 = %+v", m4)
	}
	calls := p.ToolCalls()
	if len(calls) != 1 || !calls[0].HasResult {
		t.Errorf("ToolCalls = %+v", calls)
	}
	if p.Params.Model != "gpt-4o" || *p.Params.MaxTokens != 10 || *p.Params.Temperature != 0.2 || p.Params.ToolChoice != "auto" || p.Params.Stream {
		t.Errorf("params = %+v", p.Params)
	}
	if p.Params.Extra["user"] != "u1" || p.Params.Extra["custom_top"] != "yes" {
		t.Errorf("extra = %+v", p.Params.Extra)
	}
	if _, ok := p.Params.Extra["messages"]; ok {
		t.Error("extra must not include modelled keys")
	}
	if len(p.Tools) != 1 || p.Tools[0].Name != "lookup" || p.Tools[0].Description != "d" || string(p.Tools[0].Parameters) != `{"type":"object"}` {
		t.Errorf("tools = %+v", p.Tools)
	}
	if !json.Valid(p.Raw) || p.Changes().Dirty {
		t.Error("raw must be valid JSON and prompt clean")
	}
}

func TestChatRoundTripNoEdits(t *testing.T) {
	req, p := chatPrompt(t)
	applied, err := ApplyToChatRequest(req, p)
	if err != nil {
		t.Fatal(err)
	}
	assertJSONEqual(t, req, applied)
	if !applied.Messages[2].ContentNull {
		t.Error("content: null must survive")
	}
	if applied.Messages[0].ExtraFields.Lookup("cache_control") == nil {
		t.Error("cache_control must survive")
	}
}

func TestChatEditStructuredTextPart(t *testing.T) {
	req, p := chatPrompt(t)
	if err := p.SetText("m1", 2, "changed"); err != nil {
		t.Fatal(err)
	}
	applied, err := ApplyToChatRequest(req, p)
	if err != nil {
		t.Fatal(err)
	}
	for i := range req.Messages {
		if i == 1 {
			continue
		}
		assertJSONEqual(t, req.Messages[i], applied.Messages[i])
	}
	got := messageJSON(t, applied.Messages[1])
	want := `{"role":"user","content":[{"type":"text","text":"what is this?"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA","detail":"low"}},{"type":"text","text":"changed"}],"name":"alice"}`
	if got != want {
		t.Errorf("edited message\n got: %s\nwant: %s", got, want)
	}
}

func TestChatEditKeepsCacheControlAndSingleString(t *testing.T) {
	req, p := chatPrompt(t)
	if err := p.SetText("m0", 0, "be very brief"); err != nil {
		t.Fatal(err)
	}
	applied, err := ApplyToChatRequest(req, p)
	if err != nil {
		t.Fatal(err)
	}
	got := messageJSON(t, applied.Messages[0])
	if got != `{"role":"system","content":"be very brief","cache_control":{"type":"ephemeral"}}` {
		t.Errorf("edited system message = %s", got)
	}
	if _, isString := applied.Messages[0].Content.(string); !isString {
		t.Error("string content must stay a string")
	}
}

func TestChatInsertSystemMessages(t *testing.T) {
	req, p := chatPrompt(t)
	p.Insert(0, pluginapi.TextMessage(pluginapi.RoleSystem, "prefix"))
	tail := pluginapi.TextMessage(pluginapi.RoleUser, "suffix")
	tail.Name = "bob"
	tail.CacheBreakpoint = true
	p.Append(tail)
	applied, err := ApplyToChatRequest(req, p)
	if err != nil {
		t.Fatal(err)
	}
	if len(applied.Messages) != 7 {
		t.Fatalf("messages = %d", len(applied.Messages))
	}
	if got := messageJSON(t, applied.Messages[0]); got != `{"role":"system","content":"prefix"}` {
		t.Errorf("first = %s", got)
	}
	if got := messageJSON(t, applied.Messages[6]); got != `{"role":"user","content":"suffix","cache_control":{"type":"ephemeral"},"name":"bob"}` {
		t.Errorf("last = %s", got)
	}
	for i := range req.Messages {
		assertJSONEqual(t, req.Messages[i], applied.Messages[i+1])
	}
}

func TestChatInsertToolCallAndResultMessages(t *testing.T) {
	req, p := chatPrompt(t)
	call := pluginapi.Message{Role: pluginapi.RoleAssistant, Parts: []pluginapi.Part{
		{Kind: pluginapi.PartToolCall, ToolCall: &pluginapi.ToolCall{ID: "call_2", Name: "f", Arguments: json.RawMessage(`{"a":1}`)}},
	}}
	result := pluginapi.Message{Role: pluginapi.RoleTool, Parts: []pluginapi.Part{
		{Kind: pluginapi.PartToolResult, ToolResult: &pluginapi.ToolResult{CallID: "call_2", Parts: []pluginapi.Part{{Kind: pluginapi.PartText, Text: "done"}}}},
	}}
	multi := pluginapi.Message{Role: pluginapi.RoleUser, Parts: []pluginapi.Part{
		{Kind: pluginapi.PartText, Text: "look"},
		{Kind: pluginapi.PartImage, URL: "https://x/y.png"},
	}}
	p.Append(call)
	p.Append(result)
	p.Append(multi)
	applied, err := ApplyToChatRequest(req, p)
	if err != nil {
		t.Fatal(err)
	}
	n := len(req.Messages)
	if got := messageJSON(t, applied.Messages[n]); got != `{"role":"assistant","content":null,"tool_calls":[{"id":"call_2","type":"function","function":{"name":"f","arguments":"{\"a\":1}"}}]}` {
		t.Errorf("call = %s", got)
	}
	if got := messageJSON(t, applied.Messages[n+1]); got != `{"role":"tool","content":"done","tool_call_id":"call_2"}` {
		t.Errorf("result = %s", got)
	}
	if got := messageJSON(t, applied.Messages[n+2]); got != `{"role":"user","content":[{"type":"text","text":"look"},{"type":"image_url","image_url":{"url":"https://x/y.png"}}]}` {
		t.Errorf("multi = %s", got)
	}
}

func TestChatRemoveToolPair(t *testing.T) {
	tests := []struct {
		name  string
		order []string
	}{
		{name: "call then result", order: []string{"m2", "m3"}},
		{name: "result then call", order: []string{"m3", "m2"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, p := chatPrompt(t)
			_ = p.Remove(tt.order[0])
			if err := p.Remove(tt.order[1]); err != nil {
				t.Fatal(err)
			}
			applied, err := ApplyToChatRequest(req, p)
			if err != nil {
				t.Fatal(err)
			}
			if len(applied.Messages) != 3 {
				t.Fatalf("messages = %d", len(applied.Messages))
			}
			assertJSONEqual(t, req.Messages[0], applied.Messages[0])
			assertJSONEqual(t, req.Messages[1], applied.Messages[1])
			assertJSONEqual(t, req.Messages[4], applied.Messages[2])
		})
	}
}

func TestChatRemoveOnlyCallIsRejected(t *testing.T) {
	req, p := chatPrompt(t)
	if err := p.Remove("m2"); err == nil {
		t.Fatal("expected dangling error")
	}
	if _, err := ApplyToChatRequest(req, p); err == nil || !strings.Contains(err.Error(), "m3") {
		t.Fatalf("apply err = %v, want dangling naming m3", err)
	}
}

func TestChatToolEdits(t *testing.T) {
	req, p := chatPrompt(t)
	if err := p.SetToolArguments("m2", "call_1", json.RawMessage(`{"q":"y"}`)); err != nil {
		t.Fatal(err)
	}
	if err := p.SetToolResult("m3", "call_1", []pluginapi.Part{{Kind: pluginapi.PartText, Text: "[redacted]"}}); err != nil {
		t.Fatal(err)
	}
	applied, err := ApplyToChatRequest(req, p)
	if err != nil {
		t.Fatal(err)
	}
	if got := messageJSON(t, applied.Messages[2]); got != `{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{\"q\":\"y\"}"},"custom":1}]}` {
		t.Errorf("call = %s", got)
	}
	if got := messageJSON(t, applied.Messages[3]); got != `{"role":"tool","content":"[redacted]","tool_call_id":"call_1"}` {
		t.Errorf("result = %s", got)
	}
}

func TestChatParams(t *testing.T) {
	tests := []struct {
		name    string
		set     map[string]any
		wantErr string
		check   func(t *testing.T, r *core.ChatRequest)
	}{
		{
			name: "typed and extra keys",
			set:  map[string]any{"max_tokens": 99, "temperature": 0.7, "top_p": 0.5, "user": "u2", "foo": "bar", "reasoning": map[string]any{"effort": "low"}, "parallel_tool_calls": true, "tool_choice": "none"},
			check: func(t *testing.T, r *core.ChatRequest) {
				if *r.MaxTokens != 99 || *r.Temperature != 0.7 || *r.TopP != 0.5 || r.User != "u2" || r.ToolChoice != "none" || !*r.ParallelToolCalls {
					t.Errorf("typed params = %+v", r)
				}
				if r.Reasoning == nil || r.Reasoning.Effort != "low" {
					t.Errorf("reasoning = %+v", r.Reasoning)
				}
				if string(r.ExtraFields.Lookup("foo")) != `"bar"` || string(r.ExtraFields.Lookup("custom_top")) != `"yes"` {
					t.Error("extra fields not merged")
				}
				if r.Model != "gpt-4o" || len(r.Messages) != 5 {
					t.Error("model or messages changed")
				}
			},
		},
		{name: "model is frozen", set: map[string]any{"model": "other"}, wantErr: "model"},
		{name: "stream is frozen", set: map[string]any{"stream": true}, wantErr: "stream"},
		{name: "bad type", set: map[string]any{"max_tokens": "ten"}, wantErr: "integer"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, p := chatPrompt(t)
			for k, v := range tt.set {
				p.SetParam(k, v)
			}
			applied, err := ApplyToChatRequest(req, p)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			tt.check(t, applied)
		})
	}
}

func TestChatInterfaceContentAndAudio(t *testing.T) {
	req := decodeChat(t, `{"model":"m","messages":[{"role":"user","content":[{"type":"input_audio","input_audio":{"data":"AAAA","format":"wav"}},{"type":"text","text":"transcribe"}]}]}`)
	p, err := FromChatRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	audio := p.Messages[0].Parts[0]
	if audio.Kind != pluginapi.PartAudio || audio.MediaType != "audio/wav" || string(audio.Data) != "AAAA" {
		t.Errorf("audio part = %+v", audio)
	}
	if err := p.SetText("m0", 1, "translate"); err != nil {
		t.Fatal(err)
	}
	applied, err := ApplyToChatRequest(req, p)
	if err != nil {
		t.Fatal(err)
	}
	if got := messageJSON(t, applied.Messages[0]); got != `{"role":"user","content":[{"type":"input_audio","input_audio":{"data":"AAAA","format":"wav"}},{"type":"text","text":"translate"}]}` {
		t.Errorf("message = %s", got)
	}
}

func TestChatRequestFromMessages(t *testing.T) {
	temp := 0.1
	req := ChatRequestFromMessages("openai/gpt-4o", []pluginapi.Message{
		pluginapi.TextMessage(pluginapi.RoleSystem, "classify"),
		pluginapi.TextMessage(pluginapi.RoleUser, "hello"),
		{Role: pluginapi.RoleUser, Parts: []pluginapi.Part{{Kind: pluginapi.PartFile, URL: "https://x"}, {Kind: pluginapi.PartText, Text: "fallback"}}},
	}, 5, &temp)
	if req.Model != "openai/gpt-4o" || *req.MaxTokens != 5 || *req.Temperature != 0.1 || len(req.Messages) != 3 {
		t.Errorf("request = %+v", req)
	}
	if got := messageJSON(t, req.Messages[0]); got != `{"role":"system","content":"classify"}` {
		t.Errorf("system = %s", got)
	}
	if got := messageJSON(t, req.Messages[2]); got != `{"role":"user","content":"fallback"}` {
		t.Errorf("unsupported part must fall back to text: %s", got)
	}
	if r := ChatRequestFromMessages("m", nil, 0, nil); r.MaxTokens != nil || len(r.Messages) != 0 {
		t.Errorf("empty request = %+v", r)
	}
}
