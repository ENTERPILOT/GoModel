package pluginapi

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func toolPrompt() *Prompt {
	p := &Prompt{Messages: []Message{
		{ID: "m0", Role: RoleSystem, Parts: []Part{{Kind: PartText, Text: "be brief"}}},
		{ID: "m1", Role: RoleUser, Parts: []Part{{Kind: PartText, Text: "weather?"}, {Kind: PartImage, URL: "https://x/y.png"}}},
		{ID: "m2", Role: RoleAssistant, Parts: []Part{{Kind: PartToolCall, ToolCall: &ToolCall{ID: "call_1", Name: "weather", Arguments: json.RawMessage(`{"city":"Oslo"}`)}}}},
		{ID: "m3", Role: RoleTool, ToolCallID: "call_1", Parts: []Part{{Kind: PartToolResult, ToolResult: &ToolResult{CallID: "call_1", Parts: []Part{{Kind: PartText, Text: "rain"}}}}}},
		{ID: "m4", Role: RoleUser, Parts: []Part{{Kind: PartText, Text: "thanks"}}},
	}}
	p.Reset()
	return p
}

func TestPromptViews(t *testing.T) {
	p := toolPrompt()
	if got := p.LastUser().ID; got != "m4" {
		t.Errorf("LastUser = %q, want m4", got)
	}
	if got := p.Text(); got != "be brief\nweather?\nthanks" {
		t.Errorf("Text() = %q", got)
	}
	if got := p.Text(RoleUser); got != "weather?\nthanks" {
		t.Errorf("Text(user) = %q", got)
	}
	if got := p.SystemText(); got != "be brief" {
		t.Errorf("SystemText = %q", got)
	}
	calls := p.ToolCalls()
	if len(calls) != 1 || calls[0].MessageID != "m2" || !calls[0].HasResult || calls[0].Call.Name != "weather" {
		t.Errorf("ToolCalls = %+v", calls)
	}
	if got := len(p.NewSince(3)); got != 2 {
		t.Errorf("NewSince(3) len = %d, want 2", got)
	}
	if got := p.NewSince(99); got != nil {
		t.Errorf("NewSince(99) = %v, want nil", got)
	}
	if p.Message("nope") != nil || p.Message("m3").Role != RoleTool {
		t.Error("Message lookup wrong")
	}
	if p.Changes().Dirty {
		t.Error("fresh prompt must not be dirty")
	}
}

func TestPromptSetText(t *testing.T) {
	tests := []struct {
		name    string
		msgID   string
		partIdx int
		wantErr string
	}{
		{name: "text part", msgID: "m1", partIdx: 0},
		{name: "unknown message", msgID: "zz", partIdx: 0, wantErr: "unknown message"},
		{name: "part out of range", msgID: "m1", partIdx: 5, wantErr: "no part 5"},
		{name: "non-text part", msgID: "m1", partIdx: 1, wantErr: "not text"},
		{name: "tool call part", msgID: "m2", partIdx: 0, wantErr: "not text"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := toolPrompt()
			err := p.SetText(tt.msgID, tt.partIdx, "new")
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
				}
				if p.Changes().Dirty {
					t.Error("failed edit must not dirty the prompt")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if p.Message(tt.msgID).Parts[tt.partIdx].Text != "new" {
				t.Error("text not applied")
			}
			ch := p.Changes()
			if !ch.Dirty || ch.Messages[tt.msgID] != ChangeEdited {
				t.Errorf("changes = %+v", ch)
			}
		})
	}
}

func TestPromptToolEdits(t *testing.T) {
	p := toolPrompt()
	if err := p.SetToolArguments("m2", "call_1", json.RawMessage(`{"city":"Bergen"}`)); err != nil {
		t.Fatal(err)
	}
	if got := string(p.Message("m2").Parts[0].ToolCall.Arguments); got != `{"city":"Bergen"}` {
		t.Errorf("arguments = %s", got)
	}
	if err := p.SetToolArguments("m2", "call_1", json.RawMessage(`{bad`)); err == nil {
		t.Error("invalid JSON must be rejected")
	}
	if err := p.SetToolArguments("m2", "call_9", json.RawMessage(`{}`)); err == nil {
		t.Error("unknown call must be rejected")
	}
	if err := p.SetToolResult("m3", "call_1", []Part{{Kind: PartText, Text: "[redacted]"}}); err != nil {
		t.Fatal(err)
	}
	if got := p.Message("m3").Text(); got != "[redacted]" {
		t.Errorf("tool result text = %q", got)
	}
	if err := p.SetToolResult("m3", "call_9", nil); err == nil {
		t.Error("unknown result must be rejected")
	}
	ch := p.Changes()
	if ch.Messages["m2"] != ChangeEdited || ch.Messages["m3"] != ChangeEdited {
		t.Errorf("changes = %+v", ch.Messages)
	}
}

