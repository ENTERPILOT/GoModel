package exchange

import (
	"encoding/base64"
	"testing"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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
	require.NoError(t, err)

	return req, p
}

func TestFromChatRequestMapping(t *testing.T) {
	_, p := chatPrompt(t)
	require.Len(t, p.Messages, 5)

	m0, m1, m2, m3, m4 := p.Messages[0], p.Messages[1], p.Messages[2], p.Messages[3], p.Messages[4]
	assert.Equal(t, "m0", m0.ID)
	assert.Equal(t, pluginapi.RoleSystem, m0.Role)
	assert.True(t, m0.CacheBreakpoint)
	assert.Equal(t, "be brief", m0.Text())
	assert.Equal(t, "alice", m1.Name)
	require.Len(t, m1.Parts, 3)
	assert.Equal(t, pluginapi.PartImage, m1.Parts[1].Kind)
	assert.Equal(t, "data:image/png;base64,AAAA", m1.Parts[1].URL)
	assert.Equal(t, "image/png", m1.Parts[1].MediaType)
	assert.Nil(t, m1.Parts[1].Data)
	require.Len(t, m2.Parts, 1)
	assert.Equal(t, pluginapi.PartToolCall, m2.Parts[0].Kind)
	assert.Equal(t, "call_1", m2.Parts[0].ToolCall.ID)
	assert.Equal(t, `{"q":"x"}`, string(m2.Parts[0].ToolCall.Arguments))
	assert.Equal(t, pluginapi.RoleTool, m3.Role)
	assert.Equal(t, "call_1", m3.ToolCallID)
	assert.Equal(t, pluginapi.PartToolResult, m3.Parts[0].Kind)
	assert.Equal(t, "call_1", m3.Parts[0].ToolResult.CallID)
	assert.Equal(t, "result text", m3.Text())
	assert.Equal(t, "thanks", m4.Text())
	assert.False(t, m4.CacheBreakpoint)

	calls := p.ToolCalls()
	require.Len(t, calls, 1)
	assert.True(t, calls[0].HasResult)
	assert.Equal(t, "gpt-4o", p.Params.Model)
	assert.Equal(t, 10, *p.Params.MaxTokens)
	assert.Equal(t, 0.2, *p.Params.Temperature)
	assert.Equal(t, "auto", p.Params.ToolChoice)
	assert.False(t, p.Params.Stream)
	assert.Equal(t, "u1", p.Params.Extra["user"])
	assert.Equal(t, "yes", p.Params.Extra["custom_top"])
	_, ok := p.Params.Extra["messages"]
	assert.False(t, ok)
	require.Len(t, p.Tools, 1)
	assert.Equal(t, "lookup", p.Tools[0].Name)
	assert.Equal(t, "d", p.Tools[0].Description)
	assert.Equal(t, `{"type":"object"}`, string(p.Tools[0].Parameters))
	assert.True(t, json.Valid(p.Raw))
	assert.False(t, p.Changes().Dirty)
}

func TestChatRoundTripNoEdits(t *testing.T) {
	req, p := chatPrompt(t)
	applied, err := ApplyToChatRequest(req, p)
	require.NoError(t, err)

	assertJSONEqual(t, req, applied)
	assert.True(t, applied.Messages[2].ContentNull)
	assert.NotNil(t, applied.Messages[0].ExtraFields.Lookup("cache_control"))
}

func TestChatEditStructuredTextPart(t *testing.T) {
	req, p := chatPrompt(t)
	err := p.SetText("m1", 2, "changed")
	require.NoError(t, err)

	applied, err := ApplyToChatRequest(req, p)
	require.NoError(t, err)

	for i := range req.Messages {
		if i == 1 {
			continue
		}
		assertJSONEqual(t, req.Messages[i], applied.Messages[i])
	}
	got := messageJSON(t, applied.Messages[1])
	want := `{"role":"user","content":[{"type":"text","text":"what is this?"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA","detail":"low"}},{"type":"text","text":"changed"}],"name":"alice"}`
	assert.Equal(t, want, got)
}

func TestChatEditKeepsCacheControlAndSingleString(t *testing.T) {
	req, p := chatPrompt(t)
	err := p.SetText("m0", 0, "be very brief")
	require.NoError(t, err)

	applied, err := ApplyToChatRequest(req, p)
	require.NoError(t, err)

	got := messageJSON(t, applied.Messages[0])
	assert.Equal(t, `{"role":"system","content":"be very brief","cache_control":{"type":"ephemeral"}}`, got)
	_, isString := applied.Messages[0].Content.(string)
	assert.True(t, isString)
}

