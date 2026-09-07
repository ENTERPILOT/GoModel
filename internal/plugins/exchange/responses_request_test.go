package exchange

import (
	"strings"
	"testing"

	"github.com/goccy/go-json"

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
	if err != nil {
		t.Fatal(err)
	}
	return req, p
}

func TestFromResponsesRequestMapping(t *testing.T) {
	_, p := responsesPrompt(t)
	if len(p.Messages) != 6 {
		t.Fatalf("messages = %d", len(p.Messages))
	}
	ins, m0, m1, m2, m3, m4 := p.Messages[0], p.Messages[1], p.Messages[2], p.Messages[3], p.Messages[4], p.Messages[5]
	if ins.ID != InstructionsMessageID || ins.Role != pluginapi.RoleSystem || ins.Text() != "be kind" {
		t.Errorf("instructions = %+v", ins)
	}
	if m0.ID != "m0" || m0.Role != pluginapi.RoleUser || len(m0.Parts) != 2 || m0.Parts[0].Text != "hi" || m0.Parts[1].Kind != pluginapi.PartImage || m0.Parts[1].URL != "https://x/y.png" || len(m0.Parts[1].Raw) == 0 {
		t.Errorf("m0 = %+v", m0)
	}
	if m1.Role != pluginapi.RoleAssistant || m1.Parts[0].Kind != pluginapi.PartToolCall || m1.Parts[0].ToolCall.ID != "call_1" || string(m1.Parts[0].ToolCall.Arguments) != `{"q":1}` {
		t.Errorf("m1 = %+v", m1)
	}
	if m2.Role != pluginapi.RoleTool || m2.ToolCallID != "call_1" || m2.Text() != "found" {
		t.Errorf("m2 = %+v", m2)
	}
	if m3.Role != pluginapi.RoleAssistant || len(m3.Parts) != 1 || m3.Parts[0].Kind != pluginapi.PartOpaque || !strings.Contains(string(m3.Parts[0].Raw), "encrypted_content") {
		t.Errorf("m3 = %+v", m3)
	}
	if m4.Role != pluginapi.RoleAssistant || m4.Text() != "ok" {
		t.Errorf("m4 = %+v", m4)
	}
	if *p.Params.MaxTokens != 50 || p.Params.Model != "gpt-5" || p.Params.Extra["store"] != true || p.Params.Extra["custom_field"] != float64(123) {
		t.Errorf("params = %+v", p.Params)
	}
}

func TestResponsesRoundTripNoEdits(t *testing.T) {
	req, p := responsesPrompt(t)
	applied, err := ApplyToResponsesRequest(req, p)
	if err != nil {
		t.Fatal(err)
	}
	assertJSONEqual(t, req, applied)
}

func TestResponsesEditStructuredTextPart(t *testing.T) {
	req, p := responsesPrompt(t)
	if err := p.SetText("m0", 0, "hello"); err != nil {
		t.Fatal(err)
	}
	applied, err := ApplyToResponsesRequest(req, p)
	if err != nil {
		t.Fatal(err)
	}
	elements := applied.Input.([]core.ResponsesInputElement)
	var want, got any
	if err := json.Unmarshal([]byte(`{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"},{"type":"input_image","image_url":"https://x/y.png","detail":"low"}]}`), &want); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(mustJSON(t, elements[0]), &got); err != nil {
		t.Fatal(err)
	}
	assertJSONEqual(t, want, got)
	orig := req.Input.([]core.ResponsesInputElement)
	for i := 1; i < len(orig); i++ {
		assertJSONEqual(t, orig[i], elements[i])
	}
	if applied.Instructions != "be kind" {
		t.Error("instructions changed")
	}
}

func TestResponsesInstructions(t *testing.T) {
	t.Run("edit", func(t *testing.T) {
		req, p := responsesPrompt(t)
		if err := p.SetText(InstructionsMessageID, 0, "be strict"); err != nil {
			t.Fatal(err)
		}
		applied, err := ApplyToResponsesRequest(req, p)
		if err != nil {
			t.Fatal(err)
		}
		if applied.Instructions != "be strict" {
			t.Errorf("instructions = %q", applied.Instructions)
		}
		assertJSONEqual(t, req.Input, applied.Input)
	})
	t.Run("remove", func(t *testing.T) {
		req, p := responsesPrompt(t)
		if err := p.Remove(InstructionsMessageID); err != nil {
			t.Fatal(err)
		}
		applied, err := ApplyToResponsesRequest(req, p)
		if err != nil {
			t.Fatal(err)
		}
		if applied.Instructions != "" {
			t.Errorf("instructions = %q, want cleared", applied.Instructions)
		}
		assertJSONEqual(t, req.Input, applied.Input)
	})
	t.Run("insert system at 0 without instructions", func(t *testing.T) {
		req := decodeResponses(t, `{"model":"m","input":[{"role":"user","content":"hi"}]}`)
		p, err := FromResponsesRequest(req)
		if err != nil {
			t.Fatal(err)
		}
		p.Insert(0, pluginapi.TextMessage(pluginapi.RoleSystem, "guard"))
		applied, err := ApplyToResponsesRequest(req, p)
		if err != nil {
			t.Fatal(err)
		}
		if applied.Instructions != "guard" {
			t.Errorf("instructions = %q", applied.Instructions)
		}
		assertJSONEqual(t, req.Input, applied.Input)
	})
	t.Run("insert system elsewhere becomes an input item", func(t *testing.T) {
		req, p := responsesPrompt(t)
		p.Append(pluginapi.TextMessage(pluginapi.RoleSystem, "tail"))
		applied, err := ApplyToResponsesRequest(req, p)
		if err != nil {
			t.Fatal(err)
		}
		elements := applied.Input.([]core.ResponsesInputElement)
		if got := messageJSON(t, elements[len(elements)-1]); got != `{"type":"message","role":"system","content":"tail"}` {
			t.Errorf("tail = %s", got)
		}
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
			if err != nil {
				t.Fatal(err)
			}
			if p.Messages[0].ID != "m0" || p.Messages[0].Text() != "hello" {
				t.Fatalf("m0 = %+v", p.Messages[0])
			}
			tt.edit(p)
			applied, err := ApplyToResponsesRequest(req, p)
			if err != nil {
				t.Fatal(err)
			}
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
			if err != nil {
				t.Fatal(err)
			}
			if err := p.SetText("m0", 0, "hello"); err != nil {
				t.Fatal(err)
			}
			applied, err := ApplyToResponsesRequest(req, p)
			if err != nil {
				t.Fatal(err)
			}
			var got []any
			switch out := applied.Input.(type) {
			case []map[string]any:
				if tt.name != "map slice" {
					t.Fatalf("container changed to %T", applied.Input)
				}
				for _, m := range out {
					got = append(got, m)
				}
			case []any:
				if tt.name != "interface slice" {
					t.Fatalf("container changed to %T", applied.Input)
				}
				got = out
			default:
				t.Fatalf("container changed to %T", applied.Input)
			}
			first := got[0].(map[string]any)
			if first["content"] != "hello" || first["meta"].(map[string]any)["k"] != "v" {
				t.Errorf("edited item = %v", first)
			}
			assertJSONEqual(t, items[1], got[1])
			assertJSONEqual(t, items[2], got[2])
		})
	}
}

