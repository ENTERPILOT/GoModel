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

const responsesFixture = `{
  "model": "gpt-5",
  "instructions": "be kind",
  "input": [
    {"type": "message", "role": "user", "content": [
      {"type": "input_text", "text": "hi"},
      {"type": "input_image", "image_url": "https://x/y.png", "detail": "low"}
    ]},
    {"type": "function_call", "call_id": "call_1", "name": "lookup", "arguments": "{\"q\":1}", "status": "completed"},
    {"type": "function_call_output", "call_id": "call_1", "output": "found"},
    {"type": "reasoning", "id": "rs_1", "summary": [], "encrypted_content": "abc"},
    {"role": "assistant", "content": "ok", "meta": {"k": "v"}}
  ],
  "max_output_tokens": 50,
  "store": true,
  "custom_field": 123
}`

func responsesPrompt(t *testing.T) (*core.ResponsesRequest, *pluginapi.Prompt) {
	t.Helper()
	req := decodeResponses(t, responsesFixture)
	p, err := FromResponsesRequest(req)
	require.NoError(t, err)

	return req, p
}

func TestFromResponsesRequestMapping(t *testing.T) {
	_, p := responsesPrompt(t)
	require.Len(t, p.Messages, 6)

	ins, m0, m1, m2, m3, m4 := p.Messages[0], p.Messages[1], p.Messages[2], p.Messages[3], p.Messages[4], p.Messages[5]
	assert.Equal(t, InstructionsMessageID, ins.ID)
	assert.Equal(t, pluginapi.RoleSystem, ins.Role)
	assert.Equal(t, "be kind", ins.Text())
	assert.Equal(t, "m0", m0.ID)
	assert.Equal(t, pluginapi.RoleUser, m0.Role)
	require.Len(t, m0.Parts, 2)
	assert.Equal(t, "hi", m0.Parts[0].Text)
	assert.Equal(t, pluginapi.PartImage, m0.Parts[1].Kind)
	assert.Equal(t, "https://x/y.png", m0.Parts[1].URL)
	assert.NotEmpty(t, m0.Parts[1].Raw)
	assert.Equal(t, pluginapi.RoleAssistant, m1.Role)
	assert.Equal(t, pluginapi.PartToolCall, m1.Parts[0].Kind)
	assert.Equal(t, "call_1", m1.Parts[0].ToolCall.ID)
	assert.Equal(t, `{"q":1}`, string(m1.Parts[0].ToolCall.Arguments))
	assert.Equal(t, pluginapi.RoleTool, m2.Role)
	assert.Equal(t, "call_1", m2.ToolCallID)
	assert.Equal(t, "found", m2.Text())
	assert.Equal(t, pluginapi.RoleAssistant, m3.Role)
	require.Len(t, m3.Parts, 1)
	assert.Equal(t, pluginapi.PartOpaque, m3.Parts[0].Kind)
	assert.Contains(t, string(m3.Parts[0].Raw), "encrypted_content", "m3 = %+v", m3)
	assert.Equal(t, pluginapi.RoleAssistant, m4.Role)
	assert.Equal(t, "ok", m4.Text())
	assert.Equal(t, 50, *p.Params.MaxTokens)
	assert.Equal(t, "gpt-5", p.Params.Model)
	assert.Equal(t, true, p.Params.Extra["store"])
	assert.Equal(t, float64(123), p.Params.Extra["custom_field"])
}

func TestResponsesRoundTripNoEdits(t *testing.T) {
	req, p := responsesPrompt(t)
	applied, err := ApplyToResponsesRequest(req, p)
	require.NoError(t, err)

	assertJSONEqual(t, req, applied)
}

