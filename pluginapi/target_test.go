package pluginapi

import (
	"reflect"
	"strings"
	"testing"
)

func TestPromptTextTargets(t *testing.T) {
	p := toolPrompt()
	p.Messages[3].Parts[0].ToolResult.Parts = append(p.Messages[3].Parts[0].ToolResult.Parts,
		Part{Kind: PartImage, URL: "https://x/radar.png"},
		Part{Kind: PartText, Text: "later sun"},
	)

	got := p.TextTargets()
	want := []TextTarget{
		{MessageID: "m0", Role: RoleSystem, Part: 0, Text: "be brief"},
		{MessageID: "m1", Role: RoleUser, Part: 0, Text: "weather?"},
		{MessageID: "m3", Role: RoleTool, Part: 0, CallID: "call_1", ResultPart: 0, Text: "rain"},
		{MessageID: "m3", Role: RoleTool, Part: 0, CallID: "call_1", ResultPart: 2, Text: "later sun"},
		{MessageID: "m4", Role: RoleUser, Part: 0, Text: "thanks"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("TextTargets() = %+v\nwant %+v", got, want)
	}

	users := p.TextTargets(RoleUser)
	if len(users) != 2 || users[0].Text != "weather?" || users[1].Text != "thanks" {
		t.Fatalf("TextTargets(user) = %+v", users)
	}
	if got := p.TextTargets(RoleSystem, RoleTool); len(got) != 3 {
		t.Fatalf("TextTargets(system, tool) = %+v", got)
	}
	if got := p.TextTargets(RoleDeveloper); got != nil {
		t.Fatalf("TextTargets(developer) = %+v, want nil", got)
	}
}

func TestPromptSetTargetText(t *testing.T) {
	p := toolPrompt()
	p.Messages[3].Parts[0].ToolResult.Parts = append(p.Messages[3].Parts[0].ToolResult.Parts, Part{Kind: PartText, Text: "later sun"})
	targets := p.TextTargets()

	for _, target := range targets {
		if err := p.SetTargetText(target, strings.ToUpper(target.Text)); err != nil {
			t.Fatalf("SetTargetText(%+v) error = %v", target, err)
		}
	}
	if got := p.Messages[1].Parts[0].Text; got != "WEATHER?" {
		t.Fatalf("user text = %q", got)
	}
	result := p.Messages[3].Parts[0].ToolResult.Parts
	if result[0].Text != "RAIN" || result[1].Text != "LATER SUN" {
		t.Fatalf("tool result parts = %+v; successive edits must compose", result)
	}
	changes := p.Changes()
	for _, id := range []string{"m0", "m1", "m3", "m4"} {
		if changes.Messages[id] != ChangeEdited {
			t.Fatalf("message %s change = %q, want edited", id, changes.Messages[id])
		}
	}
	if _, ok := changes.Messages["m2"]; ok {
		t.Fatal("untouched message m2 was marked")
	}
	for _, target := range p.TextTargets() {
		if target.Text != strings.ToUpper(target.Text) {
			t.Fatalf("relisted target %+v does not reflect the edit", target)
		}
	}
}

func TestPromptSetTargetTextErrors(t *testing.T) {
	p := toolPrompt()
	tests := []struct {
		name   string
		target TextTarget
		want   string
	}{
		{"unknown message", TextTarget{MessageID: "nope", Part: 0}, "unknown message"},
		{"non-text part", TextTarget{MessageID: "m1", Part: 1}, "not text"},
		{"unknown tool message", TextTarget{MessageID: "nope", Part: 0, CallID: "call_1"}, "unknown message"},
		{"holder out of range", TextTarget{MessageID: "m3", Part: 4, CallID: "call_1"}, "no part 4"},
		{"holder is not the result", TextTarget{MessageID: "m1", Part: 0, CallID: "call_1"}, "not the result of tool call"},
		{"wrong call id", TextTarget{MessageID: "m3", Part: 0, CallID: "call_9"}, "not the result of tool call"},
		{"result part out of range", TextTarget{MessageID: "m3", Part: 0, CallID: "call_1", ResultPart: 3}, "no part 3"},
	}
	p.Messages[3].Parts[0].ToolResult.Parts = append(p.Messages[3].Parts[0].ToolResult.Parts, Part{Kind: PartImage, URL: "https://x/r.png"})
	tests = append(tests, struct {
		name   string
		target TextTarget
		want   string
	}{"result part is not text", TextTarget{MessageID: "m3", Part: 0, CallID: "call_1", ResultPart: 1}, "not text"})

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := p.SetTargetText(tt.target, "x")
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want containing %q", err, tt.want)
			}
		})
	}
	if p.Changes().Dirty {
		t.Fatal("failed edits must not mark the prompt dirty")
	}
}

func TestCompletionTextTargets(t *testing.T) {
	c := &Completion{Choices: []Choice{
		{Index: 0, Message: Message{Role: RoleAssistant, Parts: []Part{{Kind: PartReasoning, Text: "hmm"}, {Kind: PartText, Text: "hello"}, {Kind: PartText, Text: " there"}}}},
		{Index: 1, Message: Message{Parts: []Part{{Kind: PartToolCall, ToolCall: &ToolCall{ID: "c1", Name: "f"}}}}},
		{Index: 2, Message: Message{Parts: []Part{{Kind: PartText, Text: "bye"}}}},
	}}
	got := c.TextTargets()
	want := []TextTarget{
		{Choice: 0, Role: RoleAssistant, Part: 1, Text: "hello"},
		{Choice: 0, Role: RoleAssistant, Part: 2, Text: " there"},
		{Choice: 2, Role: RoleAssistant, Part: 0, Text: "bye"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("TextTargets() = %+v\nwant %+v", got, want)
	}
	for _, target := range got {
		if err := c.SetTargetText(target, strings.ToUpper(target.Text)); err != nil {
			t.Fatalf("SetTargetText(%+v) error = %v", target, err)
		}
	}
	if c.Text(0) != "HELLO THERE" || c.Text(2) != "BYE" {
		t.Fatalf("texts = %q, %q", c.Text(0), c.Text(2))
	}
	changes := c.Changes()
	if changes.Messages["choice:0"] != ChangeEdited || changes.Messages["choice:2"] != ChangeEdited {
		t.Fatalf("changes = %+v", changes.Messages)
	}
	if _, ok := changes.Messages["choice:1"]; ok {
		t.Fatal("untouched choice 1 was marked")
	}
	if err := c.SetTargetText(TextTarget{Choice: 1, Part: 0}, "x"); err == nil {
		t.Fatal("editing a tool call part must fail")
	}
	if err := c.SetTargetText(TextTarget{Choice: 7}, "x"); err == nil {
		t.Fatal("editing a missing choice must fail")
	}
}