func TestPromptInsertAppendRemove(t *testing.T) {
	p := toolPrompt()
	first := p.Insert(0, TextMessage(RoleSystem, "prefix"))
	last := p.Append(TextMessage(RoleUser, "suffix"))
	far := p.Insert(99, TextMessage(RoleUser, "clamped"))
	if first != "new-1" || last != "new-2" || far != "new-3" {
		t.Errorf("ids = %q %q %q", first, last, far)
	}
	if p.Messages[0].ID != first || p.Messages[len(p.Messages)-1].ID != far || p.Messages[len(p.Messages)-2].ID != last {
		t.Error("insert positions wrong")
	}
	ch := p.Changes()
	if ch.Messages[first] != ChangeInserted || ch.Messages[last] != ChangeInserted {
		t.Errorf("changes = %+v", ch.Messages)
	}

	// Editing an inserted message keeps it inserted.
	if err := p.SetText(first, 0, "prefix2"); err != nil {
		t.Fatal(err)
	}
	if p.Changes().Messages[first] != ChangeInserted {
		t.Error("edited inserted message must stay inserted")
	}

	// Removing an inserted message forgets it entirely.
	if err := p.Remove(first); err != nil {
		t.Fatal(err)
	}
	if _, ok := p.Changes().Messages[first]; ok {
		t.Error("removed inserted message must not be in changes")
	}
	if p.Message(first) != nil {
		t.Error("removed message still present")
	}

	// Removing an original records it.
	if err := p.Remove("m0"); err != nil {
		t.Fatal(err)
	}
	if p.Changes().Messages["m0"] != ChangeRemoved || p.Message("m0") != nil {
		t.Error("removed original not recorded")
	}
	if err := p.Remove("m0"); err == nil {
		t.Error("second removal must fail")
	}
	if err := p.Remove("zz"); err == nil {
		t.Error("unknown id must fail")
	}
	if err := p.Validate(); err != nil {
		t.Errorf("Validate = %v", err)
	}

	// IDs never collide with ones the host handed out.
	q := &Prompt{Messages: []Message{{ID: "new-1", Role: RoleUser}}}
	q.Reset()
	if id := q.Append(TextMessage(RoleUser, "x")); id != "new-2" {
		t.Errorf("colliding id reuse: %q", id)
	}
}

func TestPromptRemoveToolPairs(t *testing.T) {
	tests := []struct {
		name  string
		order []string
	}{
		{name: "call then result", order: []string{"m2", "m3"}},
		{name: "result then call", order: []string{"m3", "m2"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := toolPrompt()
			err := p.Remove(tt.order[0])
			var dangling *DanglingToolError
			if !errors.As(err, &dangling) {
				t.Fatalf("first removal err = %v, want DanglingToolError", err)
			}
			if dangling.PartnerID != tt.order[1] || dangling.CallID != "call_1" {
				t.Errorf("dangling = %+v", dangling)
			}
			if p.Message(tt.order[0]) != nil {
				t.Error("first message must be removed despite the error")
			}
			if verr := p.Validate(); !errors.As(verr, &dangling) {
				t.Errorf("Validate = %v, want dangling", verr)
			}
			if err := p.Remove(dangling.PartnerID); err != nil {
				t.Fatalf("second removal err = %v", err)
			}
			if err := p.Validate(); err != nil {
				t.Errorf("Validate after both removed = %v", err)
			}
			ch := p.Changes()
			if ch.Messages["m2"] != ChangeRemoved || ch.Messages["m3"] != ChangeRemoved {
				t.Errorf("changes = %+v", ch.Messages)
			}
			if len(p.ToolCalls()) != 0 {
				t.Error("tool calls must be gone")
			}
		})
	}
}

func TestPromptSetParam(t *testing.T) {
	p := toolPrompt()
	p.SetParam("max_tokens", 42)
	p.SetParam("temperature", 0.5)
	p.SetParam("top_p", json.Number("0.9"))
	p.SetParam("user", "alice")
	if p.Params.MaxTokens == nil || *p.Params.MaxTokens != 42 {
		t.Error("max_tokens not applied to Params")
	}
	if p.Params.Temperature == nil || *p.Params.Temperature != 0.5 {
		t.Error("temperature not applied to Params")
	}
	if p.Params.TopP == nil || *p.Params.TopP != 0.9 {
		t.Error("top_p not applied to Params")
	}
	ch := p.Changes()
	if !ch.Dirty || len(ch.Params) != 4 || ch.Params["user"] != "alice" {
		t.Errorf("changes = %+v", ch)
	}
	p.Reset()
	if p.Changes().Dirty {
		t.Error("Reset must clear tracking")
	}
	// Changes() returns a copy.
	p.SetParam("x", 1)
	ch = p.Changes()
	ch.Params["y"] = 2
	if _, ok := p.Changes().Params["y"]; ok {
		t.Error("Changes must return a copy")
	}
}

func TestValuesNilSafe(t *testing.T) {
	var v Values
	if _, ok := v.Get("k"); ok {
		t.Error("nil Values.Get must report absent")
	}
	v = Values{}
	v.Set("k", 1)
	if got, ok := v.Get("k"); !ok || got != 1 {
		t.Error("Set/Get round trip failed")
	}
}