func TestResponsesOpaqueItemsPreserved(t *testing.T) {
	req, p := responsesPrompt(t)
	if err := p.SetText("m4", 0, "fine"); err != nil {
		t.Fatal(err)
	}
	if err := p.SetText("m3", 0, "x"); err == nil {
		t.Error("opaque part must not be editable")
	}
	applied, err := ApplyToResponsesRequest(req, p)
	if err != nil {
		t.Fatal(err)
	}
	orig := req.Input.([]core.ResponsesInputElement)
	elements := applied.Input.([]core.ResponsesInputElement)
	assertJSONEqual(t, orig[3], elements[3])
	if got := messageJSON(t, elements[4]); got != `{"role":"assistant","content":"fine","meta":{"k":"v"}}` {
		t.Errorf("edited = %s", got)
	}
}

func TestResponsesToolEditsAndRemoval(t *testing.T) {
	req, p := responsesPrompt(t)
	if err := p.SetToolArguments("m1", "call_1", json.RawMessage(`{"q":2}`)); err != nil {
		t.Fatal(err)
	}
	if err := p.SetToolResult("m2", "call_1", []pluginapi.Part{{Kind: pluginapi.PartText, Text: "[redacted]"}}); err != nil {
		t.Fatal(err)
	}
	applied, err := ApplyToResponsesRequest(req, p)
	if err != nil {
		t.Fatal(err)
	}
	elements := applied.Input.([]core.ResponsesInputElement)
	if got := messageJSON(t, elements[1]); got != `{"type":"function_call","call_id":"call_1","name":"lookup","arguments":"{\"q\":2}","status":"completed"}` {
		t.Errorf("call = %s", got)
	}
	if got := messageJSON(t, elements[2]); got != `{"type":"function_call_output","call_id":"call_1","output":"[redacted]"}` {
		t.Errorf("output = %s", got)
	}

	req, p = responsesPrompt(t)
	_ = p.Remove("m2")
	if err := p.Remove("m1"); err != nil {
		t.Fatal(err)
	}
	applied, err = ApplyToResponsesRequest(req, p)
	if err != nil {
		t.Fatal(err)
	}
	orig := req.Input.([]core.ResponsesInputElement)
	elements = applied.Input.([]core.ResponsesInputElement)
	if len(elements) != 3 {
		t.Fatalf("elements = %d", len(elements))
	}
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
	if err != nil {
		t.Fatal(err)
	}
	elements := applied.Input.([]core.ResponsesInputElement)
	tail := elements[len(elements)-3:]
	if got := messageJSON(t, tail[0]); got != `{"type":"message","role":"assistant","content":"calling"}` {
		t.Errorf("message = %s", got)
	}
	if got := messageJSON(t, tail[1]); got != `{"type":"function_call","call_id":"call_2","name":"f","arguments":"{}"}` {
		t.Errorf("call = %s", got)
	}
	if got := messageJSON(t, tail[2]); got != `{"type":"function_call_output","call_id":"call_2","output":"done"}` {
		t.Errorf("output = %s", got)
	}
}

func TestResponsesParams(t *testing.T) {
	req, p := responsesPrompt(t)
	p.SetParam("max_tokens", 7)
	p.SetParam("metadata", map[string]any{"team": "a"})
	p.SetParam("custom", 1)
	applied, err := ApplyToResponsesRequest(req, p)
	if err != nil {
		t.Fatal(err)
	}
	if *applied.MaxOutputTokens != 7 || applied.Metadata["team"] != "a" || string(applied.ExtraFields.Lookup("custom")) != "1" {
		t.Errorf("applied = %+v extra=%s", applied, applied.ExtraFields.Lookup("custom"))
	}
	if string(applied.ExtraFields.Lookup("custom_field")) != "123" || applied.Store == nil || !*applied.Store {
		t.Error("existing fields lost")
	}
	assertJSONEqual(t, req.Input, applied.Input)

	req, p = responsesPrompt(t)
	p.SetParam("model", "other")
	if _, err := ApplyToResponsesRequest(req, p); err == nil {
		t.Error("model must be frozen")
	}
}