func TestChatInsertSystemMessages(t *testing.T) {
	req, p := chatPrompt(t)
	p.Insert(0, pluginapi.TextMessage(pluginapi.RoleSystem, "prefix"))
	tail := pluginapi.TextMessage(pluginapi.RoleUser, "suffix")
	tail.Name = "bob"
	tail.CacheBreakpoint = true
	p.Append(tail)
	applied, err := ApplyToChatRequest(req, p)
	require.NoError(t, err)
	require.Len(t, applied.Messages, 7)
	got := messageJSON(t, applied.Messages[0])
	assert.Equal(t, `{"role":"system","content":"prefix"}`, got)
	got = messageJSON(t, applied.Messages[6])
	assert.Equal(t, `{"role":"user","content":"suffix","cache_control":{"type":"ephemeral"},"name":"bob"}`, got)

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
	require.NoError(t, err)

	n := len(req.Messages)
	got := messageJSON(t, applied.Messages[n])
	assert.Equal(t, `{"role":"assistant","content":null,"tool_calls":[{"id":"call_2","type":"function","function":{"name":"f","arguments":"{\"a\":1}"}}]}`, got)
	got = messageJSON(t, applied.Messages[n+1])
	assert.Equal(t, `{"role":"tool","content":"done","tool_call_id":"call_2"}`, got)
	got = messageJSON(t, applied.Messages[n+2])
	assert.Equal(t, `{"role":"user","content":[{"type":"text","text":"look"},{"type":"image_url","image_url":{"url":"https://x/y.png"}}]}`, got)
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
			err := p.Remove(tt.order[1])
			require.NoError(t, err)

			applied, err := ApplyToChatRequest(req, p)
			require.NoError(t, err)
			require.Len(t, applied.Messages, 3)

			assertJSONEqual(t, req.Messages[0], applied.Messages[0])
			assertJSONEqual(t, req.Messages[1], applied.Messages[1])
			assertJSONEqual(t, req.Messages[4], applied.Messages[2])
		})
	}
}

func TestChatRemoveOnlyCallIsRejected(t *testing.T) {
	req, p := chatPrompt(t)
	require.Error(t, p.Remove("m2"))
	_, err := ApplyToChatRequest(req, p)
	require.ErrorContains(t, err, "m3")
}

func TestChatToolEdits(t *testing.T) {
	req, p := chatPrompt(t)
	err := p.SetToolArguments("m2", "call_1", json.RawMessage(`{"q":"y"}`))
	require.NoError(t, err)
	err = p.SetToolResult("m3", "call_1", []pluginapi.Part{{Kind: pluginapi.PartText, Text: "[redacted]"}})
	require.NoError(t, err)

	applied, err := ApplyToChatRequest(req, p)
	require.NoError(t, err)
	got := messageJSON(t, applied.Messages[2])
	assert.Equal(t, `{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{\"q\":\"y\"}"},"custom":1}]}`, got)
	got = messageJSON(t, applied.Messages[3])
	assert.Equal(t, `{"role":"tool","content":"[redacted]","tool_call_id":"call_1"}`, got)
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
				assert.Equal(t, 99, *r.MaxTokens)
				assert.Equal(t, 0.7, *r.Temperature)
				assert.Equal(t, 0.5, *r.TopP)
				assert.Equal(t, "u2", r.User)
				assert.Equal(t, "none", r.ToolChoice)
				assert.True(t, *r.ParallelToolCalls)
				require.NotNil(t, r.Reasoning)
				assert.Equal(t, "low", r.Reasoning.Effort)
				assert.Equal(t, `"bar"`, string(r.ExtraFields.Lookup("foo")))
				assert.Equal(t, `"yes"`, string(r.ExtraFields.Lookup("custom_top")))
				assert.Equal(t, "gpt-4o", r.Model)
				assert.Len(t, r.Messages, 5)
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
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)

			tt.check(t, applied)
		})
	}
}