func TestResponsesEditStructuredTextPart(t *testing.T) {
	req, p := responsesPrompt(t)
	err := p.SetText("m0", 0, "hello")
	require.NoError(t, err)

	applied, err := ApplyToResponsesRequest(req, p)
	require.NoError(t, err)

	elements := applied.Input.([]core.ResponsesInputElement)
	var want, got any
	err = json.Unmarshal([]byte(`{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"},{"type":"input_image","image_url":"https://x/y.png","detail":"low"}]}`), &want)
	require.NoError(t, err)
	err = json.Unmarshal(mustJSON(t, elements[0]), &got)
	require.NoError(t, err)

	assertJSONEqual(t, want, got)
	orig := req.Input.([]core.ResponsesInputElement)
	for i := 1; i < len(orig); i++ {
		assertJSONEqual(t, orig[i], elements[i])
	}
	assert.Equal(t, "be kind", applied.Instructions)
}

func TestResponsesInstructions(t *testing.T) {
	t.Run("edit", func(t *testing.T) {
		req, p := responsesPrompt(t)
		err := p.SetText(InstructionsMessageID, 0, "be strict")
		require.NoError(t, err)

		applied, err := ApplyToResponsesRequest(req, p)
		require.NoError(t, err)
		assert.Equal(t, "be strict", applied.Instructions)

		assertJSONEqual(t, req.Input, applied.Input)
	})
	t.Run("remove", func(t *testing.T) {
		req, p := responsesPrompt(t)
		err := p.Remove(InstructionsMessageID)
		require.NoError(t, err)

		applied, err := ApplyToResponsesRequest(req, p)
		require.NoError(t, err)
		assert.Empty(t, applied.Instructions)

		assertJSONEqual(t, req.Input, applied.Input)
	})
	t.Run("insert system at 0 without instructions", func(t *testing.T) {
		req := decodeResponses(t, `{"model":"m","input":[{"role":"user","content":"hi"}]}`)
		p, err := FromResponsesRequest(req)
		require.NoError(t, err)

		p.Insert(0, pluginapi.TextMessage(pluginapi.RoleSystem, "guard"))
		applied, err := ApplyToResponsesRequest(req, p)
		require.NoError(t, err)
		assert.Equal(t, "guard", applied.Instructions)

		assertJSONEqual(t, req.Input, applied.Input)
	})
	t.Run("insert system elsewhere becomes an input item", func(t *testing.T) {
		req, p := responsesPrompt(t)
		p.Append(pluginapi.TextMessage(pluginapi.RoleSystem, "tail"))
		applied, err := ApplyToResponsesRequest(req, p)
		require.NoError(t, err)

		elements := applied.Input.([]core.ResponsesInputElement)
		got := messageJSON(t, elements[len(elements)-1])
		assert.Equal(t, `{"type":"message","role":"system","content":"tail"}`, got)
	})
}

func TestResponsesStringInput(t *testing.T) {
	tests := []struct {
		name string
		edit func(p *pluginapi.Prompt)
		want any
	}{
		{name: "untouched", edit: func(*pluginapi.Prompt) {}, want: "hello"},
		{name: "edited stays a string", edit: func(p *pluginapi.Prompt) { _ = p.SetText("m0", 0, "bye") }, want: "bye"},
		{name: "removed", edit: func(p *pluginapi.Prompt) { _ = p.Remove("m0") }, want: ""},
		{
			name: "append becomes an array",
			edit: func(p *pluginapi.Prompt) { p.Append(pluginapi.TextMessage(pluginapi.RoleUser, "more")) },
			want: []core.ResponsesInputElement{
				{Type: "message", Role: "user", Content: "hello"},
				{Type: "message", Role: "user", Content: "more"},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := decodeResponses(t, `{"model":"m","input":"hello"}`)
			p, err := FromResponsesRequest(req)
			require.NoError(t, err)
			require.Equal(t, "m0", p.Messages[0].ID)
			require.Equal(t, "hello", p.Messages[0].Text())

			tt.edit(p)
			applied, err := ApplyToResponsesRequest(req, p)
			require.NoError(t, err)

			assertJSONEqual(t, tt.want, applied.Input)
		})
	}
}

