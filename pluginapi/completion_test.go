package pluginapi

import "testing"

func sampleCompletion() *Completion {
	c := &Completion{Choices: []Choice{
		{Index: 0, FinishReason: "stop", Message: Message{Role: RoleAssistant, Parts: []Part{
			{Kind: PartReasoning, Text: "thinking"},
			{Kind: PartText, Text: "hello "},
			{Kind: PartText, Text: "world"},
			{Kind: PartToolCall, ToolCall: &ToolCall{ID: "c1", Name: "f"}},
		}}},
		{Index: 1, FinishReason: "stop", Message: TextMessage(RoleAssistant, "second")},
	}}
	c.Reset()
	return c
}

func TestCompletionText(t *testing.T) {
	c := sampleCompletion()
	if got := c.Text(0); got != "hello world" {
		t.Errorf("Text(0) = %q", got)
	}
	if got := c.Text(7); got != "" {
		t.Errorf("Text(7) = %q, want empty", got)
	}
}

func TestCompletionEdits(t *testing.T) {
	tests := []struct {
		name    string
		edit    func(c *Completion) error
		wantErr bool
		wantKey string
		want    ChangeKind
		check   func(t *testing.T, c *Completion)
	}{
		{
			name:    "set text",
			edit:    func(c *Completion) error { return c.SetText(0, 1, "bye ") },
			wantKey: "choice:0", want: ChangeEdited,
			check: func(t *testing.T, c *Completion) {
				if c.Text(0) != "bye world" {
					t.Errorf("text = %q", c.Text(0))
				}
			},
		},
		{name: "set text on reasoning part", edit: func(c *Completion) error { return c.SetText(0, 0, "x") }, wantErr: true},
		{name: "set text bad choice", edit: func(c *Completion) error { return c.SetText(5, 0, "x") }, wantErr: true},
		{name: "set text bad part", edit: func(c *Completion) error { return c.SetText(1, 3, "x") }, wantErr: true},
		{
			name:    "finish reason",
			edit:    func(c *Completion) error { return c.SetFinishReason(1, "content_filter") },
			wantKey: "choice:1", want: ChangeEdited,
			check: func(t *testing.T, c *Completion) {
				if c.Choices[1].FinishReason != "content_filter" {
					t.Error("finish reason not set")
				}
			},
		},
		{name: "finish reason bad choice", edit: func(c *Completion) error { return c.SetFinishReason(9, "stop") }, wantErr: true},
		{
			name:    "replace text keeps non-text parts",
			edit:    func(c *Completion) error { return c.ReplaceText(0, "[redacted]") },
			wantKey: "choice:0", want: ChangeReplaced,
			check: func(t *testing.T, c *Completion) {
				parts := c.Choices[0].Message.Parts
				if len(parts) != 3 || parts[0].Kind != PartReasoning || parts[1].Text != "[redacted]" || parts[2].Kind != PartToolCall {
					t.Errorf("parts = %+v", parts)
				}
			},
		},
		{
			name: "replace text with no text parts prepends",
			edit: func(c *Completion) error {
				c.Choices[1].Message.Parts = []Part{{Kind: PartToolCall, ToolCall: &ToolCall{ID: "z"}}}
				return c.ReplaceText(1, "answer")
			},
			wantKey: "choice:1", want: ChangeReplaced,
			check: func(t *testing.T, c *Completion) {
				parts := c.Choices[1].Message.Parts
				if len(parts) != 2 || parts[0].Text != "answer" || parts[1].Kind != PartToolCall {
					t.Errorf("parts = %+v", parts)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := sampleCompletion()
			err := tt.edit(c)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				if c.Changes().Dirty {
					t.Error("failed edit must not dirty")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			ch := c.Changes()
			if !ch.Dirty || ch.Messages[tt.wantKey] != tt.want {
				t.Errorf("changes = %+v", ch)
			}
			tt.check(t, c)
		})
	}
}

func TestCompletionReplaceThenEditStaysReplaced(t *testing.T) {
	c := sampleCompletion()
	if err := c.ReplaceText(0, "a"); err != nil {
		t.Fatal(err)
	}
	if err := c.SetText(0, 1, "b"); err != nil {
		t.Fatal(err)
	}
	if c.Changes().Messages["choice:0"] != ChangeReplaced {
		t.Error("edit after replace must keep the replaced state")
	}
	c.Reset()
	if c.Changes().Dirty {
		t.Error("Reset must clear tracking")
	}
}