func TestChatInterfaceContentAndAudio(t *testing.T) {
	req := decodeChat(t, `{"model":"m","messages":[{"role":"user","content":[{"type":"input_audio","input_audio":{"data":"AAAA","format":"wav"}},{"type":"text","text":"transcribe"}]}]}`)
	p, err := FromChatRequest(req)
	require.NoError(t, err)

	audio := p.Messages[0].Parts[0]
	assert.Equal(t, pluginapi.PartAudio, audio.Kind)
	assert.Equal(t, "audio/wav", audio.MediaType)
	assert.Equal(t, "AAAA", string(audio.Data))
	err = p.SetText("m0", 1, "translate")
	require.NoError(t, err)

	applied, err := ApplyToChatRequest(req, p)
	require.NoError(t, err)
	got := messageJSON(t, applied.Messages[0])
	assert.Equal(t, `{"role":"user","content":[{"type":"input_audio","input_audio":{"data":"AAAA","format":"wav"}},{"type":"text","text":"translate"}]}`, got)
}

func TestChatRequestFromMessages(t *testing.T) {
	temp := 0.1
	req := ChatRequestFromMessages("openai/gpt-4o", []pluginapi.Message{
		pluginapi.TextMessage(pluginapi.RoleSystem, "classify"),
		pluginapi.TextMessage(pluginapi.RoleUser, "hello"),
		{Role: pluginapi.RoleUser, Parts: []pluginapi.Part{{Kind: pluginapi.PartFile, URL: "https://x"}, {Kind: pluginapi.PartText, Text: "fallback"}}},
	}, 5, &temp)
	assert.Equal(t, "openai/gpt-4o", req.Model)
	assert.Equal(t, 5, *req.MaxTokens)
	assert.Equal(t, 0.1, *req.Temperature)
	require.Len(t, req.Messages, 3)
	got := messageJSON(t, req.Messages[0])
	assert.Equal(t, `{"role":"system","content":"classify"}`, got)
	got = messageJSON(t, req.Messages[2])
	assert.Equal(t, `{"role":"user","content":"fallback"}`, got)
	r := ChatRequestFromMessages("m", nil, 0, nil)
	assert.Nil(t, r.MaxTokens)
	assert.Empty(t, r.Messages)
}

func TestChatEditKeepsFilePart(t *testing.T) {
	req := decodeChat(t, `{"model":"m","messages":[
		{"role":"system","content":"be brief"},
		{"role":"user","content":[
			{"type":"text","text":"summarize"},
			{"type":"file","file":{"file_id":"file_123","filename":"report.pdf","x_file":1}},
			{"type":"file","file":{"file_data":"data:application/pdf;base64,JVBERi0="}}
		]}
	]}`)
	p, err := FromChatRequest(req)
	require.NoError(t, err)
	err = p.SetText("m0", 0, "be very brief")
	require.NoError(t, err)

	applied, err := ApplyToChatRequest(req, p)
	require.NoError(t, err)

	assertJSONEqual(t, req.Messages[1], applied.Messages[1])
	got := messageJSON(t, applied.Messages[1])
	want := `{"role":"user","content":[{"type":"text","text":"summarize"},{"type":"file","file":{"file_id":"file_123","filename":"report.pdf","x_file":1}},{"type":"file","file":{"file_data":"data:application/pdf;base64,JVBERi0="}}]}`
	assert.Equal(t, want, got)
}

// Tool-call ids may be empty or repeated, so envelopes are matched by
// position first and by a unique id second.
func TestPatchToolCallsMatchesByPositionThenUniqueID(t *testing.T) {
	extra := func(k string) core.UnknownJSONFields {
		return core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{k: json.RawMessage(`1`)})
	}
	unnamed := []core.ToolCall{
		{ID: "", Type: "function", ExtraFields: extra("first")},
		{ID: "", Type: "custom", ExtraFields: extra("second")},
	}
	out := patchToolCalls(unnamed, []pluginapi.ToolCall{{ID: "", Name: "a"}, {ID: "", Name: "b"}})
	require.Len(t, out, 2)
	assert.Equal(t, "custom", out[1].Type)
	assert.NotNil(t, out[1].ExtraFields.Lookup("second"))
	assert.NotNil(t, out[0].ExtraFields.Lookup("first"))

	repeated := []core.ToolCall{{ID: "x", Type: "function"}, {ID: "x", Type: "custom"}}
	out = patchToolCalls(repeated, []pluginapi.ToolCall{{ID: "x", Name: "a"}, {ID: "x", Name: "b"}})
	require.Len(t, out, 2)
	assert.Equal(t, "function", out[0].Type)
	assert.Equal(t, "custom", out[1].Type)

	reordered := []core.ToolCall{{ID: "a", Type: "function"}, {ID: "b", Type: "function", ExtraFields: extra("b")}}
	out = patchToolCalls(reordered, []pluginapi.ToolCall{{ID: "b", Name: "b"}, {ID: "a", Name: "a"}, {ID: "new", Name: "n"}})
	require.Len(t, out, 3)
	assert.Equal(t, "b", out[0].ID)
	assert.NotNil(t, out[0].ExtraFields.Lookup("b"))
	assert.Equal(t, "a", out[1].ID)
	assert.Equal(t, "new", out[2].ID)
	assert.Equal(t, "function", out[2].Type)
}