func TestResponsesInputEnvelopeShapes(t *testing.T) {
	items := []map[string]any{
		{"type": "message", "role": "user", "content": "hi", "meta": map[string]any{"k": "v"}},
		{"type": "reasoning", "id": "rs_1", "encrypted_content": "abc"},
		{"type": "function_call_output", "call_id": "c1", "output": map[string]any{"ok": true}},
	}
	asAny := make([]any, len(items))
	for i, m := range items {
		asAny[i] = m
	}
	tests := []struct {
		name  string
		input any
	}{
		{name: "map slice", input: items},
		{name: "interface slice", input: asAny},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := &core.ResponsesRequest{Model: "m", Input: tt.input}
			p, err := FromResponsesRequest(req)
			require.NoError(t, err)
			err = p.SetText("m0", 0, "hello")
			require.NoError(t, err)

			applied, err := ApplyToResponsesRequest(req, p)
			require.NoError(t, err)

			var got []any
			switch out := applied.Input.(type) {
			case []map[string]any:
				require.Equal(t, "map slice", tt.name, "container changed to %T", applied.Input)
				for _, m := range out {
					got = append(got, m)
				}
			case []any:
				require.Equal(t, "interface slice", tt.name, "container changed to %T", applied.Input)
				got = out
			default:
				t.Fatalf("container changed to %T", applied.Input)
			}
			first := got[0].(map[string]any)
			assert.Equal(t, "hello", first["content"])
			assert.Equal(t, "v", first["meta"].(map[string]any)["k"], "edited item = %v", first)

			assertJSONEqual(t, items[1], got[1])
			assertJSONEqual(t, items[2], got[2])
		})
	}
}

func TestResponsesOpaqueItemsPreserved(t *testing.T) {
	req, p := responsesPrompt(t)
	err := p.SetText("m4", 0, "fine")
	require.NoError(t, err)
	assert.Error(t, p.SetText("m3", 0, "x"))

	applied, err := ApplyToResponsesRequest(req, p)
	require.NoError(t, err)

	orig := req.Input.([]core.ResponsesInputElement)
	elements := applied.Input.([]core.ResponsesInputElement)
	assertJSONEqual(t, orig[3], elements[3])
	got := messageJSON(t, elements[4])
	assert.Equal(t, `{"role":"assistant","content":"fine","meta":{"k":"v"}}`, got)
}

func TestResponsesToolEditsAndRemoval(t *testing.T) {
	req, p := responsesPrompt(t)
	err := p.SetToolArguments("m1", "call_1", json.RawMessage(`{"q":2}`))
	require.NoError(t, err)
	err = p.SetToolResult("m2", "call_1", []pluginapi.Part{{Kind: pluginapi.PartText, Text: "[redacted]"}})
	require.NoError(t, err)

	applied, err := ApplyToResponsesRequest(req, p)
	require.NoError(t, err)

	elements := applied.Input.([]core.ResponsesInputElement)
	got := messageJSON(t, elements[1])
	assert.Equal(t, `{"type":"function_call","call_id":"call_1","name":"lookup","arguments":"{\"q\":2}","status":"completed"}`, got)
	got = messageJSON(t, elements[2])
	assert.Equal(t, `{"type":"function_call_output","call_id":"call_1","output":"[redacted]"}`, got)

	req, p = responsesPrompt(t)
	_ = p.Remove("m2")
	err = p.Remove("m1")
	require.NoError(t, err)

	applied, err = ApplyToResponsesRequest(req, p)
	require.NoError(t, err)

	orig := req.Input.([]core.ResponsesInputElement)
	elements = applied.Input.([]core.ResponsesInputElement)
	require.Len(t, elements, 3)

	assertJSONEqual(t, orig[0], elements[0])
	assertJSONEqual(t, orig[3], elements[1])
	assertJSONEqual(t, orig[4], elements[2])
}

