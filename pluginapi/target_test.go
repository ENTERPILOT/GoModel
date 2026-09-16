package pluginapi

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
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
	require.Equal(t, want, got)

	users := p.TextTargets(RoleUser)
	require.Len(t, users, 2)
	require.Equal(t, "weather?", users[0].Text)
	require.Equal(t, "thanks", users[1].Text)
	got = p.TextTargets(RoleSystem, RoleTool)
	require.Len(t, got, 3)
	got = p.TextTargets(RoleDeveloper)
	require.Nil(t, got)
}

func TestPromptSetTargetText(t *testing.T) {
	p := toolPrompt()
	p.Messages[3].Parts[0].ToolResult.Parts = append(p.Messages[3].Parts[0].ToolResult.Parts, Part{Kind: PartText, Text: "later sun"})
	targets := p.TextTargets()

	for _, target := range targets {
		err := p.SetTargetText(target, strings.ToUpper(target.Text))
		require.NoError(t, err)
	}
	got := p.Messages[1].Parts[0].Text
	require.Equal(t, "WEATHER?", got)

	result := p.Messages[3].Parts[0].ToolResult.Parts
	require.Equal(t, "RAIN", result[0].Text)
	require.Equal(t, "LATER SUN", result[1].Text, "tool result parts = %+v; successive edits must compose", result)

	changes := p.Changes()
	for _, id := range []string{"m0", "m1", "m3", "m4"} {
		require.Equal(t, ChangeEdited, changes.Messages[id], "message %s change = %q, want edited", id, changes.Messages[id])
	}
	_, ok := changes.Messages["m2"]
	require.False(t, ok)

	for _, target := range p.TextTargets() {
		require.Equal(t, strings.ToUpper(target.Text), target.Text, "relisted target %+v does not reflect the edit", target)
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
			require.Error(t, err)
			require.Contains(t, err.Error(), tt.want)
		})
	}
	require.False(t, p.Changes().Dirty)
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
	require.Equal(t, want, got)

	for _, target := range got {
		err := c.SetTargetText(target, strings.ToUpper(target.Text))
		require.NoError(t, err)
	}
	require.Equal(t, "HELLO THERE", c.Text(0))
	require.Equal(t, "BYE", c.Text(2))

	changes := c.Changes()
	require.Equal(t, ChangeEdited, changes.Messages["choice:0"])
	require.Equal(t, ChangeEdited, changes.Messages["choice:2"], "changes = %+v", changes.Messages)
	_, ok := changes.Messages["choice:1"]
	require.False(t, ok)
	require.Error(t, c.SetTargetText(TextTarget{Choice: 1, Part: 0}, "x"))
	require.Error(t, c.SetTargetText(TextTarget{Choice: 7}, "x"))
}