func TestChatSetMediaReencodesImageAndAudio(t *testing.T) {
	req, p := chatPrompt(t)
	redacted := []byte("redacted-png")
	err := p.SetMedia("m1", 1, "image/png", redacted)
	require.NoError(t, err)

	applied, err := ApplyToChatRequest(req, p)
	require.NoError(t, err)

	wantURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(redacted)
	want := `{"role":"user","content":[{"type":"text","text":"what is this?"},{"type":"image_url","image_url":{"url":"` + wantURL + `","detail":"low"}},{"type":"text","text":"second text"}],"name":"alice"}`
	got := messageJSON(t, applied.Messages[1])
	assert.Equal(t, want, got)
	assert.Equal(t, "data:image/png;base64,AAAA", req.Messages[1].Content.([]core.ContentPart)[1].ImageURL.URL)

	// Audio in a typed part, and image and audio in interface content.
	audioReq := &core.ChatRequest{Model: "m", Messages: []core.Message{
		{Role: "user", Content: []core.ContentPart{{Type: "input_audio", InputAudio: &core.InputAudioContent{Data: "AAAA", Format: "wav"}}}},
		{Role: "user", Content: []any{
			map[string]any{"type": "image_url", "image_url": map[string]any{"url": "https://x/y.png", "detail": "high"}},
			map[string]any{"type": "input_audio", "input_audio": map[string]any{"data": "AAAA", "format": "wav"}},
			map[string]any{"type": "text", "text": "hi"},
		}},
	}}
	p, err = FromChatRequest(audioReq)
	require.NoError(t, err)
	err = p.SetMedia("m0", 0, "audio/mp3", redacted)
	require.NoError(t, err)
	err = p.SetMedia("m1", 0, "image/jpeg", redacted)
	require.NoError(t, err)
	err = p.SetMedia("m1", 1, "audio/ogg", redacted)
	require.NoError(t, err)

	applied, err = ApplyToChatRequest(audioReq, p)
	require.NoError(t, err)

	b64 := base64.StdEncoding.EncodeToString(redacted)
	got = messageJSON(t, applied.Messages[0])
	assert.Equal(t, `{"role":"user","content":[{"type":"input_audio","input_audio":{"data":"`+b64+`","format":"mp3"}}]}`, got)

	want = `{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/jpeg;base64,` + b64 + `","detail":"high"}},{"type":"input_audio","input_audio":{"data":"` + b64 + `","format":"ogg"}},{"type":"text","text":"hi"}]}`
	got = messageJSON(t, applied.Messages[1])
	assert.Equal(t, want, got)
	assert.Equal(t, "https://x/y.png", audioReq.Messages[1].Content.([]any)[0].(map[string]any)["image_url"].(map[string]any)["url"])
}

// Replacing audio with the same bytes but another format still rewrites the
// format, in typed and interface content alike.
func TestChatSetMediaSameBytesNewFormat(t *testing.T) {
	same, err := base64.StdEncoding.DecodeString("AAAA")
	require.NoError(t, err)

	req := &core.ChatRequest{Model: "m", Messages: []core.Message{
		{Role: "user", Content: []core.ContentPart{{Type: "input_audio", InputAudio: &core.InputAudioContent{Data: "AAAA", Format: "wav"}}}},
		{Role: "user", Content: []any{map[string]any{"type": "input_audio", "input_audio": map[string]any{"data": "AAAA", "format": "wav"}}}},
	}}
	p, err := FromChatRequest(req)
	require.NoError(t, err)

	for _, id := range []string{"m0", "m1"} {
		err := p.SetMedia(id, 0, "audio/mp3", same)
		require.NoError(t, err)
	}
	applied, err := ApplyToChatRequest(req, p)
	require.NoError(t, err)

	for i := range applied.Messages {
		got := messageJSON(t, applied.Messages[i])
		assert.Equal(t, `{"role":"user","content":[{"type":"input_audio","input_audio":{"data":"AAAA","format":"mp3"}}]}`, got, "message %d = %s", i, got)
	}
	assert.Equal(t, "wav", req.Messages[1].Content.([]any)[0].(map[string]any)["input_audio"].(map[string]any)["format"])
}