func TestResponsesInsertedToolMessages(t *testing.T) {
	req, p := responsesPrompt(t)
	p.Append(pluginapi.Message{Role: pluginapi.RoleAssistant, Parts: []pluginapi.Part{
		{Kind: pluginapi.PartText, Text: "calling"},
		{Kind: pluginapi.PartToolCall, ToolCall: &pluginapi.ToolCall{ID: "call_2", Name: "f", Arguments: json.RawMessage(`{}`)}},
	}})
	p.Append(pluginapi.Message{Role: pluginapi.RoleTool, ToolCallID: "call_2", Parts: []pluginapi.Part{
		{Kind: pluginapi.PartToolResult, ToolResult: &pluginapi.ToolResult{CallID: "call_2", Parts: []pluginapi.Part{{Kind: pluginapi.PartText, Text: "done"}}}},
	}})
	applied, err := ApplyToResponsesRequest(req, p)
	require.NoError(t, err)

	elements := applied.Input.([]core.ResponsesInputElement)
	tail := elements[len(elements)-3:]
	got := messageJSON(t, tail[0])
	assert.Equal(t, `{"type":"message","role":"assistant","content":"calling"}`, got)
	got = messageJSON(t, tail[1])
	assert.Equal(t, `{"type":"function_call","call_id":"call_2","name":"f","arguments":"{}"}`, got)
	got = messageJSON(t, tail[2])
	assert.Equal(t, `{"type":"function_call_output","call_id":"call_2","output":"done"}`, got)
}

func TestResponsesParams(t *testing.T) {
	req, p := responsesPrompt(t)
	p.SetParam("max_tokens", 7)
	p.SetParam("metadata", map[string]any{"team": "a"})
	p.SetParam("custom", 1)
	applied, err := ApplyToResponsesRequest(req, p)
	require.NoError(t, err)
	assert.Equal(t, 7, *applied.MaxOutputTokens)
	assert.Equal(t, "a", applied.Metadata["team"])
	assert.Equal(t, "1", string(applied.ExtraFields.Lookup("custom")), "applied = %+v extra=%s", applied, applied.ExtraFields.Lookup("custom"))
	assert.Equal(t, "123", string(applied.ExtraFields.Lookup("custom_field")))
	require.NotNil(t, applied.Store)
	assert.True(t, *applied.Store)

	assertJSONEqual(t, req.Input, applied.Input)

	req, p = responsesPrompt(t)
	p.SetParam("model", "other")
	_, err = ApplyToResponsesRequest(req, p)
	assert.Error(t, err)
}

func TestResponsesSetMediaReencodesImage(t *testing.T) {
	req, p := responsesPrompt(t)
	redacted := []byte("redacted-png")
	err := p.SetMedia("m0", 1, "image/png", redacted)
	require.NoError(t, err)

	applied, err := ApplyToResponsesRequest(req, p)
	require.NoError(t, err)

	elements := applied.Input.([]core.ResponsesInputElement)
	wantURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(redacted)
	var want, got any
	err = json.Unmarshal([]byte(`{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"},{"type":"input_image","image_url":"`+wantURL+`","detail":"low"}]}`), &want)
	require.NoError(t, err)
	err = json.Unmarshal(mustJSON(t, elements[0]), &got)
	require.NoError(t, err)

	assertJSONEqual(t, want, got)
	orig := req.Input.([]core.ResponsesInputElement)
	for i := 1; i < len(orig); i++ {
		assertJSONEqual(t, orig[i], elements[i])
	}

	// An image block whose image_url is an object keeps the object.
	objReq := decodeResponses(t, `{"model":"m","input":[{"type":"message","role":"user","content":[{"type":"input_image","image_url":{"url":"https://x/y.png","detail":"auto"}}]}]}`)
	p, err = FromResponsesRequest(objReq)
	require.NoError(t, err)
	err = p.SetMedia("m0", 0, "image/png", redacted)
	require.NoError(t, err)

	applied, err = ApplyToResponsesRequest(objReq, p)
	require.NoError(t, err)
	err = json.Unmarshal([]byte(`{"type":"message","role":"user","content":[{"type":"input_image","image_url":{"url":"`+wantURL+`","detail":"auto"}}]}`), &want)
	require.NoError(t, err)
	err = json.Unmarshal(mustJSON(t, applied.Input.([]core.ResponsesInputElement)[0]), &got)
	require.NoError(t, err)

	assertJSONEqual(t, want, got)
	var original any
	err = json.Unmarshal(mustJSON(t, objReq.Input.([]core.ResponsesInputElement)[0]), &original)
	require.NoError(t, err)
	require.NotContains(t, string(mustJSON(t, original)), wantURL, "original request was mutated")
}
